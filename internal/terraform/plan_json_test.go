package terraform

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/somaz94/whatdies/internal/verdict"
)

// planDoc reads terraform show -json, which the decoder is checked against.
type planDoc struct {
	FormatVersion     string            `json:"format_version"`
	Errored           bool              `json:"errored"`
	Complete          *bool             `json:"complete"`
	ResourceChanges   []resourceChange  `json:"resource_changes"`
	ActionInvocations []json.RawMessage `json:"action_invocations"`
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
		Provisioners []json.RawMessage `json:"provisioners"`
	} `json:"resources"`
	ModuleCalls map[string]struct {
		Module module `json:"module"`
	} `json:"module_calls"`
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
		Partial:         doc.Complete != nil && !*doc.Complete,
		ProviderActions: len(doc.ActionInvocations),
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

// judgeJSON lets the verdict rules be written as readable JSON plans.
func judgeJSON(raw []byte, stateful map[string]bool) verdict.Verdict {
	facts, problem := factsFromJSON(raw)
	if problem != "" {
		return verdict.AskBecause(problem)
	}
	return judgeFacts(facts, stateful)
}
