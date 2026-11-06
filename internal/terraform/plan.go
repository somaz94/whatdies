package terraform

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/somaz94/whatdies/internal/verdict"
)

// planDoc decodes only what a verdict needs: a plan holds sensitive values in
// clear text, and fields never decoded cannot end up in a message.
type planDoc struct {
	FormatVersion     string            `json:"format_version"`
	Errored           bool              `json:"errored"`
	Applyable         *bool             `json:"applyable"`
	Complete          *bool             `json:"complete"`
	ResourceChanges   []resourceChange  `json:"resource_changes"`
	ActionInvocations []json.RawMessage `json:"action_invocations"`
	DeferredChanges   []json.RawMessage `json:"deferred_changes"`
	DeferredActions   []json.RawMessage `json:"deferred_action_invocations"`
	Configuration     struct {
		RootModule module `json:"root_module"`
	} `json:"configuration"`
}

type resourceChange struct {
	Address string `json:"address"`
	Mode    string `json:"mode"`
	Type    string `json:"type"`
	Deposed string `json:"deposed"`
	Change  struct {
		Actions []string `json:"actions"`
	} `json:"change"`
}

type module struct {
	Resources []struct {
		Address      string            `json:"address"`
		Provisioners []json.RawMessage `json:"provisioners"`
	} `json:"resources"`
	ModuleCalls map[string]struct {
		Module module `json:"module"`
	} `json:"module_calls"`
}

const maxListed = 15

type effect int

const (
	harmless effect = iota
	unclear
	destroys
	replaces
)

// classify reads change.actions. A value this code does not know is unclear
// rather than harmless, because new actions have appeared before: the JSON
// documentation does not list forget at all.
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
	Errored      bool
	NotApplyable bool
	Partial      bool
	Changes      []plannedChange
	// ProviderActions and Deferred count what the change list does not describe.
	ProviderActions int
	Deferred        int
	Provisioners    bool
}

type plannedChange struct {
	Address string
	Type    string
	Data    bool
	Deposed bool
	Actions []string
}

// changesAnything is true when applying it does something, which is when a
// provisioner in the configuration can run.
func (c plannedChange) changesAnything() bool {
	return classify(c.Actions) != harmless || slices.Contains(c.Actions, "create") || slices.Contains(c.Actions, "update")
}

// JudgePlan decides from the output of terraform show -json.
func JudgePlan(raw []byte, stateful map[string]bool) verdict.Verdict {
	facts, problem := factsFromJSON(raw)
	if problem != "" {
		return verdict.AskBecause(problem)
	}
	return judgeFacts(facts, stateful)
}

func factsFromJSON(raw []byte) (planFacts, string) {
	var doc planDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return planFacts{}, fmt.Sprintf("could not decode the plan: %v", err)
	}
	if major, _, _ := strings.Cut(doc.FormatVersion, "."); major != "1" {
		return planFacts{}, fmt.Sprintf("plan format %q is not one whatdies knows", doc.FormatVersion)
	}
	facts := planFacts{
		Errored:         doc.Errored,
		NotApplyable:    doc.Applyable != nil && !*doc.Applyable,
		Partial:         doc.Complete != nil && !*doc.Complete,
		ProviderActions: len(doc.ActionInvocations),
		Deferred:        len(doc.DeferredChanges) + len(doc.DeferredActions),
		Provisioners:    hasProvisioner(doc.Configuration.RootModule),
	}
	for _, rc := range doc.ResourceChanges {
		facts.Changes = append(facts.Changes, plannedChange{
			Address: rc.Address,
			Type:    rc.Type,
			Data:    rc.Mode == "data",
			Deposed: rc.Deposed != "",
			Actions: rc.Change.Actions,
		})
	}
	return facts, ""
}

func judgeFacts(f planFacts, stateful map[string]bool) verdict.Verdict {
	if f.Errored {
		return verdict.AskBecause("the plan is marked as errored, so its change list may be partial")
	}
	if f.NotApplyable {
		return verdict.Verdict{}
	}

	var fatal, other, vague []string
	changing := false
	for _, c := range f.Changes {
		changing = changing || c.changesAnything()
		if c.Data {
			continue
		}
		verb := "destroys"
		switch classify(c.Actions) {
		case harmless:
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
		reasons = append(reasons, "the plan is partial: more changes follow after this one is applied")
	}
	if f.ProviderActions > 0 {
		reasons = append(reasons, fmt.Sprintf("the plan runs %d provider action(s), whose effects it does not show", f.ProviderActions))
	}
	if f.Deferred > 0 {
		reasons = append(reasons, fmt.Sprintf("the plan defers %d change(s), which it does not describe", f.Deferred))
	}
	if changing && f.Provisioners {
		reasons = append(reasons, "the configuration has provisioners, which run commands the plan does not show")
	}
	reasons = append(reasons, vague...)

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

func hasProvisioner(m module) bool {
	for _, r := range m.Resources {
		if len(r.Provisioners) > 0 {
			return true
		}
	}
	for _, call := range m.ModuleCalls {
		if hasProvisioner(call.Module) {
			return true
		}
	}
	return false
}
