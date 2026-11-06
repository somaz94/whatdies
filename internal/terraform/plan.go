package terraform

import (
	"fmt"
	"slices"

	"github.com/somaz94/whatdies/internal/verdict"
)

const maxListed = 15

type effect int

const (
	harmless effect = iota
	unclear
	destroys
	replaces
)

// classify reads a change's actions. One it does not know is unclear rather than
// harmless, because new actions have appeared before.
func classify(actions []string) effect {
	if len(actions) == 0 {
		return unclear
	}
	var del, create, unknown, forget bool
	for _, a := range actions {
		switch a {
		case "delete":
			del = true
		case "create":
			create = true
		case "no-op", "read", "update":
		case "forget":
			forget = true
		default:
			unknown = true
		}
	}
	switch {
	case del && create:
		return replaces
	case del:
		return destroys
	case forget || unknown:
		return unclear
	}
	return harmless
}

// planFacts is what a verdict needs from a plan.
type planFacts struct {
	Errored bool
	Partial bool
	Changes []plannedChange
	// ProviderActions run code whose effects the change list does not describe.
	ProviderActions int
	Provisioners    bool
}

type plannedChange struct {
	Address string
	Type    string
	Data    bool
	Deposed bool
	Actions []string
	// Weakens describes guard attributes an update turns off, such as force_destroy.
	Weakens []string
}

// changesAnything is true when applying it does something, which is when a
// provisioner in the configuration can run.
func (c plannedChange) changesAnything() bool {
	return classify(c.Actions) != harmless || slices.Contains(c.Actions, "create") || slices.Contains(c.Actions, "update")
}

func judgeFacts(f planFacts, stateful map[string]bool) verdict.Verdict {
	if f.Errored {
		return verdict.AskBecause("the plan is marked as errored, so its change list may be partial")
	}
	var fatal, other, vague, weakened []string
	changing := false
	for _, c := range f.Changes {
		changing = changing || c.changesAnything()
		if c.Data {
			continue
		}
		verb := "destroys"
		switch classify(c.Actions) {
		case harmless:
			for _, w := range c.Weakens {
				weakened = append(weakened, fmt.Sprintf("%s %s, so a later destroy can lose data it would have kept", c.Address, w))
			}
			continue
		case unclear:
			vague = append(vague, fmt.Sprintf("%s has actions %v, which whatdies cannot rule out as destructive", c.Address, c.Actions))
			continue
		case replaces:
			verb = "replaces"
		}
		line := fmt.Sprintf("%s %s", verb, c.Address)
		if c.Deposed {
			line += " (deposed object)"
		}
		if stateful[c.Type] {
			fatal = append(fatal, line+" ("+c.Type+" holds data)")
		} else {
			other = append(other, line)
		}
	}

	var reasons []string
	if f.Partial {
		reasons = append(reasons, "the plan is not marked complete, so more changes may follow after this one is applied")
	}
	if f.ProviderActions > 0 {
		reasons = append(reasons, fmt.Sprintf("the plan runs %d provider action(s), whose effects it does not show", f.ProviderActions))
	}
	if changing && f.Provisioners {
		reasons = append(reasons, "the configuration mentions provisioners, which run commands the plan does not show")
	}
	reasons = append(reasons, vague...)
	reasons = append(reasons, limit(weakened)...)

	if len(fatal) > 0 {
		all := append(append(limit(fatal), limit(other)...), reasons...)
		return verdict.DenyBecause(verdict.StopAdvice, all...)
	}
	all := append(limit(other), reasons...)
	if len(all) == 0 {
		return verdict.Verdict{}
	}
	return verdict.AskBecause(all...)
}

func limit(lines []string) []string {
	if len(lines) <= maxListed {
		return lines
	}
	return append(append([]string{}, lines[:maxListed]...), fmt.Sprintf("and %d more", len(lines)-maxListed))
}
