// Package vocab exposes spanwit's closed vocabularies from the embedded
// registry (config/schema/spanwit-vocabulary.v1.schema.json), the single source of truth.
//
// Report schemas stay self-contained and inline these enums; the registry
// tests require every inline enum site to map to a vocabulary here (equal or
// a declared subset), and Go constant sets to equal their vocabulary. Adding
// a value means editing the registry, the schemas and the constants together.
package vocab

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
)

type registryDoc struct {
	Defs map[string]struct {
		Description string   `json:"description"`
		Enum        []string `json:"enum"`
	} `json:"$defs"`
}

var (
	loadOnce sync.Once
	loaded   map[string][]string
	loadErr  error
)

func load() (map[string][]string, error) {
	loadOnce.Do(func() {
		var doc registryDoc
		if err := json.Unmarshal(spanwitschema.Vocabulary, &doc); err != nil {
			loadErr = fmt.Errorf("parse vocabulary registry: %w", err)
			return
		}
		loaded = make(map[string][]string, len(doc.Defs))
		for name, def := range doc.Defs {
			loaded[name] = append([]string(nil), def.Enum...)
		}
	})
	return loaded, loadErr
}

// Names returns the registered vocabulary names, sorted.
func Names() ([]string, error) {
	reg, err := load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// Values returns the values of a vocabulary in registry order.
func Values(name string) ([]string, error) {
	reg, err := load()
	if err != nil {
		return nil, err
	}
	vals, ok := reg[name]
	if !ok {
		return nil, fmt.Errorf("unknown vocabulary %q", name)
	}
	return append([]string(nil), vals...), nil
}
