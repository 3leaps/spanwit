package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/config"
)

func TestBuildPrunePlan_UsesTargetsAndIgnores(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "old-rust", "target", "debug", "app.o"), 2048)
	writeFile(t, filepath.Join(tmp, "old-js", "node_modules", "dep", "index.js"), 1024)
	writeFile(t, filepath.Join(tmp, "ignored", "target", "debug", "skip.o"), 4096)

	old := time.Now().Add(-48 * time.Hour)
	mustChtimes(t, filepath.Join(tmp, "old-rust", "target"), old)
	mustChtimes(t, filepath.Join(tmp, "old-js", "node_modules"), old)
	mustChtimes(t, filepath.Join(tmp, "ignored", "target"), old)

	cfg := &config.Config{
		Version: 1,
		Defaults: config.Defaults{
			MinSize: "1K",
			MinAge:  "24h",
		},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 3,
			Targets: []config.Target{
				{Pattern: "**/target"},
				{Pattern: "**/node_modules"},
			},
			Ignores: []string{"ignored/**"},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}

	if len(plan.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %#v", plan.Candidates)
	}
	assertCandidate(t, plan.Candidates, filepath.Join(tmp, "old-rust", "target"))
	assertCandidate(t, plan.Candidates, filepath.Join(tmp, "old-js", "node_modules"))
	if plan.TotalSize != 3072 {
		t.Fatalf("expected total size 3072, got %d", plan.TotalSize)
	}
}

func TestBuildPrunePlan_RespectsThresholdsAndDepth(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "small", "target", "debug", "app.o"), 32)
	writeFile(t, filepath.Join(tmp, "deep", "nested", "project", "target", "debug", "app.o"), 2048)
	writeFile(t, filepath.Join(tmp, "recent", "target", "debug", "app.o"), 2048)

	old := time.Now().Add(-48 * time.Hour)
	mustChtimes(t, filepath.Join(tmp, "small", "target"), old)
	mustChtimes(t, filepath.Join(tmp, "deep", "nested", "project", "target"), old)

	cfg := &config.Config{
		Version: 1,
		Defaults: config.Defaults{
			MinSize: "1K",
			MinAge:  "24h",
		},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 2,
			Targets:  []config.Target{{Pattern: "**/target"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	// Depth-exceeded match is not discovered. Small and recent matches must be
	// visible as withheld (not silently dropped).
	if len(plan.Candidates) != 2 {
		t.Fatalf("expected 2 withheld candidates (small + recent), got %#v", plan.Candidates)
	}
	byPath := map[string]PruneCandidate{}
	for _, c := range plan.Candidates {
		byPath[c.Path] = c
		if c.State != candidateStateWithheld {
			t.Fatalf("expected withheld, got %#v", c)
		}
	}
	small := byPath[filepath.Join(tmp, "small", "target")]
	if small.WithheldReason != withheldReasonMinSize {
		t.Fatalf("small target: want reason %q, got %q", withheldReasonMinSize, small.WithheldReason)
	}
	recent := byPath[filepath.Join(tmp, "recent", "target")]
	if recent.WithheldReason != withheldReasonAge {
		t.Fatalf("recent target: want reason %q, got %q", withheldReasonAge, recent.WithheldReason)
	}
	if plan.EffectiveFilters.MinSize != "1K" || plan.EffectiveFilters.MinAge != "24h" {
		t.Fatalf("effective filters: got %#v", plan.EffectiveFilters)
	}
	for _, c := range plan.Candidates {
		if c.EffectiveMinSize != "1K" || c.EffectiveMinAge != "24h" {
			t.Fatalf("candidate filters: got min_size=%q min_age=%q", c.EffectiveMinSize, c.EffectiveMinAge)
		}
	}
}

func TestBuildPrunePlan_PathOverrideAgeIsResolvedAndVisible(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "rust-app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "rust-app", "target", "debug", "app.o"), 2048)
	mustChtimes(t, filepath.Join(tmp, "rust-app", "target"), time.Now())

	// Global defaults empty (no gate); path sets min_age: 90d (older-than floor) — must
	// withhold and report effective min_age=90d, not plan-level "none".
	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 4,
			MinAge:   "90d",
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected one candidate, got %#v", plan.Candidates)
	}
	c := plan.Candidates[0]
	if c.State != candidateStateWithheld || c.WithheldReason != withheldReasonAge {
		t.Fatalf("expected withheld/age, got state=%q reason=%q", c.State, c.WithheldReason)
	}
	if c.EffectiveMinAge != "90d" {
		t.Fatalf("candidate effective min_age: want 90d, got %q", c.EffectiveMinAge)
	}
	if c.EffectiveMinSize != filterDisplayNone {
		t.Fatalf("candidate effective min_size: want none, got %q", c.EffectiveMinSize)
	}
	if plan.EffectiveFilters.MinAge != "90d" {
		t.Fatalf("plan aggregate min_age: want 90d (path override), got %q", plan.EffectiveFilters.MinAge)
	}
	if plan.EffectiveFilters.MinSize != filterDisplayNone {
		t.Fatalf("plan aggregate min_size: want none, got %q", plan.EffectiveFilters.MinSize)
	}
}

func TestBuildPrunePlan_TargetOverrideMinSizeIsResolved(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "proj", "build", "out.bin"), 512)
	mustChtimes(t, filepath.Join(tmp, "proj", "build"), time.Now().Add(-48*time.Hour))

	// Global min_size none; target min_size 1K must withhold the small dir and
	// surface effective_min_size=1K on the candidate and plan aggregate.
	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 4,
			Targets:  []config.Target{{Pattern: "**/build", MinSize: "1K"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected one candidate, got %#v", plan.Candidates)
	}
	c := plan.Candidates[0]
	if c.State != candidateStateWithheld || c.WithheldReason != withheldReasonMinSize {
		t.Fatalf("expected withheld/min_size, got state=%q reason=%q", c.State, c.WithheldReason)
	}
	if c.EffectiveMinSize != "1K" {
		t.Fatalf("candidate effective min_size: want 1K, got %q", c.EffectiveMinSize)
	}
	if plan.EffectiveFilters.MinSize != "1K" {
		t.Fatalf("plan aggregate min_size: want 1K, got %q", plan.EffectiveFilters.MinSize)
	}
}

func TestBuildPrunePlan_MixedPathOverridesAggregateAsMixed(t *testing.T) {
	tmpA := t.TempDir()
	tmpB := t.TempDir()
	writeFile(t, filepath.Join(tmpA, "a", "build", "x"), 2048)
	writeFile(t, filepath.Join(tmpB, "b", "build", "x"), 2048)
	old := time.Now().Add(-48 * time.Hour)
	mustChtimes(t, filepath.Join(tmpA, "a", "build"), old)
	mustChtimes(t, filepath.Join(tmpB, "b", "build"), old)

	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{},
		Paths: []config.PathProfile{
			{Path: tmpA, MaxDepth: 4, MinAge: "24h", Targets: []config.Target{{Pattern: "**/build"}}},
			{Path: tmpB, MaxDepth: 4, MinAge: "7d", Targets: []config.Target{{Pattern: "**/build"}}},
		},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("expected two candidates, got %#v", plan.Candidates)
	}
	if plan.EffectiveFilters.MinAge != filterDisplayMixed {
		t.Fatalf("plan min_age should be mixed, got %q", plan.EffectiveFilters.MinAge)
	}
	ages := map[string]bool{}
	for _, c := range plan.Candidates {
		ages[c.EffectiveMinAge] = true
	}
	if !ages["24h"] || !ages["7d"] {
		t.Fatalf("expected per-candidate ages 24h and 7d, got %#v", ages)
	}
}

func TestBuildPrunePlan_AgeWithheldIsVisible(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "rust-app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "rust-app", "target", "debug", "app.o"), 2048)
	// Fresh mtime — fails min_age: 24h older-than floor.
	mustChtimes(t, filepath.Join(tmp, "rust-app", "target"), time.Now())

	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{MinSize: "1K", MinAge: "24h"},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 4,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected age-withheld candidate, got %#v", plan.Candidates)
	}
	c := plan.Candidates[0]
	if c.State != candidateStateWithheld || c.WithheldReason != withheldReasonAge {
		t.Fatalf("expected withheld/age, got state=%q reason=%q", c.State, c.WithheldReason)
	}
}

func TestBuildPrunePlan_MinSizeWithheldIsVisible(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "rust-app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "rust-app", "target", "debug", "app.o"), 32)
	mustChtimes(t, filepath.Join(tmp, "rust-app", "target"), time.Now().Add(-48*time.Hour))

	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{MinSize: "1K", MinAge: "24h"},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 4,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected min_size-withheld candidate, got %#v", plan.Candidates)
	}
	c := plan.Candidates[0]
	if c.State != candidateStateWithheld || c.WithheldReason != withheldReasonMinSize {
		t.Fatalf("expected withheld/min_size, got state=%q reason=%q", c.State, c.WithheldReason)
	}
}

func TestBuildPrunePlan_RustSignatureRequiresCargoEvidence(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "rust-app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "rust-app", "target", "debug", "app.o"), 2048)
	writeFile(t, filepath.Join(tmp, "etl", "target", "debug", "data.bin"), 2048)

	old := time.Now().Add(-48 * time.Hour)
	mustChtimes(t, filepath.Join(tmp, "rust-app", "target"), old)
	mustChtimes(t, filepath.Join(tmp, "etl", "target"), old)

	cfg := &config.Config{
		Version: 1,
		Defaults: config.Defaults{
			MinSize: "1K",
			MinAge:  "24h",
		},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 3,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected one Rust candidate, got %#v", plan.Candidates)
	}
	candidate := plan.Candidates[0]
	if candidate.Path != filepath.Join(tmp, "rust-app", "target") {
		t.Fatalf("expected Rust target only, got %#v", plan.Candidates)
	}
	if candidate.Signature != "development.rust.cargo-target" {
		t.Fatalf("expected signature on candidate, got %#v", candidate)
	}
	if candidate.Confidence != "high" {
		t.Fatalf("expected high confidence, got %q", candidate.Confidence)
	}
}

func TestBuildPrunePlan_SignatureNotSafeIsWithheld(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "etl", "node_modules", "dep", "index.js"), 2048)
	mustChtimes(t, filepath.Join(tmp, "etl", "node_modules"), time.Now().Add(-48*time.Hour))

	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{MinSize: "1K", MinAge: "24h"},
		Signatures: config.SignatureCatalog{
			"development": {"node": {"modules": config.Signature{
				CandidatePatterns: []string{"**/node_modules"},
				Confidence:        "medium",
				SafeToPrune:       false,
			}}},
		},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 3,
			Targets:  []config.Target{{Signature: "development.node.modules"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected the match to be reported, got %#v", plan.Candidates)
	}
	candidate := plan.Candidates[0]
	if candidate.State != candidateStateWithheld {
		t.Fatalf("expected withheld state, got %q", candidate.State)
	}
	if candidate.WithheldReason != withheldReasonSafeToPrune {
		t.Fatalf("expected reason %q, got %q", withheldReasonSafeToPrune, candidate.WithheldReason)
	}
}

func TestBuildPrunePlan_SignatureSafeIsPrunable(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "rust-app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "rust-app", "target", "debug", "app.o"), 2048)
	mustChtimes(t, filepath.Join(tmp, "rust-app", "target"), time.Now().Add(-48*time.Hour))

	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{MinSize: "1K", MinAge: "24h"},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 3,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected one candidate, got %#v", plan.Candidates)
	}
	if plan.Candidates[0].State != candidateStatePrunable {
		t.Fatalf("expected prunable state (built-in cargo-target is safe_to_prune), got %q", plan.Candidates[0].State)
	}
	if plan.Candidates[0].WithheldReason != "" {
		t.Fatalf("expected no withheld reason, got %q", plan.Candidates[0].WithheldReason)
	}
}

func TestBuildPrunePlan_PatternOnlyStaysPrunable(t *testing.T) {
	// A raw pattern target is direct user intent and remains executable; the
	// safe_to_prune gate applies only to signatures.
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "proj", "build", "out.bin"), 2048)
	mustChtimes(t, filepath.Join(tmp, "proj", "build"), time.Now().Add(-48*time.Hour))

	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{MinSize: "1K", MinAge: "24h"},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 3,
			Targets:  []config.Target{{Pattern: "**/build"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected one candidate, got %#v", plan.Candidates)
	}
	if plan.Candidates[0].State != candidateStatePrunable {
		t.Fatalf("expected pattern-only candidate to stay prunable, got %q", plan.Candidates[0].State)
	}
}

// TestBuildPrunePlan_NoAgeGateIncludesFreshTargets ensures omitting age fields
// (post loader-honesty fix) does not hide freshly mtime'd signature matches —
// the crisis footgun was a silent 90d older-than default.
func TestBuildPrunePlan_MaxAgeCeilingWithholdsOldTargets(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "rust-app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "rust-app", "target", "debug", "app.o"), 2048)
	// 48h old — fails max_age: 24h younger-than ceiling.
	mustChtimes(t, filepath.Join(tmp, "rust-app", "target"), time.Now().Add(-48*time.Hour))

	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{MinSize: "1K", MaxAge: "24h"},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 4,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected one age-withheld candidate, got %#v", plan.Candidates)
	}
	c := plan.Candidates[0]
	if c.State != candidateStateWithheld || c.WithheldReason != withheldReasonAge {
		t.Fatalf("expected withheld/age, got state=%q reason=%q", c.State, c.WithheldReason)
	}
	if c.EffectiveMaxAge != "24h" || c.EffectiveMinAge != filterDisplayNone {
		t.Fatalf("filters: got min_age=%q max_age=%q", c.EffectiveMinAge, c.EffectiveMaxAge)
	}
}

func TestBuildPrunePlan_AgeWindowFloorAndCeiling(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "old", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "old", "target", "debug", "a.o"), 2048)
	writeFile(t, filepath.Join(tmp, "mid", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "mid", "target", "debug", "a.o"), 2048)
	writeFile(t, filepath.Join(tmp, "new", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "new", "target", "debug", "a.o"), 2048)
	mustChtimes(t, filepath.Join(tmp, "old", "target"), time.Now().Add(-10*24*time.Hour))
	mustChtimes(t, filepath.Join(tmp, "mid", "target"), time.Now().Add(-3*24*time.Hour))
	mustChtimes(t, filepath.Join(tmp, "new", "target"), time.Now())

	// Window: at least 1d old and at most 7d old → only "mid" is prunable.
	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{MinSize: "1K", MinAge: "1d", MaxAge: "7d"},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 4,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 3 {
		t.Fatalf("expected 3 candidates, got %#v", plan.Candidates)
	}
	byPath := map[string]PruneCandidate{}
	for _, c := range plan.Candidates {
		byPath[c.Path] = c
	}
	oldC := byPath[filepath.Join(tmp, "old", "target")]
	midC := byPath[filepath.Join(tmp, "mid", "target")]
	newC := byPath[filepath.Join(tmp, "new", "target")]
	if oldC.State != candidateStateWithheld || oldC.WithheldReason != withheldReasonAge {
		t.Fatalf("old should be age-withheld (above max_age), got %#v", oldC)
	}
	if midC.State != candidateStatePrunable {
		t.Fatalf("mid should be prunable in window, got %#v", midC)
	}
	if newC.State != candidateStateWithheld || newC.WithheldReason != withheldReasonAge {
		t.Fatalf("new should be age-withheld (below min_age), got %#v", newC)
	}
}

// TestBuildPrunePlan_OverlappingRootsDedupes ensures nested path profiles that
// discover the same absolute path contribute once to candidates and TotalSize
// (first profile / first match wins) and emit a de-dupe warning.
func TestBuildPrunePlan_OverlappingRootsDedupes(t *testing.T) {
	tmp := t.TempDir()
	nested := filepath.Join(tmp, "org", "repo")
	target := filepath.Join(nested, "target")
	writeFile(t, filepath.Join(target, "debug", "app.o"), 4096)
	mustChtimes(t, target, time.Now().Add(-48*time.Hour))

	cfg := &config.Config{
		Version: 1,
		Defaults: config.Defaults{
			MinSize: "1K",
		},
		Paths: []config.PathProfile{
			{
				Path:     tmp,
				MaxDepth: 8,
				Targets:  []config.Target{{Pattern: "**/target"}},
			},
			// Nested root re-discovers the same target/ — must not double-count.
			{
				Path:     nested,
				MaxDepth: 4,
				Targets:  []config.Target{{Pattern: "**/target"}},
			},
		},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected 1 de-duped candidate, got %#v", plan.Candidates)
	}
	wantPath, err := filepath.Abs(target)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if plan.Candidates[0].Path != wantPath {
		t.Fatalf("path: want %s, got %s", wantPath, plan.Candidates[0].Path)
	}
	if plan.Candidates[0].State != candidateStatePrunable {
		t.Fatalf("expected prunable, got %#v", plan.Candidates[0])
	}
	if plan.TotalSize != plan.Candidates[0].Size {
		t.Fatalf("TotalSize %d must equal single candidate size %d", plan.TotalSize, plan.Candidates[0].Size)
	}
	// Double-count would be 2× measured size; require unique once.
	if plan.TotalSize != 4096 {
		t.Fatalf("expected total size 4096 (unique once), got %d", plan.TotalSize)
	}

	foundWarn := false
	for _, w := range plan.Warnings {
		if strings.Contains(w.Error(), "dropped 1 duplicate candidate path") {
			foundWarn = true
			break
		}
	}
	if !foundWarn {
		t.Fatalf("expected de-dupe warning, got %#v", plan.Warnings)
	}
}

// TestBuildPrunePlan_OverlappingRootsFirstMatchWins checks that when the same
// path is discovered under two profiles, the first profile's filter metadata
// is kept (first-match), not the second profile's overrides.
func TestBuildPrunePlan_OverlappingRootsFirstMatchWins(t *testing.T) {
	tmp := t.TempDir()
	nested := filepath.Join(tmp, "proj")
	target := filepath.Join(nested, "target")
	writeFile(t, filepath.Join(target, "debug", "app.o"), 2048)
	// Fresh mtime: first profile min_age:24h withholds; second has no age gate.
	mustChtimes(t, target, time.Now())

	cfg := &config.Config{
		Version: 1,
		Defaults: config.Defaults{
			MinSize: "1K",
		},
		Paths: []config.PathProfile{
			{
				Path:     tmp,
				MaxDepth: 4,
				MinAge:   "24h",
				Targets:  []config.Target{{Pattern: "**/target"}},
			},
			{
				Path:     nested,
				MaxDepth: 3,
				// No min_age — would be prunable if second match won.
				Targets: []config.Target{{Pattern: "**/target"}},
			},
		},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %#v", plan.Candidates)
	}
	c := plan.Candidates[0]
	if c.State != candidateStateWithheld || c.WithheldReason != withheldReasonAge {
		t.Fatalf("first-match should keep first profile age withhold, got %#v", c)
	}
	if c.EffectiveMinAge != "24h" {
		t.Fatalf("effective min_age: want 24h from first profile, got %q", c.EffectiveMinAge)
	}
}

func TestDedupeCandidateJobs(t *testing.T) {
	a := filepath.Join(t.TempDir(), "a", "target")
	abs, err := filepath.Abs(a)
	if err != nil {
		t.Fatal(err)
	}
	// Identical and unclean absolute forms of the same path — first-match wins.
	jobs := []candidateJob{
		{Path: abs, State: candidateStatePrunable, EffectiveMinSize: "50M"},
		{Path: abs, State: candidateStateWithheld, WithheldReason: withheldReasonAge, EffectiveMinSize: "1K"},
		{Path: abs + string(filepath.Separator) + "." + string(filepath.Separator), State: candidateStatePrunable},
	}
	unique, dropped := dedupeCandidateJobs(jobs)
	if dropped != 2 {
		t.Fatalf("dropped: want 2, got %d", dropped)
	}
	if len(unique) != 1 {
		t.Fatalf("unique: want 1, got %#v", unique)
	}
	if unique[0].State != candidateStatePrunable || unique[0].EffectiveMinSize != "50M" {
		t.Fatalf("first-wins metadata: got %#v", unique[0])
	}
	if unique[0].Path != abs {
		t.Fatalf("path key: want %s, got %s", abs, unique[0].Path)
	}
}

func TestBuildPrunePlan_NoAgeGateIncludesFreshTargets(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "rust-app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "rust-app", "target", "debug", "app.o"), 2048)
	// mtime=now: would be excluded by a ghost older-than floor if it were injected.
	mustChtimes(t, filepath.Join(tmp, "rust-app", "target"), time.Now())

	cfg := &config.Config{
		Version: 1,
		// Empty Defaults: no min_size, no min_age/max_age — omit-all-age means no age gate.
		Defaults: config.Defaults{},
		Paths: []config.PathProfile{{
			Path:     tmp,
			MaxDepth: 4,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildPrunePlan returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected fresh cargo target to be a candidate with no age gate, got %#v", plan.Candidates)
	}
	want := filepath.Join(tmp, "rust-app", "target")
	if plan.Candidates[0].Path != want {
		t.Fatalf("expected candidate %s, got %s", want, plan.Candidates[0].Path)
	}
	if plan.Candidates[0].State != candidateStatePrunable {
		t.Fatalf("expected prunable state, got %q", plan.Candidates[0].State)
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	data := make([]byte, size)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustChtimes(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func assertCandidate(t *testing.T, candidates []PruneCandidate, path string) {
	t.Helper()
	for _, candidate := range candidates {
		if candidate.Path == path {
			return
		}
	}
	t.Fatalf("expected candidate %s in %#v", path, candidates)
}
