package terraform

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
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
		Actions      []string                   `json:"actions"`
		Before       map[string]json.RawMessage `json:"before"`
		After        map[string]json.RawMessage `json:"after"`
		AfterUnknown map[string]json.RawMessage `json:"after_unknown"`
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

func factsFromJSON(raw []byte, guards map[string]map[string]bool) (planFacts, string) {
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
		c := plannedChange{
			Address: rc.Address,
			Type:    rc.Type,
			Data:    rc.Mode == "data",
			Deposed: rc.Deposed != "",
			Actions: rc.Change.Actions,
		}
		if attrs := guards[rc.Type]; len(attrs) > 0 && !c.Data && slices.Equal(c.Actions, []string{"update"}) {
			before, after := packJSON(rc.Change.Before, nil), packJSON(rc.Change.After, rc.Change.AfterUnknown)
			weakens, err := weakenedGuards([][]byte{before, after}, attrs)
			if err != nil {
				return planFacts{}, err.Error()
			}
			c.Weakens = weakens
		}
		facts.Changes = append(facts.Changes, c)
	}
	return facts, ""
}

// packJSON re-encodes a JSON object as the DynamicValue a saved plan holds, keeping
// only whether each top-level value is a boolean, null, unknown or something else.
func packJSON(values, unknown map[string]json.RawMessage) []byte {
	var kv []byte
	n := 0
	for _, k := range slices.Sorted(maps.Keys(values)) {
		var v []byte
		switch string(values[k]) {
		case "true":
			v = []byte{0xc3}
		case "false":
			v = []byte{0xc2}
		case "null":
			v = []byte{0xc0}
		default:
			v = []byte{0xa0}
		}
		kv = append(append(kv, mpKey(k)...), v...)
		n++
	}
	for _, k := range slices.Sorted(maps.Keys(unknown)) {
		if _, known := values[k]; !known && string(unknown[k]) == "true" {
			kv = append(append(kv, mpKey(k)...), 0xd4, 0, 0)
			n++
		}
	}
	return pbBytes(dynamicMsgpack, append([]byte{0xde, byte(n >> 8), byte(n)}, kv...))
}

func mpKey(k string) []byte { return append([]byte{0xd9, byte(len(k))}, k...) }

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
	facts, problem := factsFromJSON(raw, nil)
	if problem != "" {
		return verdict.AskBecause(problem)
	}
	return judgeFacts(facts, stateful)
}
