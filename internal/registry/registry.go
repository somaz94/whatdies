// Package registry holds the data files that drive verdicts.
package registry

import (
	"bytes"
	_ "embed"
	"fmt"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed stateful.yaml
var statefulYAML []byte

//go:embed guards.yaml
var guardsYAML []byte

// Parsed lazily: the hook runs on every shell command an agent issues and most
// of them never reach a terraform plan.
var statefulTypes = sync.OnceValues(func() (map[string]bool, error) {
	return parseStateful(statefulYAML)
})

func parseStateful(data []byte) (map[string]bool, error) {
	var doc struct {
		Stateful []string `yaml:"stateful"`
	}
	if err := strict(data, &doc); err != nil {
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

var guardAttributes = sync.OnceValues(func() (map[string]map[string]bool, error) {
	return parseGuards(guardsYAML)
})

func parseGuards(data []byte) (map[string]map[string]bool, error) {
	var doc struct {
		Guards map[string]map[string]bool `yaml:"guards"`
	}
	if err := strict(data, &doc); err != nil {
		return nil, fmt.Errorf("parse guards.yaml: %w", err)
	}
	return doc.Guards, nil
}

// Guards maps a resource type to its guarding attributes and, for each, the value
// that turns the guard off.
func Guards() (map[string]map[string]bool, error) { return guardAttributes() }

// strict rejects unknown keys, so a misspelt section fails instead of loading empty.
func strict(data []byte, doc any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(doc)
}
