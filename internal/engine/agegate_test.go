package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/config"
)

// A configured min_age reads the newest activity inside a candidate, not only
// the candidate directory's own mtime: a directory's mtime does not move when
// a file deeper inside it is written.

func cargoProject(t *testing.T, root, name string) string {
	t.Helper()
	writeFile(t, filepath.Join(root, name, "Cargo.toml"), 64)
	target := filepath.Join(root, name, "target")
	writeFile(t, filepath.Join(target, "debug", "app.o"), 2048)
	return target
}

func minAgePlan(t *testing.T, root, minAge string, scope string) PrunePlan {
	t.Helper()
	cfg := &config.Config{
		Version:  1,
		Defaults: config.Defaults{MinAge: minAge},
		Paths: []config.PathProfile{{
			Path: root, MaxDepth: 4,
			Targets: []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}
	opts := []PlanOptions{}
	if scope != "" {
		opts = append(opts, WithReclaimScope(scope))
	}
	plan, err := BuildPrunePlan(context.Background(), cfg, opts...)
	if err != nil {
		t.Fatalf("BuildPrunePlan: %v", err)
	}
	return plan
}

func stateAt(t *testing.T, cands []PruneCandidate, path string) PruneCandidate {
	t.Helper()
	for _, c := range cands {
		if c.Path == path {
			return c
		}
	}
	t.Fatalf("no candidate at %s", path)
	return PruneCandidate{}
}

func cargoExact(target string) ExactEntry {
	return ExactEntry{
		Path: target, Signature: "development.rust.cargo-target",
		EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: "7d", EffectiveMaxAge: FilterDisplayNone,
	}
}

func TestConfiguredMinAge_FreshFileInsideOldTargetIsWithheld(t *testing.T) {
	root := t.TempDir()
	target := cargoProject(t, root, "crate")
	ageTree(t, target, 30*24*time.Hour)
	// An active build writes below the target; target's own mtime stays old.
	writeFile(t, filepath.Join(target, "debug", "deps", "fresh.o"), 512)
	if err := os.Chtimes(target, time.Now().Add(-30*24*time.Hour), time.Now().Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	c := stateAt(t, minAgePlan(t, root, "7d", "").Candidates, target)
	if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonAge {
		t.Fatalf("discovery: active target under min_age 7d must be withheld for age: %+v", c)
	}
	c = stateAt(t, exactFor(t, root, cargoExact(target)), target)
	if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonAge {
		t.Fatalf("exact revalidation: active target under min_age 7d must be withheld for age: %+v", c)
	}
}

// Activity that only creates or removes entries moves a nested directory's
// mtime and no file's: a build's stale-artifact sweep (unlink) or a fresh
// build-script output directory. Both count.
func TestConfiguredMinAge_NestedDirectoryActivityIsWithheld(t *testing.T) {
	cases := map[string]func(t *testing.T, target string){
		"file just unlinked": func(t *testing.T, target string) {
			stale := filepath.Join(target, "debug", "deps", "stale.o")
			writeFile(t, stale, 128)
			ageTree(t, target, 30*24*time.Hour)
			if err := os.Remove(stale); err != nil {
				t.Fatal(err)
			}
		},
		"empty directory just created": func(t *testing.T, target string) {
			mkdirAll(t, filepath.Join(target, "debug", "build"))
			ageTree(t, target, 30*24*time.Hour)
			mkdirAll(t, filepath.Join(target, "debug", "build", "foo-123"))
		},
	}
	for name, act := range cases {
		root := t.TempDir()
		target := cargoProject(t, root, "crate")
		act(t, target)
		// The candidate's own mtime stays old: only nested activity is fresh.
		old := time.Now().Add(-30 * 24 * time.Hour)
		if err := os.Chtimes(target, old, old); err != nil {
			t.Fatal(err)
		}
		c := stateAt(t, minAgePlan(t, root, "7d", "").Candidates, target)
		if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonAge {
			t.Errorf("%s: discovery must withhold for age: %+v", name, c)
		}
		c = stateAt(t, exactFor(t, root, cargoExact(target)), target)
		if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonAge {
			t.Errorf("%s: exact revalidation must withhold for age: %+v", name, c)
		}
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredMinAge_OldTreeStaysPrunable(t *testing.T) {
	root := t.TempDir()
	target := cargoProject(t, root, "crate")
	ageTree(t, target, 30*24*time.Hour)

	if c := stateAt(t, minAgePlan(t, root, "7d", "").Candidates, target); c.State != CandidateStatePrunable {
		t.Fatalf("discovery: an idle target is prunable under min_age 7d: %+v", c)
	}
	if c := stateAt(t, exactFor(t, root, cargoExact(target)), target); c.State != CandidateStatePrunable {
		t.Fatalf("exact revalidation: an idle target is prunable under min_age 7d: %+v", c)
	}
}

// The activity read is the full sizing walk, not the capped floor walk: a
// real cargo target holds tens of thousands of entries and must not read as
// incomplete (and so withheld) merely for its size.
func TestConfiguredMinAge_LargeIdleTargetStaysPrunable(t *testing.T) {
	if testing.Short() {
		t.Skip("writes more than floorScanEntryLimit files")
	}
	root := t.TempDir()
	target := cargoProject(t, root, "crate")
	deps := filepath.Join(target, "debug", "deps")
	if err := os.MkdirAll(deps, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= floorScanEntryLimit; i++ {
		f, err := os.Create(filepath.Join(deps, fmt.Sprintf("o%05d", i)))
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	ageTree(t, target, 30*24*time.Hour)

	if c := stateAt(t, minAgePlan(t, root, "7d", "").Candidates, target); c.State != CandidateStatePrunable {
		t.Fatalf("discovery: a large idle target is prunable under min_age 7d: %+v", c)
	}
	if c := stateAt(t, exactFor(t, root, cargoExact(target)), target); c.State != CandidateStatePrunable {
		t.Fatalf("exact revalidation: a large idle target is prunable under min_age 7d: %+v", c)
	}
}

func TestConfiguredMinAge_FreshIncrementalCrateIsWithheld(t *testing.T) {
	root := t.TempDir()
	target := cargoProject(t, root, "crate")
	inc := filepath.Join(target, "debug", "incremental")
	session := filepath.Join(inc, "app-1abc", "s-old")
	writeFile(t, filepath.Join(session, "dep-graph.bin"), 1024)
	ageTree(t, target, 30*24*time.Hour)
	// rustc writes inside an existing session dir; the incremental leaf's own
	// mtime is reset to old to isolate the descendant read.
	writeFile(t, filepath.Join(session, "query-cache.bin"), 1024)
	old := time.Now().Add(-30 * 24 * time.Hour)
	for _, d := range []string{inc, filepath.Join(inc, "app-1abc")} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}

	c := stateAt(t, minAgePlan(t, root, "7d", ReclaimScopeIncremental).Candidates, inc)
	if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonAge {
		t.Fatalf("incremental: an active incremental leaf under min_age 7d must be withheld for age: %+v", c)
	}
	e := cargoExact(inc)
	e.ReclaimScope = ReclaimScopeIncremental
	e.ParentPath = target
	c = stateAt(t, exactFor(t, root, e), inc)
	if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonAge {
		t.Fatalf("incremental exact: an active incremental leaf under min_age 7d must be withheld for age: %+v", c)
	}
}

// Without a min_age the descendant read changes nothing.
func TestNoMinAge_FreshFileInsideTargetStaysPrunable(t *testing.T) {
	root := t.TempDir()
	target := cargoProject(t, root, "crate")
	if c := stateAt(t, minAgePlan(t, root, "", "").Candidates, target); c.State != CandidateStatePrunable {
		t.Fatalf("no min_age: a fresh target stays prunable as before: %+v", c)
	}
}

func TestDescendantsMeetMinAge(t *testing.T) {
	now := time.Now()
	old := SizeResult{NewestMtime: now.Add(-30 * 24 * time.Hour)}
	fresh := SizeResult{NewestMtime: now.Add(-time.Minute)}
	cases := []struct {
		name   string
		minAge string
		sized  SizeResult
		want   bool
	}{
		{"no floor", FilterDisplayNone, fresh, true},
		{"empty floor", "", fresh, true},
		{"old activity", "7d", old, true},
		{"fresh activity", "7d", fresh, false},
		{"activity incomplete", "7d", SizeResult{NewestMtime: old.NewestMtime, ActivityIncomplete: true}, false},
		{"size incomplete", "7d", SizeResult{NewestMtime: old.NewestMtime, Incomplete: true}, false},
		{"nothing beneath", "7d", SizeResult{}, true},
		{"unparseable floor", "soon", old, false},
	}
	for _, tc := range cases {
		if got := descendantsMeetMinAge(tc.minAge, tc.sized, now); got != tc.want {
			t.Errorf("%s: descendantsMeetMinAge(%q) = %v, want %v", tc.name, tc.minAge, got, tc.want)
		}
	}
}

// A regular-file candidate's own mtime is its activity: an old file is
// admitted under min_age, not withheld as a failed directory walk.
func TestConfiguredMinAge_RegularFileCandidateUsesOwnMtime(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "proj", "out.bin")
	writeFile(t, file, 2048)
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	sized, err := DirSizeContext(context.Background(), file, -1)
	if err != nil {
		t.Fatal(err)
	}
	if !descendantsMeetMinAge("7d", sized, time.Now()) {
		t.Fatalf("an old regular file meets min_age 7d by its own mtime: %+v", sized)
	}
}
