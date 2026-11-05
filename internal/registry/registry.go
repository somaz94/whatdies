// Package registry holds the data files that drive verdicts.
package registry

import (
	_ "embed"
	"fmt"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed stateful.yaml
var statefulYAML []byte

// Parsed lazily: the hook runs on every shell command an agent issues and most
// of them never reach a terraform plan.
var statefulTypes = sync.OnceValues(func() (map[string]bool, error) {
	return parseStateful(statefulYAML)
})

func parseStateful(data []byte) (map[string]bool, error) {
	var doc struct {
		Stateful []string `yaml:"stateful"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse stateful.yaml: %w", err)
	}
	types := make(map[string]bool, len(doc.Stateful))
	for _, t := range doc.Stateful {
		types[t] = true
	}
	return types, nil
}

// Stateful returns the set of Terraform resource types that hold data.
func Stateful() (map[string]bool, error) { return statefulTypes() }
