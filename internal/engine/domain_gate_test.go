package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/3leaps/spanwit/internal/config"
)

func cargoConfig(root string, enabled *[]string) *config.Config {
	cfg := &config.Config{
		Version: 1,
		Paths: []config.PathProfile{{
			Path:     root,
			MaxDepth: 12,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}
	if enabled != nil {
		cfg.Domains = &config.DomainsConfig{Enabled: enabled}
	}
	return cfg
}

func writeGateFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cargoTargetCandidate(t *testing.T, plan PrunePlan) PruneCandidate {
	t.Helper()
	for _, c := range plan.Candidates {
		if c.Signature == "development.rust.cargo-target" {
			return c
		}
	}
	t.Fatalf("cargo-target candidate not found in plan: %#v", plan.Candidates)
	return PruneCandidate{}
}

func newCargoFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	writeGateFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeGateFile(t, filepath.Join(tmp, "app", "target", "debug", "x.o"), 4096)
	return tmp
}

// Direct config prune must respect an explicit domains.enabled: []: cargo-target
// is in the development domain, so disabling it withholds the candidate rather
// than leaving it executable.
func TestBuildPrunePlan_DomainDisabledWithholds(t *testing.T) {
	empty := []string{}
	plan, err := BuildPrunePlan(context.Background(), cargoConfig(newCargoFixture(t), &empty))
	if err != nil {
		t.Fatalf("BuildPrunePlan: %v", err)
	}
	c := cargoTargetCandidate(t, plan)
	if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonDomainDisabled {
		t.Fatalf("cargo-target should be withheld/domain_disabled under enabled: []: %#v", c)
	}
}

func TestBuildPrunePlan_DomainEnabledStaysPrunable(t *testing.T) {
	dev := []string{"development"}
	plan, err := BuildPrunePlan(context.Background(), cargoConfig(newCargoFixture(t), &dev))
	if err != nil {
		t.Fatalf("BuildPrunePlan: %v", err)
	}
	if c := cargoTargetCandidate(t, plan); c.State != CandidateStatePrunable {
		t.Fatalf("cargo-target should stay prunable when development enabled: %#v", c)
	}
}

// Omitted domains block preserves current feel: no gating, cargo-target prunable.
func TestBuildPrunePlan_OmittedDomainsNoGate(t *testing.T) {
	plan, err := BuildPrunePlan(context.Background(), cargoConfig(newCargoFixture(t), nil))
	if err != nil {
		t.Fatalf("BuildPrunePlan: %v", err)
	}
	if c := cargoTargetCandidate(t, plan); c.State != CandidateStatePrunable {
		t.Fatalf("omitted domains must not gate cargo-target: %#v", c)
	}
}
