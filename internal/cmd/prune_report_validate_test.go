package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"

	"github.com/3leaps/spanwit/internal/space"
)

func TestPruneFromSpaceReport_CapacityOnlyFailsNotCarrier(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "schema-validation", "space-report-v2-capacity-only.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "capacity-only.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := buildPrunePlanFromSpaceReport(context.Background(), path); !errors.Is(err, space.ErrCapacityOnlyNotPruneCarrier) {
		t.Fatalf("loader err=%v", err)
	}

	identity := &appidentity.Identity{BinaryName: "spanwit"}
	for _, execute := range []bool{false, true} {
		cmd := newPruneCmd(identity)
		args := []string{"--from-space-report", path}
		if execute {
			args = append(args, "--execute")
		}
		cmd.SetArgs(args)
		err := cmd.Execute()
		if !errors.Is(err, space.ErrCapacityOnlyNotPruneCarrier) {
			t.Fatalf("execute=%v err=%v", execute, err)
		}
	}
}

func TestPruneFromSpaceReport_RejectsTruncatedJSON(t *testing.T) {
	tmp := t.TempDir()
	// Truncated JSON missing required report and exact-plan policy fields.
	raw := []byte(`{
  "prune_handoff": {
    "present": true,
    "exact_plan": {
      "candidates": [
        {
          "path": "/tmp/x/target",
          "signature": "development.rust.cargo-target"
        }
      ]
    }
  }
}`)
	path := filepath.Join(tmp, "bad.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := buildPrunePlanFromSpaceReport(context.Background(), path)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(err.Error(), "schema validation failed") {
		t.Fatalf("got %v", err)
	}

	// Execute entry also refuses
	identity := &appidentity.Identity{BinaryName: "spanwit"}
	logger, _ := logging.NewCLI("spanwit")
	Initialize(context.Background(), identity, logger)
	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--from-space-report", path, "--execute"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("execute must refuse truncated report")
	}
}

func TestPruneFromSpaceReport_ValidStillWorks(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	report, err := space.Analyze(context.Background(), space.Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "ok.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("candidates=%d", len(plan.Candidates))
	}
}
