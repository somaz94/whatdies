// Package script looks one level into programs and Makefiles for infrastructure
// commands. It only reads text, so it can miss a command built at run time; a
// miss leaves the normal permission flow in place.
package script

import (
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// maxBytes bounds a read: a Makefile or script is not this large, and a file
// the agent can write must not be able to stall every later command.
const maxBytes = 1 << 20

// gap stops at a shell separator, so a word in the next command is not this one's verb.
const gap = `\b[^\n|;&]{0,120}?\b`

const (
	separator = `[ \t"',]+`
	// options are what may stand between a tool and a verb that must follow it
	// directly, since debug and exec also turn up as namespaces and file names.
	options = `(?:` + separator + `(?:-n|--namespace|--context|--kubeconfig)` + separator + `[^ \t"',]+|` + separator + `-[^ \t"',=]+=[^ \t"',]+|` + separator + `-n[^ \t"',]+)*`

	terraformTool = `\b(?:terraform|tofu|terragrunt)`
	// The short names must not be the tail of -k, .tf or a path.
	terraformAlias = `(?:` + terraformTool + `|(?:^|[^-\w./])(?:tf|tg))`
	kubeTool       = `\b(?:kubectl|kubecolor|oc)`
)

var mentions = regexp.MustCompile(`(?i)` +
	terraformAlias + gap + `(?:apply|destroy|taint|force-unlock)\b` +
	`|` + terraformAlias + gap + `state` + gap + `(?:rm|mv|push)\b` +
	`|` + terraformAlias + gap + `workspace` + gap + `delete\b` +
	`|` + terraformAlias + gap + `init` + gap + `(?:migrate-state|force-copy)\b` +
	`|` + terraformTool + options + separator + `import\b` +
	`|` + kubeTool + gap + `(?:delete|drain|replace|taint)\b` +
	`|` + kubeTool + options + separator + `(?:exec|attach|debug)\b` +
	`|` + kubeTool + gap + `scale` + gap + `replicas[= ]0\b` +
	`|` + kubeTool + gap + `apply` + gap + `prune\b` +
	`|` + kubeTool + gap + `patch` + gap + `finalizers\b` +
	`|(?:^|[^-\w.])k[ \t]+(?:delete|drain|replace|taint|exec|attach|debug)\b` +
	`|\bhelm` + gap + `(?:uninstall|delete|rollback|upgrade)\b` +
	`|\bhelmfile` + gap + `(?:apply|delete|destroy|sync)\b`)

// inert matches lines that only print or define text: a usage message and an alias
// run nothing, and aliases do not expand in a script anyway.
var inert = regexp.MustCompile("^[ \\t]*(?:[@-]*(?:echo|printf)\\b[^|;&$`>]*|alias[ \\t].*)$")

// continuation joins lines ending in a backslash, which split a command in two.
var continuation = strings.NewReplacer("\\\r\n", " ", "\\\n", " ")

// Mention returns the first infrastructure command found in text, skipping
// comment lines. A comment does not continue: bash ignores a backslash ending one,
// so the next line still runs.
func Mention(text string) string {
	var logical strings.Builder
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		if logical.Len() == 0 && isComment(line) {
			continue
		}
		if before, ok := strings.CutSuffix(line, `\`); ok {
			logical.WriteString(before + " ")
			continue
		}
		logical.WriteString(line)
		if m := mentionIn(logical.String()); m != "" {
			return m
		}
		logical.Reset()
	}
	return mentionIn(logical.String())
}

func mentionIn(line string) string {
	if inert.MatchString(line) {
		return ""
	}
	return strings.TrimLeftFunc(mentions.FindString(line), func(r rune) bool { return !unicode.IsLetter(r) })
}

func isComment(line string) bool {
	line = strings.TrimSpace(line)
	return strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//")
}

func read(path string) (string, bool) {
	// Stat first: opening a FIFO blocks until a writer appears.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// File returns the first infrastructure command in a program file.
func File(path string) string {
	text, ok := read(path)
	if !ok || isBinary(text) {
		return ""
	}
	return Mention(text)
}

// isBinary skips compiled programs named by path, whose strings are not commands.
func isBinary(text string) bool {
	if len(text) > 8192 {
		text = text[:8192]
	}
	return strings.IndexByte(text, 0) >= 0
}

type rule struct {
	prereqs []string
	recipe  []string
	vars    map[string]string
}

var (
	ruleLine        = regexp.MustCompile(`^([A-Za-z0-9_./%-]+(?:[ \t]+[A-Za-z0-9_./%-]+)*)[ \t]*:([^=].*)?$`)
	varLine         = regexp.MustCompile(`^[ \t]*(?:(?:export|override|private)[ \t]+)*([A-Za-z0-9_.]+)[ \t]*(:{1,3}|[?+!])?=[ \t]*(.*)$`)
	defineLine      = regexp.MustCompile(`^[ \t]*(?:(?:export|override|private)[ \t]+)*define[ \t]+([A-Za-z0-9_.]+)`)
	makeConditional = regexp.MustCompile(`^[ \t]*(?:ifeq|ifneq|ifdef|ifndef|else|endif)\b`)
	includeLine     = regexp.MustCompile(`^-?s?include[ \t]+(.+)$`)
)

type makefileDoc struct {
	rules map[string]*rule
	vars  map[string]string
	first string
}

// parseMakefile starts from env because make imports the environment, and a
// file's plain assignment replaces what it imported while ?= keeps it.
func parseMakefile(text string, env map[string]string) makefileDoc {
	doc := makefileDoc{rules: map[string]*rule{}, vars: maps.Clone(env)}
	if doc.vars == nil {
		doc.vars = map[string]string{}
	}
	var current []*rule
	var defining string
	var body []string
	for _, line := range strings.Split(continuation.Replace(text), "\n") {
		if defining != "" {
			if strings.TrimSpace(line) == "endef" {
				doc.vars[defining] = strings.Join(body, "\n")
				defining, body = "", nil
			} else {
				body = append(body, line)
			}
			continue
		}
		if strings.HasPrefix(line, "\t") {
			for _, r := range current {
				r.recipe = append(r.recipe, line)
			}
			continue
		}
		// A conditional or a comment between recipe lines does not end the rule.
		if makeConditional.MatchString(line) || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if m := defineLine.FindStringSubmatch(line); m != nil {
			defining = m[1]
			continue
		}
		if m := varLine.FindStringSubmatch(line); m != nil {
			doc.assign(m[1], m[2], m[3])
			current = nil
			continue
		}
		m := ruleLine.FindStringSubmatch(line)
		if m == nil {
			if strings.TrimSpace(line) != "" {
				current = nil
			}
			continue
		}
		current = doc.addRule(strings.Fields(m[1]), m[2])
	}
	return doc
}

func (d *makefileDoc) addRule(names []string, after string) []*rule {
	body, _, _ := strings.Cut(after, "#")
	prereqs, inline, _ := strings.Cut(body, ";")
	targetVar := varLine.FindStringSubmatch(prereqs)
	var added []*rule
	for _, name := range names {
		r := d.rules[name]
		if r == nil {
			r = &rule{vars: map[string]string{}}
			d.rules[name] = r
		}
		if targetVar != nil {
			r.vars[targetVar[1]] = targetVar[3]
		} else {
			r.prereqs = append(r.prereqs, strings.Fields(prereqs)...)
		}
		if strings.TrimSpace(inline) != "" {
			r.recipe = append(r.recipe, inline)
		}
		added = append(added, r)
		if d.first == "" && !strings.HasPrefix(name, ".") && !strings.Contains(name, "%") {
			d.first = name
		}
	}
	return added
}

func (d makefileDoc) assign(name, op, value string) {
	have, set := d.vars[name]
	switch {
	case op == "?" && set:
	case op == "+" && set:
		d.vars[name] = have + " " + value
	default:
		d.vars[name] = value
	}
}

// expand fills in variables, since `$(TF) destroy` is how a Makefile usually spells
// it. It repeats until nothing changes: one pass over a map resolves a nested
// variable only when the order happens to suit it, which made the verdict random.
func (d makefileDoc) expand(line string, local map[string]string) string {
	vars := d.vars
	if len(local) > 0 {
		vars = maps.Clone(d.vars)
		maps.Copy(vars, local)
	}
	for range len(vars) + 1 {
		before := line
		for name, value := range vars {
			line = strings.ReplaceAll(line, "$("+name+")", value)
			line = strings.ReplaceAll(line, "${"+name+"}", value)
			if len(name) == 1 {
				line = strings.ReplaceAll(line, "$"+name, value)
			}
		}
		if line == before || len(line) > maxBytes {
			break
		}
	}
	return line
}

// matched is a rule that builds a target, with the stem a % pattern captured.
type matched struct {
	rule *rule
	stem string
}

func (d makefileDoc) match(name string) []matched {
	if r := d.rules[name]; r != nil {
		return []matched{{rule: r}}
	}
	var out []matched
	for _, pattern := range slices.Sorted(maps.Keys(d.rules)) {
		prefix, suffix, ok := strings.Cut(pattern, "%")
		if ok && len(name) > len(prefix)+len(suffix) && strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
			out = append(out, matched{d.rules[pattern], name[len(prefix) : len(name)-len(suffix)]})
		}
	}
	return out
}

// automatic fills in the variables make sets per target, as in `terraform $@`.
func automatic(text, target, stem string) string {
	return strings.NewReplacer(
		"$@", target, "$(@)", target, "${@}", target,
		"$*", stem, "$(*)", stem, "${*}", stem,
	).Replace(text)
}

// Make returns the first infrastructure command the named targets and their
// prerequisites would run. A target the file does not define may come from an
// include this cannot read, so the whole file is searched instead. env is the
// environment the command sets, and vars the assignments on make's command line,
// which outrank the file.
func Make(dir, file string, targets []string, env, vars map[string]string) string {
	text, ok := makefile(dir, file)
	if !ok {
		return ""
	}
	text = withIncludes(dir, text)
	doc := parseMakefile(text, env)
	maps.Copy(doc.vars, vars)
	if len(targets) == 0 {
		goal := doc.vars[".DEFAULT_GOAL"]
		if goal == "" {
			goal = doc.first
		}
		if goal == "" {
			return ""
		}
		targets = []string{goal}
	}
	seen := map[string]bool{}
	pending := append([]string{}, targets...)
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		rules := doc.match(name)
		if len(rules) == 0 && contains(targets, name) {
			return Mention(text)
		}
		for _, m := range rules {
			recipe := automatic(strings.Join(m.rule.recipe, "\n"), name, m.stem)
			if hit := Mention(doc.expand(recipe, m.rule.vars)); hit != "" {
				return hit
			}
			prereqs := strings.ReplaceAll(strings.Join(m.rule.prereqs, " "), "%", m.stem)
			pending = append(pending, strings.Fields(doc.expand(automatic(prereqs, name, m.stem), m.rule.vars))...)
		}
	}
	return ""
}

// withIncludes appends the files an include line names, so a target or variable
// defined there is found. A name that is computed or missing is skipped, which
// leaves the target undefined and sends the search to the whole text.
func withIncludes(dir, text string) string {
	seen := map[string]bool{}
	for pass := 0; pass < 5 && len(text) < maxBytes; pass++ {
		added := false
		for _, line := range strings.Split(text, "\n") {
			m := includeLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			for _, name := range strings.Fields(m[1]) {
				if seen[name] || strings.ContainsAny(name, "$*?[") {
					continue
				}
				seen[name] = true
				if body, ok := makefile(dir, name); ok {
					text += "\n" + body
					added = true
				}
			}
		}
		if !added {
			break
		}
	}
	return text
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func makefile(dir, file string) (string, bool) {
	if file != "" {
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
		return read(file)
	}
	for _, name := range []string{"GNUmakefile", "makefile", "Makefile"} {
		if text, ok := read(filepath.Join(dir, name)); ok {
			return text, true
		}
	}
	return "", false
}
