// Package terraform judges terraform commands by what their saved plan does.
package terraform

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/somaz94/whatdies/internal/shell"
	"github.com/somaz94/whatdies/internal/verdict"
)

// planPath picks where the advice saves a plan. Plans hold secrets in clear text and
// .terraform/ is normally git-ignored, but terraform will not create it for -out, so
// without that directory it falls back to a plain file.
func planPath(inv shell.Invocation, chdir string) (path, note string) {
	if inv.DirKnown {
		info, err := os.Stat(filepath.Join(resolve(inv.Dir, chdir), ".terraform"))
		if err == nil && info.IsDir() {
			return ".terraform/whatdies.tfplan", ""
		}
	}
	return "whatdies.tfplan", " The plan holds sensitive values in clear text: do not commit it."
}

func resolve(dir, path string) string {
	if path == "" {
		return dir
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

// Deps are the outside inputs of a verdict.
type Deps struct {
	// Stateful returns the set of resource types whose deletion loses data. It is a
	// function so that the many terraform commands that read no plan do not parse it.
	Stateful func() (map[string]bool, error)
	// Guards maps a resource type to the attributes that guard its data and the
	// value that turns each off.
	Guards func() (map[string]map[string]bool, error)
}

var readOnly = map[string]bool{
	"console": true, "fmt": true, "get": true, "graph": true, "help": true, "init": true, "login": true,
	"logout": true, "metadata": true, "modules": true, "output": true, "plan": true,
	"providers": true, "show": true, "validate": true, "version": true,
}

// ReadOnlySubcommand reports whether a subcommand cannot change infrastructure.
func ReadOnlySubcommand(sub string) bool { return readOnly[sub] }

// Evaluate judges one terraform invocation. earlier holds the invocations that
// run before it in the same command line.
func Evaluate(inv shell.Invocation, earlier []shell.Invocation, d Deps) verdict.Verdict {
	sub, rest, chdir, problem := parseGlobal(inv.Args)
	if problem != "" {
		return verdict.AskBecause("terraform: " + problem)
	}
	switch {
	case sub == "":
		return verdict.Verdict{}
	case sub == "init":
		return JudgeInit(rest)
	case readOnly[sub]:
		return verdict.Verdict{}
	case sub == "state":
		return evaluateState(rest)
	case sub == "workspace":
		if first(rest) == "delete" || !allLiteral(rest) {
			return verdict.AskBecause("terraform workspace delete drops a workspace's state")
		}
		return verdict.Verdict{}
	case sub == "destroy":
		return noSavedPlan(inv, chdir, "terraform destroy", "terraform plan -destroy")
	case sub == "apply":
		return evaluateApply(inv, rest, chdir, earlier, d)
	}
	return verdict.AskBecause(fmt.Sprintf("terraform %s can change state or infrastructure", sub))
}

// JudgeInit finds the init options that move state to another backend.
func JudgeInit(args []shell.Arg) verdict.Verdict {
	for _, a := range args {
		name, _, _ := strings.Cut(strings.TrimLeft(a.Value, "-"), "=")
		if !a.Literal || name == "migrate-state" || name == "force-copy" {
			return verdict.AskBecause("init with these arguments can migrate state to another backend")
		}
	}
	return verdict.Verdict{}
}

func first(args []shell.Arg) string {
	if len(args) == 0 {
		return ""
	}
	return args[0].Value
}

func allLiteral(args []shell.Arg) bool {
	for _, a := range args {
		if !a.Literal {
			return false
		}
	}
	return true
}

func parseGlobal(args []shell.Arg) (sub string, rest []shell.Arg, chdir, problem string) {
	for i, a := range args {
		if !a.Literal {
			return "", nil, "", "the subcommand or a global option is computed at run time"
		}
		if !strings.HasPrefix(a.Value, "-") {
			return a.Value, args[i+1:], chdir, ""
		}
		name := strings.TrimLeft(a.Value, "-")
		switch {
		case strings.HasPrefix(name, "chdir="):
			chdir = strings.TrimPrefix(name, "chdir=")
		case name == "help" || name == "h" || name == "version" || name == "v":
			return "", nil, "", ""
		default:
			return "", nil, "", fmt.Sprintf("unknown global option %q", a.Value)
		}
	}
	return "", nil, chdir, ""
}

func evaluateState(rest []shell.Arg) verdict.Verdict {
	switch first(rest) {
	case "list", "show", "pull":
		if allLiteral(rest[:1]) {
			return verdict.Verdict{}
		}
	}
	return verdict.AskBecause(strings.TrimSpace("terraform state "+first(rest)) + " edits state directly, which a plan cannot show")
}

func noSavedPlan(inv shell.Invocation, chdir, what, planCmd string) verdict.Verdict {
	path, note := planPath(inv, chdir)
	return verdict.DenyBecause(
		fmt.Sprintf("Run `%s -out=%s` in its own Bash call, then `terraform apply %s` in a separate call so whatdies can read the plan.%s "+
			"If saving a plan is not possible here, stop and ask the user to run this themselves.", planCmd, path, path, note),
		what+" without a saved plan cannot be checked: nothing shows what it would destroy",
	)
}

var applyValueFlags = map[string]bool{
	"backup": true, "lock-timeout": true, "parallelism": true, "replace": true,
	"state": true, "state-out": true, "target": true, "var": true, "var-file": true,
}

var applyBoolFlags = map[string]bool{
	"auto-approve": true, "compact-warnings": true, "destroy": true, "input": true, "json": true,
	"lock": true, "no-color": true, "refresh": true, "refresh-only": true,
}

func evaluateApply(inv shell.Invocation, args []shell.Arg, chdir string, earlier []shell.Invocation, d Deps) verdict.Verdict {
	var positional []shell.Arg
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.Literal {
			return verdict.AskBecause("terraform apply has an argument computed at run time")
		}
		if !strings.HasPrefix(a.Value, "-") || a.Value == "-" {
			positional = append(positional, a)
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(a.Value, "-"), "=")
		switch {
		case applyBoolFlags[name]:
		case applyValueFlags[name]:
			if !hasValue {
				i++
			}
		default:
			return verdict.AskBecause(fmt.Sprintf("terraform apply has an option whatdies does not know: %q", a.Value))
		}
	}
	if len(positional) == 0 {
		return noSavedPlan(inv, chdir, "terraform apply", "terraform plan")
	}
	if len(positional) > 1 {
		return verdict.AskBecause("terraform apply has more than one positional argument")
	}
	plan := positional[0].Value

	if prior := writesPlan(plan, earlier); prior != "" {
		return verdict.DenyBecause(
			"Split this: run the plan in one Bash call and `terraform apply "+plan+"` in the next.",
			fmt.Sprintf("%s runs first in the same command, so the plan file on disk now is not the one that would be applied", prior),
		)
	}
	if overridden := envOverride(inv); overridden != "" {
		return verdict.AskBecause("the command sets " + overridden + ", which can change what terraform does")
	}
	if inv.TargetChange != "" {
		return verdict.AskBecause("an earlier step (" + inv.TargetChange + ") changes the target before this apply")
	}
	if !inv.Bare {
		return verdict.AskBecause("this is not the plain terraform on PATH, which may read the plan differently")
	}
	if !inv.DirKnown {
		return verdict.AskBecause("the working directory is computed at run time, so the plan file cannot be found")
	}
	if strings.HasPrefix(plan, "-") {
		return verdict.AskBecause("the plan file name looks like an option")
	}
	return readPlan(resolve(inv.Dir, chdir), plan, d)
}

// envOverride finds variables the command itself sets that change terraform's
// behaviour without appearing in its arguments. TF_CLI_ARGS from the user's own
// environment is not one: with a saved plan it cannot change what gets applied,
// because terraform rejects -target, -replace and -destroy alongside a plan file.
// Decided 2026-11-05, after the rule fired on a real shell and blocked every plan.
func envOverride(inv shell.Invocation) string {
	for _, k := range slices.Sorted(maps.Keys(inv.Env)) {
		if strings.HasPrefix(k, "TF_CLI_ARGS") || k == "TF_WORKSPACE" || k == "TF_DATA_DIR" || k == "PATH" {
			return k
		}
	}
	return ""
}

// planReaders name the plan file without being able to change it.
var planReaders = map[string]bool{
	"[": true, "cat": true, "file": true, "head": true, "ls": true, "shasum": true,
	"stat": true, "tail": true, "test": true, "wc": true,
}

// writesPlan finds an earlier step that may create or replace the plan file.
func writesPlan(plan string, earlier []shell.Invocation) string {
	for _, e := range earlier {
		sub := ""
		if e.Tool == "terraform" || e.Tool == "tofu" || e.Tool == "terragrunt" {
			sub, _, _, _ = parseGlobal(e.Args)
			if sub == "plan" {
				return e.Tool + " plan"
			}
		}
		if e.WritesFile(plan) {
			return e.Tool
		}
		if planReaders[e.Tool] || sub == "show" {
			continue
		}
		if e.ArgNamesFile(plan) {
			return e.Tool
		}
	}
	return ""
}

func readPlan(dir, plan string, d Deps) verdict.Verdict {
	if d.Guards == nil || d.Stateful == nil {
		return verdict.AskBecause("whatdies was built without its resource lists")
	}
	guards, err := d.Guards()
	if err != nil {
		return verdict.AskBecause(err.Error())
	}
	facts, problem := readPlanFile(resolve(dir, plan), guards)
	if problem != "" {
		return verdict.AskBecause(fmt.Sprintf("could not read plan %q: %s", plan, problem))
	}
	stateful, err := d.Stateful()
	if err != nil {
		return verdict.AskBecause(err.Error())
	}
	return judgeFacts(facts, stateful)
}
