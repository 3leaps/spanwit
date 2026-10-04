package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/config"
)

func TestDirSizeContext_TracksNewestMtime(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old.bin")
	fresh := filepath.Join(root, "sub", "fresh.bin")
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(fresh), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fresh, []byte("fresh"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, recent, recent); err != nil {
		t.Fatal(err)
	}

	r, err := DirSizeContext(context.Background(), root, -1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Incomplete || r.ActivityIncomplete {
		t.Fatalf("expected complete walk, got incomplete=%v activity_incomplete=%v", r.Incomplete, r.ActivityIncomplete)
	}
	if r.NewestMtime.IsZero() || r.NewestMtime.Before(recent.Add(-time.Minute)) {
		t.Fatalf("newest mtime %v not near %v", r.NewestMtime, recent)
	}
}

func TestDirSizeContext_DepthBoundMarksActivityIncomplete(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c.bin")
	if err := os.MkdirAll(filepath.Dir(deep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := DirSizeContext(context.Background(), root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Incomplete || !r.ActivityIncomplete {
		t.Fatalf("expected incomplete activity, got %+v", r)
	}
}

func TestDeriveRebuildExpectation(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		mt   time.Time
		inc  bool
		want string
	}{
		{"incomplete", now.Add(-time.Hour), true, ""},
		{"zero", time.Time{}, false, ""},
		{"active", now.Add(-24 * time.Hour), false, RebuildHigh},
		{"warm", now.Add(-14 * 24 * time.Hour), false, RebuildMedium},
		{"idle", now.Add(-60 * 24 * time.Hour), false, RebuildLow},
		{"future", now.Add(time.Hour), false, RebuildHigh},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveRebuildExpectation(tc.mt, tc.inc, now)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestSortCandidates_IdleRanksUnknownLast(t *testing.T) {
	old := time.Now().Add(-90 * 24 * time.Hour)
	fresh := time.Now().Add(-time.Hour)
	cands := []PruneCandidate{
		{Path: "/big-unknown", Size: 9e9, ActivityIncomplete: true, ActivityBasis: ActivityBasisUnknown},
		{Path: "/active", Size: 1e6, LastActivityAt: fresh, ActivityBasis: ActivityBasisDescendantMtime},
		{Path: "/idle", Size: 2e6, LastActivityAt: old, ActivityBasis: ActivityBasisDescendantMtime},
	}
	SortCandidates(cands, SortIdle)
	if cands[0].Path != "/idle" || cands[1].Path != "/active" || cands[2].Path != "/big-unknown" {
		t.Fatalf("order: %v %v %v", cands[0].Path, cands[1].Path, cands[2].Path)
	}
}

func TestSortCandidates_SizeBoundAware(t *testing.T) {
	// A huge incomplete lower bound must not rank above a smaller complete size.
	cands := []PruneCandidate{
		{Path: "/inc-huge", Size: 9e9, SizeIncomplete: true},
		{Path: "/complete-small", Size: 100},
		{Path: "/complete-big", Size: 200},
		{Path: "/inc-small", Size: 50, SizeIncomplete: true},
	}
	SortCandidates(cands, SortSize)
	want := []string{"/complete-big", "/complete-small", "/inc-huge", "/inc-small"}
	for i, p := range want {
		if cands[i].Path != p {
			t.Fatalf("order[%d]=%s want %s", i, cands[i].Path, p)
		}
	}
}

func TestBuildPrunePlan_IncompleteBelowMinSizeIsIndeterminate(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "app")
	target := filepath.Join(proj, "target")
	if err := os.MkdirAll(filepath.Join(target, "debug", "a", "b", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "Cargo.toml"), []byte("[package]\nname=\"app\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Shallow mass only — depth-bounded size will be a small incomplete lower bound.
	if err := os.WriteFile(filepath.Join(target, "shallow.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "debug", "a", "b", "c", "deep.bin"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{MinSize: "1G"},
		Paths: []config.PathProfile{{
			Path:     root,
			MaxDepth: 12,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}
	plan, err := BuildPrunePlan(context.Background(), cfg, WithSizeMaxDepth(1), WithSortMode(SortSize))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("want 1 candidate, got %+v warnings=%v", plan.Candidates, plan.Warnings)
	}
	c := plan.Candidates[0]
	if !c.SizeIncomplete {
		t.Fatalf("expected size_incomplete lower bound, got %+v", c)
	}
	if c.Size >= 1024*1024*1024 {
		t.Fatalf("lower bound should be below 1G floor, got %d", c.Size)
	}
	if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonIndeterminate {
		t.Fatalf("want withheld/indeterminate (not min_size), got %s/%s", c.State, c.WithheldReason)
	}
}

func TestFindCargoIncrementalDirs(t *testing.T) {
	target := t.TempDir()
	paths := []string{
		filepath.Join(target, "debug", "incremental"),
		filepath.Join(target, "release", "incremental"),
		filepath.Join(target, "x86_64-apple-darwin", "debug", "incremental"),
	}
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Symlinked incremental must be ignored.
	evil := filepath.Join(target, "evil")
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(evil, "incremental")); err != nil {
		t.Fatal(err)
	}

	found, err := FindCargoIncrementalDirs(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 3 {
		t.Fatalf("found %d want 3: %v", len(found), found)
	}
	for _, p := range found {
		if err := ValidateIncrementalUnderParent(target, p); err != nil {
			t.Fatalf("validate %s: %v", p, err)
		}
	}
}

func TestValidateIncrementalUnderParent_RejectsEscape(t *testing.T) {
	parent := t.TempDir()
	if err := ValidateIncrementalUnderParent(parent, filepath.Join(parent, "..", "other", "incremental")); err == nil {
		t.Fatal("expected escape rejection")
	}
	if err := ValidateIncrementalUnderParent(parent, filepath.Join(parent, "debug", "deps")); err == nil {
		t.Fatal("expected non-incremental leaf rejection")
	}
}

func TestCollapseContainment(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "target")
	child := filepath.Join(parent, "debug", "incremental")
	jobs := []CandidateJob{
		{Path: child},
		{Path: parent},
		{Path: filepath.Join(root, "other")},
	}
	out, dropped := CollapseContainment(jobs)
	if dropped != 1 {
		t.Fatalf("dropped=%d want 1", dropped)
	}
	if len(out) != 2 {
		t.Fatalf("len=%d want 2", len(out))
	}
	// Parent (ancestor) kept; child dropped.
	for _, j := range out {
		if j.Path == child {
			t.Fatal("child should have been collapsed")
		}
	}
}

func TestIsSafePathSegment(t *testing.T) {
	if !IsSafePathSegment("debug") || !IsSafePathSegment("x86_64-apple-darwin") {
		t.Fatal("expected safe segments")
	}
	for _, bad := range []string{"", "..", "a/b", "a\\b", ";rm", "$(x)", "-leading"} {
		if IsSafePathSegment(bad) {
			t.Fatalf("expected unsafe: %q", bad)
		}
	}
}

// ActivityKnown is the single predicate for "activity established"; incomplete,
// zero, or unknown-basis measurements are never known (never ranked as cold).
func TestActivityKnown(t *testing.T) {
	ts := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		c    PruneCandidate
		want bool
	}{
		{"complete", PruneCandidate{LastActivityAt: ts, ActivityBasis: ActivityBasisDescendantMtime}, true},
		{"incomplete", PruneCandidate{LastActivityAt: ts, ActivityBasis: ActivityBasisDescendantMtime, ActivityIncomplete: true}, false},
		{"zero time", PruneCandidate{ActivityBasis: ActivityBasisDescendantMtime}, false},
		{"unknown basis", PruneCandidate{LastActivityAt: ts, ActivityBasis: ActivityBasisUnknown}, false},
	} {
		if got := ActivityKnown(tc.c); got != tc.want {
			t.Errorf("%s: ActivityKnown=%v want %v", tc.name, got, tc.want)
		}
	}
}

// An older timestamp with incomplete activity is not known-cold: idle ranking
// must put a newer, complete measurement ahead of it.
func TestSortCandidates_IdleRanksIncompleteAfterKnownEvenIfOlder(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cands := []PruneCandidate{
		{Path: "/older-incomplete", LastActivityAt: old, ActivityBasis: ActivityBasisDescendantMtime, ActivityIncomplete: true},
		{Path: "/newer-known", LastActivityAt: newer, ActivityBasis: ActivityBasisDescendantMtime},
	}
	SortCandidates(cands, SortIdle)
	if cands[0].Path != "/newer-known" {
		t.Fatalf("incomplete activity ranked as colder than known: %v, %v", cands[0].Path, cands[1].Path)
	}
}
