package space

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/engine"
)

// analyzeWithConfig runs a verified walk of root with an overlay config, the
// way `space -c` does.
func analyzeWithConfig(t *testing.T, root string, cfg *config.Config, minSize string) Report {
	t.Helper()
	report, err := Analyze(context.Background(), Options{
		Path: root, MinSize: minSize, Top: 10, IncludeHomeCaches: false,
		Now: time.Now(), Config: cfg,
	})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return report
}

func candidateAt(t *testing.T, r Report, path string) Entry {
	t.Helper()
	for _, c := range r.Verified.Candidates {
		if c.Path == path {
			return c
		}
	}
	t.Fatalf("no verified candidate at %s: %v", path, verifiedPaths(r))
	return Entry{}
}

// A config age floor reaches the verified walk: a fresh cargo target is
// withheld for age, reports the floor, and never enters the exact plan.
func TestConfigMinAgeWithholdsFreshTarget(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "crate", "Cargo.toml"), 64)
	target := filepath.Join(root, "crate", "target")
	writeFile(t, filepath.Join(target, "debug", "x.o"), 4096)

	r := analyzeWithConfig(t, root, &config.Config{Version: 1, Defaults: config.Defaults{MinAge: "7d"}}, "")
	c := candidateAt(t, r, target)
	if c.State != StateWithheld || c.WithheldReason != "age" {
		t.Fatalf("fresh target under config min_age must be withheld for age: %+v", c)
	}
	if c.EffectiveMinAge != "7d" {
		t.Fatalf("effective_min_age = %q, want the config floor 7d", c.EffectiveMinAge)
	}
	if r.PruneHandoff != nil && r.PruneHandoff.ExactPlan != nil {
		for _, ec := range r.PruneHandoff.ExactPlan.Candidates {
			if ec.Path == target {
				t.Fatalf("a target withheld by the config floor must not enter the exact plan: %+v", ec)
			}
		}
	}
}

// An old target is prunable under the same floor, and the handoff carries the
// floor so prune revalidates against it rather than against "none".
func TestConfigMinAgeIsCarriedIntoTheHandoff(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "crate", "Cargo.toml"), 64)
	target := filepath.Join(root, "crate", "target")
	writeFile(t, filepath.Join(target, "debug", "x.o"), 4096)
	ageAll(t, target, 30*24*time.Hour)

	r := analyzeWithConfig(t, root, &config.Config{Version: 1, Defaults: config.Defaults{MinAge: "7d", MaxAge: "400d"}}, "")
	c := candidateAt(t, r, target)
	if c.State != StatePrunable {
		t.Fatalf("a 30-day-old target is prunable under min_age 7d: %+v", c)
	}
	if c.EffectiveMinAge != "7d" || c.EffectiveMaxAge != "400d" {
		t.Fatalf("effective ages = %q/%q, want 7d/400d", c.EffectiveMinAge, c.EffectiveMaxAge)
	}
	if r.PruneHandoff == nil || r.PruneHandoff.ExactPlan == nil || len(r.PruneHandoff.ExactPlan.Candidates) != 1 {
		t.Fatalf("expected one exact-plan candidate: %+v", r.PruneHandoff)
	}
	ec := r.PruneHandoff.ExactPlan.Candidates[0]
	if ec.EffectiveMinAge != "7d" || ec.EffectiveMaxAge != "400d" {
		t.Fatalf("exact plan carries %q/%q, want 7d/400d", ec.EffectiveMinAge, ec.EffectiveMaxAge)
	}
}

// Prune revalidation of the handoff honours the carried floor: once the
// target is fresh again, the plan does not delete it.
func TestConfigMinAgeHoldsThroughPruneRevalidation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "crate", "Cargo.toml"), 64)
	target := filepath.Join(root, "crate", "target")
	writeFile(t, filepath.Join(target, "debug", "x.o"), 4096)
	ageAll(t, target, 30*24*time.Hour)

	r := analyzeWithConfig(t, root, &config.Config{Version: 1, Defaults: config.Defaults{MinAge: "7d"}}, "")
	// Activity after the report: the target is fresh at execute time.
	writeFile(t, filepath.Join(target, "debug", "y.o"), 4096)

	plan, err := engine.PlanFromExact(context.Background(), ExactPlanToEngine(r.PruneHandoff.ExactPlan))
	if err != nil {
		t.Fatalf("plan from exact: %v", err)
	}
	for _, c := range plan.Candidates {
		if c.Path == target && c.State == engine.CandidateStatePrunable {
			t.Fatalf("a target fresh at revalidation must not be prunable under the carried 7d floor: %+v", c)
		}
	}
}

// The config size floor applies when --min-size is absent, and the report
// states it; --min-size still overrides it.
func TestConfigMinSizeAppliesUnlessFlagGiven(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "crate", "Cargo.toml"), 64)
	target := filepath.Join(root, "crate", "target")
	writeFile(t, filepath.Join(target, "debug", "x.o"), 4096)
	ageAll(t, target, 30*24*time.Hour)
	cfg := &config.Config{Version: 1, Defaults: config.Defaults{MinSize: "1M"}}

	r := analyzeWithConfig(t, root, cfg, "")
	if r.MinSize != "1M" {
		t.Fatalf("report min_size = %q, want the config floor 1M", r.MinSize)
	}
	c := candidateAt(t, r, target)
	if c.State == StatePrunable || c.EffectiveMinSize != "1M" {
		t.Fatalf("a 4K target is below the 1M config floor: %+v", c)
	}

	r = analyzeWithConfig(t, root, cfg, "1K")
	if r.MinSize != "1K" {
		t.Fatalf("report min_size = %q, want the flag value 1K", r.MinSize)
	}
	if c := candidateAt(t, r, target); c.State != StatePrunable || c.EffectiveMinSize != "1K" {
		t.Fatalf("--min-size 1K overrides the config floor: %+v", c)
	}
}

// A gate set on a config path profile names another root: space refuses the
// config rather than drop the gate or copy it onto its own roots.
func TestConfigPathProfileGateIsRefused(t *testing.T) {
	root := t.TempDir()
	for name, p := range map[string]config.PathProfile{
		"min_age":  {Path: root, MinAge: "7d"},
		"min_size": {Path: root, MinSize: "1M"},
		"max_age":  {Path: root, MaxAge: "30d"},
	} {
		cfg := &config.Config{Version: 1, Paths: []config.PathProfile{p}}
		_, err := Analyze(context.Background(), Options{
			Path: root, Top: 10, IncludeHomeCaches: false, Now: time.Now(), Config: cfg,
		})
		if err == nil || !strings.Contains(err.Error(), "min_size/min_age/max_age") {
			t.Errorf("%s on a path profile must be refused: %v", name, err)
		}
	}
}

// A gate on a target still applies: targets are copied whole.
func TestConfigTargetGateApplies(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "crate", "Cargo.toml"), 64)
	target := filepath.Join(root, "crate", "target")
	writeFile(t, filepath.Join(target, "debug", "x.o"), 4096)
	cfg := &config.Config{Version: 1, Paths: []config.PathProfile{{
		Path:    root,
		Targets: []config.Target{{Signature: "development.rust.cargo-target", MinAge: "7d"}},
	}}}
	c := candidateAt(t, analyzeWithConfig(t, root, cfg, ""), target)
	if c.State != StateWithheld || c.EffectiveMinAge != "7d" {
		t.Fatalf("a target gate from --config applies to a fresh target: %+v", c)
	}
}
