package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/config"
)

func TestBuildPrunePlan_IncrementalScope(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "app")
	target := filepath.Join(proj, "target")
	inc := filepath.Join(target, "debug", "incremental")
	if err := os.MkdirAll(inc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "Cargo.toml"), []byte("[package]\nname=\"app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Put mass under incremental and under deps so whole-target would be larger.
	if err := os.WriteFile(filepath.Join(inc, "crate-hash"), []byte("xxxxxxxx"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := filepath.Join(target, "debug", "deps")
	if err := os.MkdirAll(deps, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deps, "lib.rlib"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Version: 1,
		Paths: []config.PathProfile{{
			Path:     root,
			MaxDepth: 12,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}

	whole, err := BuildPrunePlan(context.Background(), cfg, WithReclaimScope(ReclaimScopeWhole))
	if err != nil {
		t.Fatal(err)
	}
	if len(whole.Candidates) != 1 || whole.Candidates[0].Path != target {
		t.Fatalf("whole candidates: %+v", whole.Candidates)
	}

	part, err := BuildPrunePlan(context.Background(), cfg, WithReclaimScope(ReclaimScopeIncremental), WithSortMode(SortIdle))
	if err != nil {
		t.Fatal(err)
	}
	if len(part.Candidates) != 1 {
		t.Fatalf("incremental candidates: %+v", part.Candidates)
	}
	c := part.Candidates[0]
	if c.Path != inc {
		t.Fatalf("path=%s want %s", c.Path, inc)
	}
	if c.ReclaimScope != ReclaimScopeIncremental || c.ParentPath != target {
		t.Fatalf("scope/parent: %+v", c)
	}
	if c.Size >= whole.Candidates[0].Size {
		t.Fatalf("incremental size %d should be < whole %d", c.Size, whole.Candidates[0].Size)
	}
}

func TestPlanFromExact_IncrementalRevalidatesParent(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "app")
	target := filepath.Join(proj, "target")
	inc := filepath.Join(target, "release", "incremental")
	if err := os.MkdirAll(inc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "Cargo.toml"), []byte("[package]\nname=\"app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inc, "x"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanFromExact(context.Background(), ExactPlanInput{
		AnalysisRoot: root,
		Entries: []ExactEntry{{
			Path:             inc,
			Signature:        "development.rust.cargo-target",
			EffectiveMinSize: "none",
			EffectiveMinAge:  "none",
			EffectiveMaxAge:  "none",
			ReclaimScope:     ReclaimScopeIncremental,
			ParentPath:       target,
		}},
	}, WithSortMode(SortIdle))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].State != CandidateStatePrunable {
		t.Fatalf("plan: %+v", plan)
	}
}

func TestBuildPrunePlan_IncrementalSkipsNonCargo(t *testing.T) {
	root := t.TempDir()
	// Cargo project with incremental
	proj := filepath.Join(root, "app")
	target := filepath.Join(proj, "target")
	inc := filepath.Join(target, "debug", "incremental")
	if err := os.MkdirAll(inc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "Cargo.toml"), []byte("[package]\nname=\"app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inc, "x"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pattern-only custom cache (must not become whole prunable under incremental)
	custom := filepath.Join(root, "custom-cache")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(custom, "blob"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Version: 1,
		Paths: []config.PathProfile{{
			Path:     root,
			MaxDepth: 12,
			Targets: []config.Target{
				{Signature: "development.rust.cargo-target"},
				{Pattern: "**/custom-cache"},
			},
		}},
	}
	plan, err := BuildPrunePlan(context.Background(), cfg, WithReclaimScope(ReclaimScopeIncremental))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Candidates {
		if c.Path == custom || c.ReclaimScope == ReclaimScopeWhole {
			t.Fatalf("non-cargo whole candidate admitted under incremental: %+v", c)
		}
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Path != inc {
		t.Fatalf("candidates=%+v", plan.Candidates)
	}
	foundSkipWarn := false
	for _, w := range plan.Warnings {
		if w != nil && strings.Contains(w.Error(), "reclaim_scope=incremental") &&
			strings.Contains(w.Error(), "skipped non-cargo") {
			foundSkipWarn = true
		}
	}
	if !foundSkipWarn {
		t.Fatalf("expected skip warning, got %v", plan.Warnings)
	}
}

func TestBuildPrunePlan_IncrementalAgeUsesLeafNotParent(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "app")
	target := filepath.Join(proj, "target")
	inc := filepath.Join(target, "debug", "incremental")
	if err := os.MkdirAll(inc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "Cargo.toml"), []byte("[package]\nname=\"app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	leafFile := filepath.Join(inc, "crate")
	if err := os.WriteFile(leafFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Fresh parent target/ mtime, old leaf contents.
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(leafFile, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(inc, old, old); err != nil {
		t.Fatal(err)
	}
	// Bump only the parent target directory mtime to "now".
	now := time.Now()
	if err := os.Chtimes(target, now, now); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Version: 1,
		Defaults: config.Defaults{
			MinAge: "30d",
		},
		Paths: []config.PathProfile{{
			Path:     root,
			MaxDepth: 12,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}
	plan, err := BuildPrunePlan(context.Background(), cfg, WithReclaimScope(ReclaimScopeIncremental))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("candidates: %+v", plan.Candidates)
	}
	c := plan.Candidates[0]
	if c.State != CandidateStatePrunable {
		t.Fatalf("expected prunable from old leaf despite fresh parent, got %s/%s", c.State, c.WithheldReason)
	}

	// Exact replay must agree: age on leaf only.
	exact, err := PlanFromExact(context.Background(), ExactPlanInput{
		AnalysisRoot: root,
		Entries: []ExactEntry{{
			Path:             inc,
			Signature:        "development.rust.cargo-target",
			EffectiveMinSize: "none",
			EffectiveMinAge:  "30d",
			EffectiveMaxAge:  "none",
			ReclaimScope:     ReclaimScopeIncremental,
			ParentPath:       target,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(exact.Candidates) != 1 || exact.Candidates[0].State != CandidateStatePrunable {
		t.Fatalf("exact replay: %+v", exact.Candidates)
	}

	// Parent-old / leaf-fresh: leaf age should withhold.
	freshLeaf := filepath.Join(target, "release", "incremental")
	if err := os.MkdirAll(freshLeaf, 0o755); err != nil {
		t.Fatal(err)
	}
	freshFile := filepath.Join(freshLeaf, "hot")
	if err := os.WriteFile(freshFile, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make parent target look old; leave release/incremental fresh (default now).
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}
	plan2, err := BuildPrunePlan(context.Background(), cfg, WithReclaimScope(ReclaimScopeIncremental))
	if err != nil {
		t.Fatal(err)
	}
	var sawFreshWithheld, sawOldPrunable bool
	for _, c := range plan2.Candidates {
		switch c.Path {
		case freshLeaf:
			if c.State == CandidateStateWithheld && c.WithheldReason == WithheldReasonAge {
				sawFreshWithheld = true
			}
		case inc:
			if c.State == CandidateStatePrunable {
				sawOldPrunable = true
			}
		}
	}
	if !sawFreshWithheld || !sawOldPrunable {
		t.Fatalf("expected mixed age outcomes, got %+v", plan2.Candidates)
	}
}

// mode000BlocksReadDir reports whether chmod 0o000 prevents ReadDir. Linux CI
// often runs as root, which bypasses DAC and makes mode-000 unusable as a
// permission-error fixture.
func mode000BlocksReadDir(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
	_, err := os.ReadDir(blocked)
	return err != nil
}

func TestDirSizeContext_FullDepthPermissionErrorFailClosed(t *testing.T) {
	if !mode000BlocksReadDir(t) {
		t.Skip("mode 000 does not block ReadDir (common for root/CI); covered by sizeCandidateJobs missing-path test")
	}
	root := t.TempDir()
	secret := filepath.Join(root, "secret")
	if err := os.Mkdir(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0o755) })
	if err := os.WriteFile(filepath.Join(root, "ok"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Full-depth (prune/scan): must error, not return silent partial.
	_, err := DirSizeContext(context.Background(), root, -1)
	if err == nil {
		t.Fatal("expected full-depth walk error")
	}

	// Bounded triage (space): may complete with incomplete flags, no hard error required.
	r, err := DirSizeContext(context.Background(), root, 0)
	if err != nil {
		// Some platforms may still surface the unreadable child; either is fine for bounded.
		t.Logf("bounded walk err (ok): %v", err)
	}
	_ = r
}

func TestBuildPrunePlan_FullDepthUnreadableOmitsCandidate(t *testing.T) {
	if !mode000BlocksReadDir(t) {
		t.Skip("mode 000 does not block ReadDir (common for root/CI); covered by sizeCandidateJobs missing-path test")
	}
	root := t.TempDir()
	proj := filepath.Join(root, "app")
	target := filepath.Join(proj, "target")
	debug := filepath.Join(target, "debug")
	if err := os.MkdirAll(debug, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "Cargo.toml"), []byte("[package]\nname=\"app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(debug, "ok"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(debug, "blocked")
	if err := os.Mkdir(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	cfg := &config.Config{
		Version: 1,
		Paths: []config.PathProfile{{
			Path:     root,
			MaxDepth: 12,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}
	plan, err := BuildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Candidates {
		if c.Path == target && c.State == CandidateStatePrunable {
			t.Fatalf("unreadable subtree must not yield prunable candidate: %+v warnings=%v", c, plan.Warnings)
		}
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("expected sizing warning")
	}
}

// Portable fail-closed coverage for full-depth sizing errors (works as root):
// DirSizeContext error must omit the candidate and surface a warning, never
// admit an understated prunable total.
func TestSizeCandidateJobs_DirSizeErrorOmitsCandidate(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone-target")
	jobs := []CandidateJob{{
		Path:             missing,
		State:            CandidateStatePrunable,
		Pattern:          "**/target",
		Signature:        "development.rust.cargo-target",
		Confidence:       "high",
		EffectiveMinSize: FilterDisplayNone,
		EffectiveMinAge:  FilterDisplayNone,
		EffectiveMaxAge:  FilterDisplayNone,
		ReclaimScope:     ReclaimScopeWhole,
	}}
	cands, warnings := sizeCandidateJobs(context.Background(), jobs, -1)
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates after DirSize error, got %+v", cands)
	}
	if len(warnings) == 0 {
		t.Fatal("expected sizing warning")
	}
}

func TestPlanFromExact_SymlinkedIncrementalRefused(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "app")
	target := filepath.Join(proj, "target")
	debug := filepath.Join(target, "debug")
	if err := os.MkdirAll(debug, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "Cargo.toml"), []byte("[package]\nname=\"app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(debug, "incremental")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanFromExact(context.Background(), ExactPlanInput{
		AnalysisRoot: root,
		Entries: []ExactEntry{{
			Path:             link,
			Signature:        "development.rust.cargo-target",
			EffectiveMinSize: "none",
			EffectiveMinAge:  "none",
			EffectiveMaxAge:  "none",
			ReclaimScope:     ReclaimScopeIncremental,
			ParentPath:       target,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("symlinked incremental must be refused, got %+v", plan.Candidates)
	}
}
