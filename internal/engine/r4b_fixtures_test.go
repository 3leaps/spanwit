package engine

// R4b fixtures (v0.2.2 S3): adversarial cases for the planned grype download
// residue, repo-local .gocache and temp walk-root signatures. These land
// before any engine change so the delete-authority work in S4 is judged
// against a reviewed fixture set.
//
// The grype and .gocache rules (sibling evidence, absolute age floor,
// content marker) are enforced by the engine; temp walk roots are pending in
// internal/space until S4b.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/config"
)

const (
	r4bGrypeSig   = "development.security.grype-db-download"
	r4bGocacheSig = "development.go.repo-local-gocache"
)

// r4bSignatures expresses each planned signature with today's struct. S4 adds
// the evidence fields it cannot express yet (sibling, age floor, marker).
func r4bSignatures() config.SignatureCatalog {
	return config.SignatureCatalog{
		"development": {
			"security": {
				"grype-db-download": {
					Description:       "grype partial database download beside a live db/<N>",
					CandidatePatterns: []string{"**/grype/db/grype-db-download*"},
					AnyChildPaths:     []string{"vulnerability.db"},
					Confidence:        "high",
					SafeToPrune:       true,
				},
			},
			"go": {
				"repo-local-gocache": {
					Description:           "repo-local Go build cache",
					CandidatePatterns:     []string{"**/.gocache"},
					RequiredAncestorFiles: []string{"go.mod"},
					AnyChildPaths:         []string{"README"},
					Confidence:            "high",
					SafeToPrune:           true,
				},
			},
		},
	}
}

func r4bConfig(root string, sigs ...string) *config.Config {
	targets := make([]config.Target, 0, len(sigs))
	for _, s := range sigs {
		targets = append(targets, config.Target{Signature: s})
	}
	return &config.Config{
		Version:    1,
		Signatures: r4bSignatures(),
		Paths: []config.PathProfile{{
			Path:     root,
			MaxDepth: 12,
			Targets:  targets,
		}},
	}
}

func r4bCandidates(t *testing.T, cfg *config.Config, opts ...PlanOptions) []string {
	t.Helper()
	plan, err := BuildPrunePlan(context.Background(), cfg, opts...)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	out := make([]string, 0, len(plan.Candidates))
	for _, c := range plan.Candidates {
		out = append(out, c.Path)
	}
	return out
}

func r4bJobs(t *testing.T, cfg *config.Config, opts ...PlanOptions) []PruneCandidate {
	t.Helper()
	plan, err := BuildPrunePlan(context.Background(), cfg, opts...)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan.Candidates
}

// assertNeverPrunable fails if any candidate at path is prunable: the delete
// gate is State == prunable, the set that reaches the prune handoff.
func assertNeverPrunable(t *testing.T, jobs []PruneCandidate, path string) {
	t.Helper()
	for _, j := range jobs {
		if j.Path == path && j.State == CandidateStatePrunable {
			t.Fatalf("%s must never be prunable: %+v", path, j)
		}
	}
}

// assertAbsent fails if path is in the candidate set in any state.
func assertAbsent(t *testing.T, jobs []PruneCandidate, path string) {
	t.Helper()
	for _, j := range jobs {
		if j.Path == path {
			t.Fatalf("%s must not be a candidate in any state: %+v", path, j)
		}
	}
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// grypeTree builds <root>/Library/Caches/grype/db with a live db/6 and one
// download residue directory, and returns (dbDir, liveDir, residueDir).
func grypeTree(t *testing.T, root string) (string, string, string) {
	t.Helper()
	db := filepath.Join(root, "Library", "Caches", "grype", "db")
	live := filepath.Join(db, "6")
	residue := filepath.Join(db, "grype-db-download1257890935")
	writeRegular(t, filepath.Join(live, "vulnerability.db"))
	writeRegular(t, filepath.Join(live, "import.json"))
	writeRegular(t, filepath.Join(live, "last_update_check"))
	writeRegular(t, filepath.Join(residue, "vulnerability.db"))
	return db, live, residue
}

// goRepoWithCache builds a repo with go.mod and a real-shaped .gocache
// (README marker plus hex shard dirs), and returns the cache path.
func goRepoWithCache(t *testing.T, repo string) string {
	t.Helper()
	writeRegular(t, filepath.Join(repo, "go.mod"))
	cache := filepath.Join(repo, ".gocache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	readme := "This directory holds cached build artifacts from the Go build system.\n" +
		"Run \"go clean -cache\" if the directory is getting too large.\n"
	if err := os.WriteFile(filepath.Join(cache, "README"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, filepath.Join(cache, "00"), filepath.Join(cache, "ff"))
	return cache
}

func hasPath(paths []string, p string) bool {
	for _, x := range paths {
		if x == p {
			return true
		}
	}
	return false
}

// --- grype download residue -------------------------------------------------

// Hard gate: the live database directory db/<N> is never a candidate, however
// the residue rule evolves.
func TestR4bGrype_LiveDBNeverMatches(t *testing.T) {
	root := t.TempDir()
	db, live, residue := grypeTree(t, root)
	// A download name nested inside the live db must not match either.
	nested := filepath.Join(live, "grype-db-download77")
	writeRegular(t, filepath.Join(nested, "vulnerability.db"))
	jobs := r4bJobs(t, r4bConfig(root, r4bGrypeSig))
	for _, p := range []string{live, db, nested} {
		assertAbsent(t, jobs, p)
	}
	found := false
	for _, j := range jobs {
		if j.Path == residue {
			found = true
		}
	}
	if !found {
		t.Fatalf("download residue should match the name rule today: %+v", jobs)
	}
}

// A planted symlink named like a download must not be admitted, and must not
// lead the walk into its target.
func TestR4bGrype_PlantedSymlinkResidueRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlink fixture")
	}
	root := t.TempDir()
	db, live, _ := grypeTree(t, root)
	// The target lives outside the walk root, so the walk can reach it only
	// through the symlink.
	outside := filepath.Join(t.TempDir(), "precious")
	writeRegular(t, filepath.Join(outside, "vulnerability.db"))
	link := filepath.Join(db, "grype-db-download999")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	// Make the symlink target unreadable: if the walk followed the link and
	// opened the target, the walk would record a permission warning.
	if os.Geteuid() != 0 {
		if err := os.Chmod(outside, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(outside, 0o755) })
	}
	plan, err := BuildPrunePlan(context.Background(), r4bConfig(root, r4bGrypeSig))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	got := make([]string, 0, len(plan.Candidates))
	for _, c := range plan.Candidates {
		got = append(got, c.Path)
	}
	for _, p := range []string{link, outside, live} {
		if hasPath(got, p) {
			t.Fatalf("%s must not be a candidate: %v", p, got)
		}
	}
	if os.Geteuid() != 0 && len(plan.Warnings) != 0 {
		t.Fatalf("the walk must not open the symlink target; warnings: %v", plan.Warnings)
	}
}

// Sibling evidence: residue is reclaimable only beside a live db/<N> whose
// vulnerability.db and import.json are plain files (not symlinks). Without a
// live sibling, a grype-db-download directory is a name collision.
func TestR4bGrype_RequiresPlainLiveSibling(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "Library", "Caches", "grype", "db")
	orphan := filepath.Join(db, "grype-db-download42")
	writeRegular(t, filepath.Join(orphan, "vulnerability.db"))
	if got := r4bCandidates(t, r4bConfig(root, r4bGrypeSig)); hasPath(got, orphan) {
		t.Fatalf("residue without a live sibling must not match: %v", got)
	}
	// A live sibling whose import.json is a symlink does not count.
	live := filepath.Join(db, "6")
	writeRegular(t, filepath.Join(live, "vulnerability.db"))
	writeRegular(t, filepath.Join(root, "elsewhere", "import.json"))
	if err := os.Symlink(filepath.Join(root, "elsewhere", "import.json"), filepath.Join(live, "import.json")); err != nil {
		t.Fatal(err)
	}
	if got := r4bCandidates(t, r4bConfig(root, r4bGrypeSig)); hasPath(got, orphan) {
		t.Fatalf("a symlinked live import.json must not satisfy sibling evidence: %v", got)
	}
	// A live sibling whose vulnerability.db is a symlink does not count.
	if err := os.Remove(filepath.Join(live, "import.json")); err != nil {
		t.Fatal(err)
	}
	writeRegular(t, filepath.Join(live, "import.json"))
	if err := os.Remove(filepath.Join(live, "vulnerability.db")); err != nil {
		t.Fatal(err)
	}
	writeRegular(t, filepath.Join(root, "elsewhere", "vulnerability.db"))
	if err := os.Symlink(filepath.Join(root, "elsewhere", "vulnerability.db"), filepath.Join(live, "vulnerability.db")); err != nil {
		t.Fatal(err)
	}
	if got := r4bCandidates(t, r4bConfig(root, r4bGrypeSig)); hasPath(got, orphan) {
		t.Fatalf("a symlinked live vulnerability.db must not satisfy sibling evidence: %v", got)
	}
	// A sibling that is itself a symlinked db/<N> directory does not count.
	if err := os.RemoveAll(live); err != nil {
		t.Fatal(err)
	}
	realLive := filepath.Join(root, "elsewhere", "6")
	writeRegular(t, filepath.Join(realLive, "vulnerability.db"))
	writeRegular(t, filepath.Join(realLive, "import.json"))
	if err := os.Symlink(realLive, live); err != nil {
		t.Fatal(err)
	}
	if got := r4bCandidates(t, r4bConfig(root, r4bGrypeSig)); hasPath(got, orphan) {
		t.Fatalf("a symlinked db/<N> sibling must not satisfy sibling evidence: %v", got)
	}
}

// Absolute age floor: a download younger than the floor is never a candidate,
// even when the user config sets no min_age (an in-flight download).
func TestR4bGrype_AbsoluteAgeFloor(t *testing.T) {
	root := t.TempDir()
	_, _, residue := grypeTree(t, root)
	now := time.Now()
	if err := os.Chtimes(residue, now, now); err != nil {
		t.Fatal(err)
	}
	// The in-flight download stays visible: present, withheld for age, and
	// never prunable. Omitting it would hide it from the operator.
	jobs := r4bJobs(t, r4bConfig(root, r4bGrypeSig))
	assertNeverPrunable(t, jobs, residue)
	matches := 0
	for _, j := range jobs {
		if j.Path != residue {
			continue
		}
		matches++
		if j.State != CandidateStateWithheld || j.WithheldReason != WithheldReasonAge {
			t.Fatalf("a fresh download must be withheld for age, got %+v", j)
		}
	}
	if matches != 1 {
		t.Fatalf("a fresh download must be reported withheld exactly once, not omitted (got %d): %+v", matches, jobs)
	}
}

// --- repo-local .gocache ----------------------------------------------------

// Positive control: a real-shaped .gocache under a go.mod matches today.
func TestR4bGocache_RealShapeMatches(t *testing.T) {
	root := t.TempDir()
	cache := goRepoWithCache(t, filepath.Join(root, "repo"))
	if got := r4bCandidates(t, r4bConfig(root, r4bGocacheSig)); !hasPath(got, cache) {
		t.Fatalf("real-shaped .gocache under go.mod should match: %v", got)
	}
}

// No go.mod ancestor: never a candidate.
func TestR4bGocache_WithoutGoModNeverMatches(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "notes", ".gocache")
	writeRegular(t, filepath.Join(cache, "README"))
	if got := r4bCandidates(t, r4bConfig(root, r4bGocacheSig)); hasPath(got, cache) {
		t.Fatalf(".gocache without a go.mod ancestor must not match: %v", got)
	}
}

// A symlinked go.mod does not count as ancestor evidence.
func TestR4bGocache_SymlinkedGoModRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlink fixture")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	cache := goRepoWithCache(t, repo)
	if err := os.Remove(filepath.Join(repo, "go.mod")); err != nil {
		t.Fatal(err)
	}
	writeRegular(t, filepath.Join(root, "real", "go.mod"))
	if err := os.Symlink(filepath.Join(root, "real", "go.mod"), filepath.Join(repo, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if got := r4bCandidates(t, r4bConfig(root, r4bGocacheSig)); hasPath(got, cache) {
		t.Fatalf("a symlinked go.mod must not satisfy ancestor evidence: %v", got)
	}
}

// Name collision: a user folder named .gocache under a Go repo, without the
// Go build-cache content marker, is never a candidate.
func TestR4bGocache_NameCollisionWithoutMarker(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	writeRegular(t, filepath.Join(repo, "go.mod"))
	cache := filepath.Join(repo, ".gocache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "README"), []byte("my notes about caching\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := r4bCandidates(t, r4bConfig(root, r4bGocacheSig)); hasPath(got, cache) {
		t.Fatalf("a .gocache without the Go build-cache marker must not match: %v", got)
	}
}

// Case-insensitive filesystems: case variants of either name must not match,
// even through a matcher that would report the canonical spelling. The tree
// holds only variants, so the candidate set must be exactly empty.
func TestR4b_CaseVariantsDoNotMatch(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	writeRegular(t, filepath.Join(repo, "go.mod"))
	gc := filepath.Join(repo, ".GoCache")
	writeRegular(t, filepath.Join(gc, "README"))
	mkdirs(t, filepath.Join(gc, "00"))
	db := filepath.Join(root, "Library", "Caches", "grype", "db")
	writeRegular(t, filepath.Join(db, "6", "vulnerability.db"))
	writeRegular(t, filepath.Join(db, "6", "import.json"))
	writeRegular(t, filepath.Join(db, "Grype-DB-Download1", "vulnerability.db"))
	if jobs := r4bJobs(t, r4bConfig(root, r4bGrypeSig, r4bGocacheSig)); len(jobs) != 0 {
		t.Fatalf("a tree with only case variants must yield no candidates, got %+v", jobs)
	}
}

// --- scope and reach --------------------------------------------------------

// Incremental reclaim stays Cargo-only: grype and .gocache candidates are
// refused under an incremental plan.
func TestR4b_IncrementalPlanRefusesNewSignatures(t *testing.T) {
	root := t.TempDir()
	grypeTree(t, root)
	goRepoWithCache(t, filepath.Join(root, "repo"))
	got := r4bCandidates(t, r4bConfig(root, r4bGrypeSig, r4bGocacheSig), WithReclaimScope(ReclaimScopeIncremental))
	if len(got) != 0 {
		t.Fatalf("incremental scope must refuse non-Cargo candidates, got %v", got)
	}
}

// Adding the new signatures leaves the existing Cargo candidate set exactly
// unchanged: no Cargo candidate lost, none added.
func TestR4b_ExistingCargoSetUnchanged(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	writeRegular(t, filepath.Join(app, "Cargo.toml"))
	mkdirs(t, filepath.Join(app, "target", "debug"))
	grypeTree(t, root)
	goRepoWithCache(t, filepath.Join(root, "repo"))

	cargoSet := func(jobs []PruneCandidate) map[string]string {
		out := map[string]string{}
		for _, j := range jobs {
			if j.Signature == "development.rust.cargo-target" {
				out[j.Path] = j.State
			}
		}
		return out
	}
	cargoOnly := &config.Config{Version: 1, Paths: []config.PathProfile{{
		Path: root, MaxDepth: 12, Targets: []config.Target{{Signature: "development.rust.cargo-target"}},
	}}}
	before := cargoSet(r4bJobs(t, cargoOnly))
	after := cargoSet(r4bJobs(t, r4bConfig(root, "development.rust.cargo-target", r4bGrypeSig, r4bGocacheSig)))
	if len(before) == 0 {
		t.Fatal("fixture should yield a cargo candidate")
	}
	if len(before) != len(after) {
		t.Fatalf("cargo set changed: before %v after %v", before, after)
	}
	for p, st := range before {
		if after[p] != st {
			t.Fatalf("cargo candidate %s changed from %q to %q", p, st, after[p])
		}
	}
}

// An unreadable foreign subtree fails closed: no candidate and a recorded
// warning, never a partial delete set. True today; S4's temp roots must keep it.
func TestR4b_UnreadableSubtreeFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission fixture")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	root := t.TempDir()
	foreign := filepath.Join(root, "other-user")
	app := filepath.Join(foreign, "app")
	writeRegular(t, filepath.Join(app, "Cargo.toml"))
	mkdirs(t, filepath.Join(app, "target", "debug"))
	if err := os.Chmod(foreign, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(foreign, 0o755) })
	plan, err := BuildPrunePlan(context.Background(), r4bConfig(root, "development.rust.cargo-target"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Candidates) != 0 || len(plan.Warnings) == 0 {
		t.Fatalf("unreadable foreign tree must yield no candidates and a warning: %+v", plan)
	}
}
