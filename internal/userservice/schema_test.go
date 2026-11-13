//go:build darwin || linux

package userservice

import (
	"encoding/json"
	"testing"
	"time"

	schema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

func TestPreviewContractRefusesEffectClaims(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		plan, err := Render(Config{Platform: platform, Executable: "/opt/bin/example", Home: "/home/example", StateDir: "/home/example/state", Path: "/home/example", Interval: time.Minute, BinaryName: "example", Vendor: "vendor", UID: 1000})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := contract.ValidateJSON(schema.SpanwitUserServicePlanV0, raw); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"definition_written", "commands_executed"} {
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			value[key] = true
			bad, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if contract.ValidateJSON(schema.SpanwitUserServicePlanV0, bad) == nil {
				t.Fatalf("accepted %s claim", key)
			}
		}
	}
}
