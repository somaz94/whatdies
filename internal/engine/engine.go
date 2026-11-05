// Package engine turns a command line into one verdict.
package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/somaz94/whatdies/internal/kube"
	"github.com/somaz94/whatdies/internal/registry"
	"github.com/somaz94/whatdies/internal/runner"
	"github.com/somaz94/whatdies/internal/script"
	"github.com/somaz94/whatdies/internal/shell"
	"github.com/somaz94/whatdies/internal/terraform"
	"github.com/somaz94/whatdies/internal/verdict"
)

// Deps are the outside inputs of a verdict.
type Deps struct {
	Runner runner.Runner
	// ExtraStateful adds resource types for this run only. It can only make a
	// verdict stricter.
	ExtraStateful []string
}

// Default returns the real dependencies.
func Default() Deps { return Deps{Runner: runner.Exec{}} }

// quietHeads read their arguments as text: naming a tool there is not running it.
// sed and rg can execute, so judgeReader looks at those two.
var quietHeads = map[string]bool{
	"ag": true, "bat": true, "cat": true, "echo": true, "egrep": true, "fgrep": true, "gh": true,
	"glab": true, "grep": true, "head": true, "jq": true, "less": true, "ls": true, "man": true,
	"more": true, "printf": true, "tail": true, "tee": true, "touch": true, "type": true,
	"wc": true, "whatdies": true, "which": true, "yq": true,
}

var mutatingWords = map[string]bool{
	"apply": true, "del": true, "delete": true, "destroy": true, "drain": true, "exec": true,
	"force-unlock": true, "import": true, "mv": true, "patch": true, "replace": true, "rm": true,
	"rollback": true, "scale": true, "sync": true, "taint": true, "un": true, "uninstall": true, "upgrade": true,
}

// Evaluate judges a command line run from cwd.
func Evaluate(ctx context.Context, command, cwd string, d Deps) verdict.Verdict {
	analysis := shell.Analyze(command, cwd)
	if analysis.ParseError != nil {
		if m := script.Mention(command); m != "" {
			return verdict.AskBecause(fmt.Sprintf("the command could not be parsed and mentions %q", m))
		}
		return verdict.Verdict{}
	}
	var out verdict.Verdict
	for _, p := range analysis.Problems {
		out = verdict.Join(out, verdict.AskBecause(p))
	}
	for i, inv := range analysis.Invocations {
		out = verdict.Join(out, judge(ctx, inv, analysis.Invocations[:i], d))
	}
	return out
}

func judge(ctx context.Context, inv shell.Invocation, earlier []shell.Invocation, d Deps) verdict.Verdict {
	switch inv.Tool {
	case "":
		return judgeComputedHead(inv)
	case "terraform":
		return judgeTerraform(ctx, inv, earlier, d)
	case "kubectl":
		return kube.Evaluate(ctx, inv, kube.Deps{Runner: d.Runner})
	case "oc":
		return kube.Evaluate(ctx, inv, kube.Deps{Runner: d.Runner, NoProbe: "oc is not supported for lookups yet"})
	case "helm":
		return judgeHelm(inv)
	case "tofu", "terragrunt":
		return judgeTerraformLike(inv)
	case "helmfile":
		return judgeHelmfile(inv)
	case "make", "gmake":
		return judgeMake(inv)
	case shell.ScriptTool:
		return judgeScript(inv, earlier)
	case shell.CodeTool:
		return judgeCode(inv)
	case "git":
		return judgeGit(inv)
	case "sed", "gsed", "rg":
		return judgeReader(inv)
	}
	return judgeOther(inv)
}

func judgeTerraform(ctx context.Context, inv shell.Invocation, earlier []shell.Invocation, d Deps) verdict.Verdict {
	base, err := registry.Stateful()
	if err != nil {
		return verdict.AskBecause(err.Error())
	}
	stateful := base
	if len(d.ExtraStateful) > 0 {
		stateful = make(map[string]bool, len(base)+len(d.ExtraStateful))
		for k := range base {
			stateful[k] = true
		}
		for _, t := range d.ExtraStateful {
			stateful[t] = true
		}
	}
	return terraform.Evaluate(ctx, inv, earlier, terraform.Deps{Runner: d.Runner, Stateful: stateful})
}

func judgeComputedHead(inv shell.Invocation) verdict.Verdict {
	for name, value := range inv.Env {
		if !value.Literal {
			continue
		}
		if m := script.Mention(value.Value); m != "" {
			return verdict.AskBecause(fmt.Sprintf("the program is computed at run time and %s holds %q", name, m))
		}
	}
	for _, v := range inv.Literals() {
		if mutatingWords[v] {
			return verdict.AskBecause(fmt.Sprintf("the program is computed at run time and is passed %q", v))
		}
	}
	return verdict.Verdict{}
}

// firstWord returns the first word that is not an option or its value. unknown
// is true when an option not in the table came first: it may have taken the
// following word as its value, so the word returned may not be the verb.
func firstWord(args []shell.Arg, valueFlags map[string]bool) (word string, ok, unknown bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.Literal {
			return "", false, unknown
		}
		if !strings.HasPrefix(a.Value, "-") {
			return a.Value, true, unknown
		}
		switch {
		case valueFlags[a.Value]:
			i++
		case !strings.Contains(a.Value, "="):
			unknown = true
		}
	}
	return "", true, unknown
}

func anyLiteral(inv shell.Invocation, set map[string]bool) string {
	for _, v := range inv.Literals() {
		if set[v] {
			return v
		}
	}
	return ""
}

var helmValueFlags = map[string]bool{
	"--burst-limit": true, "--kube-apiserver": true, "--kube-as-group": true, "--kube-as-user": true,
	"--kube-ca-file": true, "--kube-context": true, "--kube-token": true, "--kubeconfig": true,
	"--namespace": true, "-n": true, "--qps": true, "--registry-config": true,
	"--repository-cache": true, "--repository-config": true,
}

var helmDestructive = map[string]bool{
	"uninstall": true, "del": true, "delete": true, "un": true, "upgrade": true, "rollback": true,
}

func judgeHelm(inv shell.Invocation) verdict.Verdict {
	verb, ok, unknown := firstWord(inv.Args, helmValueFlags)
	if !ok {
		return verdict.AskBecause("helm: the verb is computed at run time")
	}
	if unknown && !helmDestructive[verb] {
		verb = anyLiteral(inv, helmDestructive)
	}
	if helmDestructive[verb] {
		if helmDryRun(inv) {
			return verdict.Verdict{}
		}
		return verdict.AskBecause("helm " + verb + " can remove or replace what the release owns; whatdies does not analyse helm yet")
	}
	return verdict.Verdict{}
}

func helmDryRun(inv shell.Invocation) bool {
	for _, v := range inv.Literals() {
		switch v {
		case "--dry-run", "--dry-run=client", "--dry-run=server", "--dry-run=true":
			return true
		}
	}
	return false
}

var terraformDestructive = map[string]bool{
	"apply": true, "destroy": true, "force-unlock": true, "import": true, "refresh": true, "taint": true,
}

func judgeTerraformLike(inv shell.Invocation) verdict.Verdict {
	verb, ok, unknown := firstWord(inv.Args, nil)
	if !ok {
		return verdict.AskBecause(inv.Tool + ": the subcommand is computed at run time")
	}
	if verb == "init" {
		return terraform.JudgeInit(inv.Args)
	}
	if unknown {
		if v := anyLiteral(inv, terraformDestructive); v != "" {
			verb = v
		}
	}
	if verb == "" || terraform.ReadOnlySubcommand(verb) {
		return verdict.Verdict{}
	}
	return verdict.AskBecause(fmt.Sprintf("%s %s is not analysed yet and can change infrastructure", inv.Tool, verb))
}

var helmfileValueFlags = map[string]bool{
	"--environment": true, "-e": true, "--file": true, "-f": true, "--kube-context": true,
	"--namespace": true, "-n": true, "--selector": true, "-l": true, "--state-values-file": true,
}

var helmfileReadOnly = map[string]bool{
	"build": true, "deps": true, "diff": true, "fetch": true, "lint": true, "list": true,
	"repos": true, "status": true, "template": true, "version": true, "write-values": true,
}

func judgeHelmfile(inv shell.Invocation) verdict.Verdict {
	verb, ok, _ := firstWord(inv.Args, helmfileValueFlags)
	if !ok {
		return verdict.AskBecause("helmfile: the subcommand is computed at run time")
	}
	if verb == "" || helmfileReadOnly[verb] {
		return verdict.Verdict{}
	}
	return verdict.AskBecause("helmfile " + verb + " can remove or replace releases; whatdies does not analyse helmfile yet")
}

func judgeMake(inv shell.Invocation) verdict.Verdict {
	if !inv.DirKnown {
		return verdict.Verdict{}
	}
	dir, file := inv.Dir, ""
	var targets []string
	for i := 0; i < len(inv.Args); i++ {
		a := inv.Args[i]
		if !a.Literal {
			continue
		}
		next := func() (string, bool) {
			i++
			if i < len(inv.Args) && inv.Args[i].Literal {
				return inv.Args[i].Value, true
			}
			return "", false
		}
		v := a.Value
		switch {
		case v == "-C" || v == "--directory":
			if value, ok := next(); ok {
				dir = resolve(dir, value)
			}
		case strings.HasPrefix(v, "--directory="):
			dir = resolve(dir, strings.TrimPrefix(v, "--directory="))
		case strings.HasPrefix(v, "-C"):
			dir = resolve(dir, v[2:])
		case v == "-f" || v == "--file" || v == "--makefile":
			if value, ok := next(); ok {
				file = value
			}
		case strings.HasPrefix(v, "--file="), strings.HasPrefix(v, "--makefile="):
			_, file, _ = strings.Cut(v, "=")
		case strings.HasPrefix(v, "-f") && !strings.HasPrefix(v, "--"):
			file = v[2:]
		case strings.HasPrefix(v, "-") || strings.Contains(v, "="):
		default:
			targets = append(targets, v)
		}
	}
	if m := script.Make(dir, file, targets); m != "" {
		return verdict.AskBecause(fmt.Sprintf("this make target runs %q", m))
	}
	return verdict.Verdict{}
}

func resolve(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

// fileWriters put content in a file named among their arguments.
var fileWriters = map[string]bool{
	"cp": true, "curl": true, "dd": true, "install": true, "ln": true, "mv": true,
	"rsync": true, "scp": true, "tee": true, "wget": true,
}

func judgeScript(inv shell.Invocation, earlier []shell.Invocation) verdict.Verdict {
	if len(inv.Args) == 0 || !inv.Args[0].Literal {
		return verdict.Verdict{}
	}
	path := inv.Args[0].Value
	if m := script.Mention(strings.Join(inv.Literals()[1:], " ")); m != "" {
		return verdict.AskBecause(fmt.Sprintf("%s appears to run %q", path, m))
	}
	for i := len(earlier) - 1; i >= 0; i-- {
		if v, written := judgeWritten(path, earlier[i]); written {
			return v
		}
	}
	if !inv.DirKnown {
		return verdict.Verdict{}
	}
	if m := script.File(resolve(inv.Dir, path)); m != "" {
		return verdict.AskBecause(fmt.Sprintf("%s runs %q", path, m))
	}
	return verdict.Verdict{}
}

// judgeWritten handles a script the same command line creates, which is not on
// disk yet when the hook runs. Agents do this often with harmless scripts, so
// text the line itself holds is read rather than refused.
func judgeWritten(path string, writer shell.Invocation) (verdict.Verdict, bool) {
	redirected := shell.Invocation{Writes: writer.Writes}.NamesFile(path)
	if !redirected && !(fileWriters[writer.Tool] && writer.ArgNamesFile(path)) {
		return verdict.Verdict{}, false
	}
	var text string
	switch {
	case !redirected:
		return verdict.AskBecause(fmt.Sprintf("%s is written by %s just before it runs", path, writer.Tool)), true
	case writer.HasHeredoc && writer.HeredocLiteral:
		text = writer.Heredoc
	case (writer.Tool == "echo" || writer.Tool == "printf") && len(writer.Literals()) == len(writer.Args):
		text = strings.Join(writer.Literals(), " ")
		text = strings.ReplaceAll(text, `\n`, "\n")
	default:
		return verdict.AskBecause(fmt.Sprintf("%s is written by %s just before it runs", path, writer.Tool)), true
	}
	if m := script.Mention(text); m != "" {
		return verdict.AskBecause(fmt.Sprintf("%s runs %q", path, m)), true
	}
	return verdict.Verdict{}, true
}

func judgeCode(inv shell.Invocation) verdict.Verdict {
	if len(inv.Args) == 0 {
		return verdict.Verdict{}
	}
	if !inv.Args[0].Literal {
		return verdict.AskBecause("inline program text is computed at run time")
	}
	if m := script.Mention(inv.Args[0].Value); m != "" {
		return verdict.AskBecause(fmt.Sprintf("inline program text runs %q", m))
	}
	return verdict.Verdict{}
}

// judgeOther catches wrappers no list can enumerate, such as `ssh host kubectl
// delete ...` or `aws-vault exec prod -- terraform destroy`.
func judgeOther(inv shell.Invocation) verdict.Verdict {
	if quietHeads[inv.Tool] {
		return verdict.Verdict{}
	}
	text := inv.Tool + " " + strings.Join(inv.Literals(), " ")
	if m := script.Mention(text); m != "" {
		return verdict.AskBecause(fmt.Sprintf("%s appears to run %q", inv.Tool, m))
	}
	return verdict.Verdict{}
}

// judgeGit stays quiet about commit messages and the like, and looks only where
// git runs what it is given: aliases, rebase --exec and bisect run.
func judgeGit(inv shell.Invocation) verdict.Verdict {
	words := inv.Literals()
	runs := false
	for i, w := range words {
		switch {
		case w == "rebase", w == "bisect" && i+1 < len(words) && words[i+1] == "run", strings.HasPrefix(w, "alias."):
			runs = true
		}
	}
	if !runs {
		return verdict.Verdict{}
	}
	if m := script.Mention(strings.Join(words, " ")); m != "" {
		return verdict.AskBecause(fmt.Sprintf("git would run %q", m))
	}
	return verdict.Verdict{}
}

// sedExecute matches the s command's e flag, which runs the result as a command.
var sedExecute = regexp.MustCompile(`^(?:[0-9,$]*)s(.).*?[a-zA-Z0-9]*e[a-zA-Z0-9]*$`)

// judgeReader stays quiet about search text, and looks only where sed or rg run
// what they are given: sed's e flag and rg --pre.
func judgeReader(inv shell.Invocation) verdict.Verdict {
	runs := false
	for _, w := range inv.Literals() {
		if w == "--pre" || strings.HasPrefix(w, "--pre=") || sedExecute.MatchString(w) {
			runs = true
		}
	}
	if !runs {
		return verdict.Verdict{}
	}
	if m := script.Mention(strings.Join(inv.Literals(), " ")); m != "" {
		return verdict.AskBecause(fmt.Sprintf("%s would run %q", inv.Tool, m))
	}
	return verdict.Verdict{}
}
