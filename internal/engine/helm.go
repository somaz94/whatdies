package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/somaz94/whatdies/internal/kube"
	"github.com/somaz94/whatdies/internal/shell"
	"github.com/somaz94/whatdies/internal/terraform"
	"github.com/somaz94/whatdies/internal/verdict"
)

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

func judgeHelm(ctx context.Context, inv shell.Invocation, d Deps) verdict.Verdict {
	verb, ok, unknown := firstWord(inv.Args, helmValueFlags)
	if !ok {
		return verdict.AskBecause("helm: the verb is computed at run time")
	}
	if unknown && !helmDestructive[verb] {
		verb = anyLiteral(inv, helmDestructive)
	}
	if !helmDestructive[verb] {
		return verdict.Verdict{}
	}
	switch {
	case verb == "upgrade" || unknown:
		if helmDryRun(inv) {
			return verdict.Verdict{}
		}
		if unknown {
			return verdict.AskBecause("helm " + verb + " can remove or replace what the release owns; an option before it is not one whatdies knows")
		}
		// Knowing what an upgrade removes means rendering the new chart, which can
		// download it and run post-renderers and plugins.
		return verdict.AskBecause("helm upgrade can remove or replace what the release owns; whatdies does not render charts")
	case verb == "rollback":
		return kube.EvaluateHelm(ctx, inv, verb, kube.Deps{Runner: d.Runner})
	}
	return kube.EvaluateHelm(ctx, inv, "uninstall", kube.Deps{Runner: d.Runner})
}

// helmUpgradeBools take no value, so a --dry-run after one is the flag itself.
var helmUpgradeBools = map[string]bool{
	"--atomic": true, "--cleanup-on-fail": true, "--create-namespace": true, "--debug": true,
	"--dependency-update": true, "--devel": true, "--disable-openapi-validation": true, "--enable-dns": true,
	"--force": true, "--hide-notes": true, "--insecure-skip-tls-verify": true, "--install": true, "-i": true,
	"--no-hooks": true, "--pass-credentials": true, "--plain-http": true, "--render-subchart-notes": true,
	"--reset-then-reuse-values": true, "--reset-values": true, "--reuse-values": true, "--skip-crds": true,
	"--skip-schema-validation": true, "--take-ownership": true, "--verify": true, "--wait": true,
	"--wait-for-jobs": true,
}

// helmDryRun reads --dry-run as helm's flag parser does: the last one wins, words
// after -- are arguments, and a --dry-run right after an option that takes a value
// is that option's value. Anything else is not a dry run.
func helmDryRun(inv shell.Invocation) bool {
	dry, prev := false, ""
	for _, a := range inv.Args {
		if !a.Literal || a.Value == "--" {
			return false
		}
		if name, value, hasValue := strings.Cut(a.Value, "="); name == "--dry-run" {
			if !hasValue && strings.HasPrefix(prev, "-") && !strings.Contains(prev, "=") && !helmUpgradeBools[prev] {
				return false
			}
			dry = !hasValue || value == "client" || value == "server" || value == "true"
		}
		prev = a.Value
	}
	return dry
}

var terraformDestructive = map[string]bool{
	"apply": true, "destroy": true, "force-unlock": true, "import": true, "refresh": true, "state": true,
	"taint": true, "untaint": true, "workspace": true,
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
	"build": true, "deps": true, "diff": true, "fetch": true, "help": true, "lint": true, "list": true,
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
