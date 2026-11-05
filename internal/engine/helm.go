package engine

import (
	"fmt"
	"strings"

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
