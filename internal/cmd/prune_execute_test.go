package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"

	"github.com/3leaps/spanwit/internal/config"
)

// A prunable candidate whose authorization provenance was lost (e.g. tampered
// or hand-built plan) must be skipped with a visible warning and make execute
// return non-zero — never silently deleted, and never a silent full-success.
func TestExecutePrunePlan_ProvenanceLossSkipsAndFails(t *testing.T) {
	identity := &appidentity.Identity{BinaryName: "spanwit"}
	logger, _ := logging.NewCLI("spanwit")
	Initialize(context.Background(), identity, logger)

	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "app", "Cargo.toml.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "app", "Cargo.toml"), []byte("[package]\nname=\"a\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "app", "target")
	if err := os.MkdirAll(filepath.Join(target, "debug"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "debug", "x.o"), make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Version: 1,
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 12,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}
	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Strip the non-inferred provenance to simulate loss/tamper.
	found := false
	for i := range plan.Candidates {
		if plan.Candidates[i].State == candidateStatePrunable {
			plan.Candidates[i].ProvenanceKind = ""
			plan.Candidates[i].AuthorizationBoundary = ""
			found = true
		}
	}
	if !found {
		t.Fatal("expected a prunable candidate in the fixture")
	}

	execErr := executePrunePlan(context.Background(), &plan)
	if execErr == nil {
		t.Fatal("execute must return non-zero when a candidate fails the guard")
	}
	if _, statErr := os.Lstat(target); statErr != nil {
		t.Fatalf("target must NOT be deleted when provenance is lost: %v", statErr)
	}
	sawWarn := false
	for _, w := range plan.Warnings {
		if strings.Contains(w.Error(), "authorization provenance") {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Fatalf("expected a visible provenance warning, got %v", plan.Warnings)
	}
}
