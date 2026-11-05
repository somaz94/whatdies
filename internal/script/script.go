// Package script looks one level into programs and Makefiles for infrastructure
// commands. It only reads text, so it can miss a command built at run time; a
// miss leaves the normal permission flow in place.
package script

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// maxBytes bounds a read: a Makefile or script is not this large, and a file
// the agent can write must not be able to stall every later command.
const maxBytes = 1 << 20

const gap = `\b[^\n]{0,120}?\b`

var mentions = regexp.MustCompile(`(?i)` +
	`\b(?:terraform|tofu|terragrunt|tf|tg)` + gap + `(?:apply|destroy|taint|force-unlock)\b` +
	`|\b(?:terraform|tofu|terragrunt|tf|tg)` + gap + `state` + gap + `(?:rm|mv|push)\b` +
	`|\b(?:kubectl|kubecolor|oc)` + gap + `(?:delete|drain|replace|taint)\b` +
	`|\bk[ \t]+(?:delete|drain|replace|taint)\b` +
	`|\bhelm` + gap + `(?:uninstall|delete|rollback|upgrade)\b` +
	`|\bhelmfile` + gap + `(?:apply|delete|destroy|sync)\b`)

// continuation joins lines ending in a backslash, which split a command in two.
var continuation = strings.NewReplacer("\\\r\n", " ", "\\\n", " ")

// Mention returns the first infrastructure command found in text, skipping
// comment lines.
func Mention(text string) string {
	text = continuation.Replace(text)
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), maxBytes+1)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if m := mentions.FindString(line); m != "" {
			return m
		}
	}
	return ""
}

func read(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
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
}

var (
	ruleLine = regexp.MustCompile(`^([A-Za-z0-9_./%-]+(?:[ \t]+[A-Za-z0-9_./%-]+)*)[ \t]*:([^=].*)?$`)
	varLine  = regexp.MustCompile(`^[ \t]*([A-Za-z0-9_.]+)[ \t]*[:?+!]?=[ \t]*(.*)$`)
)

type makefileDoc struct {
	rules map[string]*rule
	vars  map[string]string
	first string
}

func parseMakefile(text string) makefileDoc {
	doc := makefileDoc{rules: map[string]*rule{}, vars: map[string]string{}}
	var current []*rule
	for _, line := range strings.Split(continuation.Replace(text), "\n") {
		if strings.HasPrefix(line, "\t") {
			for _, r := range current {
				r.recipe = append(r.recipe, line)
			}
			continue
		}
		if m := varLine.FindStringSubmatch(line); m != nil {
			doc.vars[m[1]] = m[2]
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
		current = nil
		body, _, _ := strings.Cut(m[2], "#")
		prereqs, inline, _ := strings.Cut(body, ";")
		for _, name := range strings.Fields(m[1]) {
			r := doc.rules[name]
			if r == nil {
				r = &rule{}
				doc.rules[name] = r
			}
			r.prereqs = append(r.prereqs, strings.Fields(prereqs)...)
			if strings.TrimSpace(inline) != "" {
				r.recipe = append(r.recipe, inline)
			}
			current = append(current, r)
			if doc.first == "" && !strings.HasPrefix(name, ".") {
				doc.first = name
			}
		}
	}
	return doc
}

// expand fills in simple variables, since `$(TF) destroy` is how a Makefile
// usually spells it.
func (d makefileDoc) expand(line string) string {
	for name, value := range d.vars {
		line = strings.ReplaceAll(line, "$("+name+")", value)
		line = strings.ReplaceAll(line, "${"+name+"}", value)
	}
	return line
}

// Make returns the first infrastructure command the named targets and their
// prerequisites would run. A target the file does not define may come from an
// include this does not read, so the whole file is searched instead.
func Make(dir, file string, targets []string) string {
	text, ok := makefile(dir, file)
	if !ok {
		return ""
	}
	doc := parseMakefile(text)
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
		r := doc.rules[name]
		if r == nil {
			if contains(targets, name) {
				return Mention(text)
			}
			continue
		}
		if m := Mention(doc.expand(strings.Join(r.recipe, "\n"))); m != "" {
			return m
		}
		pending = append(pending, r.prereqs...)
	}
	return ""
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
