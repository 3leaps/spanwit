package config

import (
	"os"
	"path/filepath"
	"testing"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

func TestExampleConfigsMatchSchema(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "docs", "examples", "configs", "*.yaml"))
	if err != nil {
		t.Fatalf("glob example configs: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("expected example configs")
	}
	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read example config: %v", err)
			}
			if err := contract.ValidateYAML(spanwitschema.SpanwitConfigV1, data); err != nil {
				t.Fatalf("example config does not match schema: %v", err)
			}
		})
	}
}
