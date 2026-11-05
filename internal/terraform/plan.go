package terraform

import (
	"encoding/json"
	"fmt"
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

// JudgePlan decides from the output of terraform show -json.
func JudgePlan(raw []byte, stateful map[string]bool) verdict.Verdict {
	var doc planDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return verdict.AskBecause(fmt.Sprintf("could not decode the plan: %v", err))
	}
	if major, _, _ := strings.Cut(doc.FormatVersion, "."); major != "1" {
		return verdict.AskBecause(fmt.Sprintf("plan format %q is not one whatdies knows", doc.FormatVersion))
	}
	if doc.Errored {
		return verdict.AskBecause("the plan is marked as errored, so its change list may be partial")
	}
	if doc.Applyable != nil && !*doc.Applyable {
		return verdict.Verdict{}
	}

	var fatal, other, vague []string
	changing := false
	for _, rc := range doc.ResourceChanges {
		eff := classify(rc.Change.Actions)
		if eff != harmless {
			changing = true
		} else {
			for _, a := range rc.Change.Actions {
				if a == "create" || a == "update" {
					changing = true
				}
			}
		}
		if rc.Mode == "data" {
			continue
		}
		verb := "destroys"
		switch eff {
		case harmless:
			continue
		case unclear:
			vague = append(vague, fmt.Sprintf("%s has actions %v, which whatdies cannot rule out as destructive", rc.Address, rc.Change.Actions))
			continue
		case replaces:
			verb = "replaces"
		}
		line := fmt.Sprintf("%s %s", verb, rc.Address)
		if rc.Deposed != "" {
			line += " (deposed object)"
		}
		if stateful[rc.Type] {
			fatal = append(fatal, line+" ("+rc.Type+" holds data)")
		} else {
			other = append(other, line)
		}
	}

	var reasons []string
	if doc.Complete != nil && !*doc.Complete {
		reasons = append(reasons, "the plan is partial: more changes follow after this one is applied")
	}
	if len(doc.ActionInvocations) > 0 {
		reasons = append(reasons, fmt.Sprintf("the plan runs %d provider action(s), whose effects it does not show", len(doc.ActionInvocations)))
	}
	if n := len(doc.DeferredChanges) + len(doc.DeferredActions); n > 0 {
		reasons = append(reasons, fmt.Sprintf("the plan defers %d change(s), which it does not describe", n))
	}
	if changing && hasProvisioner(doc.Configuration.RootModule) {
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
