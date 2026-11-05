// Package engine turns a command line into one verdict.
package engine

import (
	"context"
	"fmt"
	"maps"

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
	return terraform.Evaluate(ctx, inv, earlier, terraform.Deps{Runner: d.Runner, Stateful: d.stateful})
}

func (d Deps) stateful() (map[string]bool, error) {
	types, err := registry.Stateful()
	if err != nil || len(d.ExtraStateful) == 0 {
		return types, err
	}
	types = maps.Clone(types)
	for _, t := range d.ExtraStateful {
		types[t] = true
	}
	return types, nil
}
