package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/config"
)

const (
	relocatedCargoID = "development.rust.cargo-target-relocated"
	relocatedGoID    = "development.go.build-cache-relocated"
	cargoTagContent  = "Signature: 8a477f597d28d172789f06886806bc55\n" +
		"# This file is a cache directory tag created by cargo.\n" +
		"# For information about cache directory tags see https://bford.info/cachedir/\n"
	goReadmeContent = "This directory holds cached build artifacts from the Go build system.\n" +
		"Run \"go clean -cache\" if the directory is getting too large.\n"
)

// builtInConfig scans root with every built-in signature in the shipped
// evaluation order (name-pattern first, then marker-only).
func builtInConfig(root string) *config.Config {
	var targets []config.Target
	for _, id := range config.BuiltInSignatureIDs() {
		targets = append(targets, config.Target{Signature: id})
	}
	return &config.Config{
		Version: 1,
		Paths:   []config.PathProfile{{Path: root, MaxDepth: 12, Targets: targets}},
	}
}

func writeContent(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// relocatedCargo builds a real-shaped relocated Cargo target at dir.
func relocatedCargo(t *testing.T, dir string) string {
	t.Helper()
	writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"), cargoTagContent)
	writeRegular(t, filepath.Join(dir, ".rustc_info.json"))
	writeRegular(t, filepath.Join(dir, "debug", "build", "x", "output"))
	mkdirs(t, filepath.Join(dir, "debug", ".fingerprint"))
	return dir
}

// relocatedGo builds a real-shaped Go build cache at dir.
func relocatedGo(t *testing.T, dir string) string {
	t.Helper()
	writeContent(t, filepath.Join(dir, "README"), goReadmeContent)
	writeRegular(t, filepath.Join(dir, "trim.txt"))
	writeRegular(t, filepath.Join(dir, "0a", "0a1b-d"))
	return dir
}

func findCandidate(jobs []PruneCandidate, path string) (PruneCandidate, bool) {
	for _, j := range jobs {
		if j.Path == path {
			return j, true
		}
	}
	return PruneCandidate{}, false
}

func assertSignature(t *testing.T, jobs []PruneCandidate, path, id string) PruneCandidate {
	t.Helper()
	c, ok := findCandidate(jobs, path)
	if !ok {
		t.Fatalf("%s should be a candidate: %+v", path, jobs)
	}
	if c.Signature != id {
		t.Fatalf("%s signature = %q, want %q", path, c.Signature, id)
	}
	return c
}

// isolateLiveCaches points HOME and GOCACHE away from every fixture so the
// live-cache exclusions only act where a test asks them to.
func isolateLiveCaches(t *testing.T) {
	t.Helper()
	notSharedTemp(t)
	setHome(t, t.TempDir())
	t.Setenv("GOCACHE", "")
	t.Setenv("GOENV", "off") // tests that exercise GOENV set it themselves
	t.Setenv("GOROOT", "")
	t.Setenv("PATH", t.TempDir()) // no host go binary, so no host go.env
}

// setHome points os.UserHomeDir at dir on every platform. It does not touch
// the shared-temp rules; tests whose fixtures may sit under /tmp call
// notSharedTemp (isolateLiveCaches does) and shared-temp tests call
// realSharedTemp or withSharedTemp.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// notSharedTemp treats no path as shared temp for this test, so fixtures under
// t.TempDir() (/tmp on Linux) are walked like any other root. The shared-temp
// tests set the list explicitly.
func notSharedTemp(t *testing.T) {
	t.Helper()
	saved, savedSticky := sharedTempPaths, stickyAncestorCheck
	sharedTempPaths, stickyAncestorCheck = nil, false
	t.Cleanup(func() { sharedTempPaths, stickyAncestorCheck = saved, savedSticky })
}

// withSharedTemp makes dir the only shared temp path for this test.
func withSharedTemp(t *testing.T, dir string) {
	t.Helper()
	saved := sharedTempPaths
	sharedTempPaths = []string{dir}
	t.Cleanup(func() { sharedTempPaths = saved })
}

// --- relocated Cargo target -------------------------------------------------

func TestRelocatedCargo_RealShapeMatchesAsWholeDir(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedCargo(t, filepath.Join(root, "rsfulmen-pr30-devrev-target"))
	ageTree(t, dir, 2*time.Hour)
	jobs := r4bJobs(t, builtInConfig(root))
	c := assertSignature(t, jobs, dir, relocatedCargoID)
	if c.State != CandidateStatePrunable {
		t.Fatalf("relocated target should be prunable: %+v", c)
	}
	if c.Pattern != "marker:CACHEDIR.TAG" {
		t.Fatalf("pattern = %q", c.Pattern)
	}
	// The candidate is the whole cache root: nothing beneath it is listed.
	for _, j := range jobs {
		if j.Path != dir && isWithinDir(dir, j.Path) {
			t.Fatalf("inner path listed separately: %s", j.Path)
		}
	}
}

func TestRelocatedCargo_CRLFTagMatches(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := filepath.Join(root, "t")
	writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"),
		"Signature: 8a477f597d28d172789f06886806bc55\r\n# This file is a cache directory tag created by cargo.\r\n")
	mkdirs(t, filepath.Join(dir, "release"))
	assertSignature(t, r4bJobs(t, builtInConfig(root)), dir, relocatedCargoID)
}

func TestRelocatedCargo_TwoLineTagWithoutTrailingNewlineMatches(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := filepath.Join(root, "t")
	writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"),
		"Signature: 8a477f597d28d172789f06886806bc55\n# This file is a cache directory tag created by cargo.")
	mkdirs(t, filepath.Join(dir, "debug"))
	assertSignature(t, r4bJobs(t, builtInConfig(root)), dir, relocatedCargoID)
}

// Every refusal in the delete-path matrix: the directory is absent in any state.
func TestRelocatedCargo_EvidenceRefusals(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"line 2 missing": func(t *testing.T, dir string) {
			writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"), "Signature: 8a477f597d28d172789f06886806bc55\n")
			mkdirs(t, filepath.Join(dir, "debug"))
		},
		"line 2 altered": func(t *testing.T, dir string) {
			writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"),
				"Signature: 8a477f597d28d172789f06886806bc55\n# This file is a cache directory tag created by cargo\n")
			mkdirs(t, filepath.Join(dir, "debug"))
		},
		"generic tag from another tool": func(t *testing.T, dir string) {
			writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"),
				"Signature: 8a477f597d28d172789f06886806bc55\n# This file is a cache directory tag created by othertool.\n")
			mkdirs(t, filepath.Join(dir, "debug"))
		},
		"lines out of order": func(t *testing.T, dir string) {
			writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"),
				"# This file is a cache directory tag created by cargo.\nSignature: 8a477f597d28d172789f06886806bc55\n")
			mkdirs(t, filepath.Join(dir, "debug"))
		},
		"leading blank line": func(t *testing.T, dir string) {
			writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"), "\n"+cargoTagContent)
			mkdirs(t, filepath.Join(dir, "debug"))
		},
		"no debug or release child": func(t *testing.T, dir string) {
			writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"), cargoTagContent)
			writeRegular(t, filepath.Join(dir, ".rustc_info.json"))
		},
		"tag is a directory": func(t *testing.T, dir string) {
			mkdirs(t, filepath.Join(dir, "CACHEDIR.TAG"), filepath.Join(dir, "debug"))
		},
		"first line beyond the read cap": func(t *testing.T, dir string) {
			long := make([]byte, markerReadLimit+10)
			for i := range long {
				long[i] = 'a'
			}
			writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"), string(long))
			mkdirs(t, filepath.Join(dir, "debug"))
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			isolateLiveCaches(t)
			root := t.TempDir()
			dir := filepath.Join(root, "scratch-target")
			build(t, dir)
			assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
		})
	}
}

func TestRelocatedCargo_SymlinkRefusals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlink fixture")
	}
	t.Run("tag is a symlink", func(t *testing.T) {
		isolateLiveCaches(t)
		root := t.TempDir()
		writeContent(t, filepath.Join(root, "elsewhere", "CACHEDIR.TAG"), cargoTagContent)
		dir := filepath.Join(root, "scratch-target")
		mkdirs(t, filepath.Join(dir, "debug"))
		if err := os.Symlink(filepath.Join(root, "elsewhere", "CACHEDIR.TAG"), filepath.Join(dir, "CACHEDIR.TAG")); err != nil {
			t.Fatal(err)
		}
		assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
	})
	t.Run("debug is a symlink", func(t *testing.T) {
		isolateLiveCaches(t)
		root := t.TempDir()
		dir := filepath.Join(root, "scratch-target")
		writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"), cargoTagContent)
		mkdirs(t, filepath.Join(root, "real-debug"))
		if err := os.Symlink(filepath.Join(root, "real-debug"), filepath.Join(dir, "debug")); err != nil {
			t.Fatal(err)
		}
		assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
	})
	t.Run("symlinked ancestor is never walked", func(t *testing.T) {
		isolateLiveCaches(t)
		root := t.TempDir()
		real := t.TempDir()
		relocatedCargo(t, filepath.Join(real, "t"))
		if err := os.Symlink(real, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		jobs := r4bJobs(t, builtInConfig(root))
		assertAbsent(t, jobs, filepath.Join(root, "link", "t"))
	})
	t.Run("exact plan through a symlinked ancestor", func(t *testing.T) {
		isolateLiveCaches(t)
		root := t.TempDir()
		relocatedCargo(t, filepath.Join(root, "real", "t"))
		if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "link", "t")
		plan, err := PlanFromExact(context.Background(), ExactPlanInput{
			RequestedMinAge: RequestedMinAgeNone,
			AnalysisRoot:    root,
			Entries: []ExactEntry{{Path: path, Signature: relocatedCargoID,
				EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone}},
		})
		if err == nil {
			assertAbsent(t, plan.Candidates, path)
		}
	})
}

// Nested relocated targets: only the outer root is a candidate.
func TestRelocatedCargo_NestedKeepsOuterOnly(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	outer := relocatedCargo(t, filepath.Join(root, "outer-target"))
	inner := relocatedCargo(t, filepath.Join(outer, "debug", "inner-target"))
	jobs := r4bJobs(t, builtInConfig(root))
	assertSignature(t, jobs, outer, relocatedCargoID)
	assertAbsent(t, jobs, inner)
}

// A conventional target/ under Cargo.toml that also carries CACHEDIR.TAG keeps
// the conventional id (incremental mode depends on it).
func TestRelocatedCargo_ConventionalTargetKeepsCargoTargetID(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	writeRegular(t, filepath.Join(root, "repo", "Cargo.toml"))
	dir := relocatedCargo(t, filepath.Join(root, "repo", "target"))
	assertSignature(t, r4bJobs(t, builtInConfig(root)), dir, "development.rust.cargo-target")
}

// The relocation sites seen in the field: a Library/Caches-style parent and a
// planning scratch tree at depth 4, both with non-target names.
func TestRelocatedCargo_FieldRelocationSites(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	lib := relocatedCargo(t, filepath.Join(root, "Library", "Caches", "queueprims-mutations", "target"))
	plan := relocatedCargo(t, filepath.Join(root, "planning", "yamlprims", "anchor-registration", "targets", "baseline"))
	jobs := r4bJobs(t, builtInConfig(root))
	assertSignature(t, jobs, lib, relocatedCargoID)
	assertSignature(t, jobs, plan, relocatedCargoID)
}

// Age: activity anywhere beneath the root withholds, even when the root
// directory's own mtime is old.
func TestRelocatedCargo_FreshDescendantWithheld(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedCargo(t, filepath.Join(root, "scratch-target"))
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	cfg := builtInConfig(root)
	cfg.Defaults.MinAge = "6h"
	c := assertSignature(t, r4bJobs(t, cfg), dir, relocatedCargoID)
	if c.State == CandidateStatePrunable {
		t.Fatalf("a target with a fresh descendant must not be prunable: %+v", c)
	}
}

// Exact-plan replay re-checks the marker at execute time.
func TestRelocatedCargo_ExactPlanRevalidatesMarker(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedCargo(t, filepath.Join(root, "scratch-target"))
	entry := ExactEntry{Path: dir, Signature: relocatedCargoID,
		EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone}
	in := ExactPlanInput{RequestedMinAge: RequestedMinAgeNone, AnalysisRoot: root, Entries: []ExactEntry{entry}}
	plan, err := PlanFromExact(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	assertSignature(t, plan.Candidates, dir, relocatedCargoID)

	writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"), "Signature: 8a477f597d28d172789f06886806bc55\n")
	plan, err = PlanFromExact(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, plan.Candidates, dir)
}

// A marker-only signature must not guard every path: a pattern-only target
// keeps working on directories that carry no relocated-cache marker.
func TestRelocated_PatternOnlyTargetsUnaffected(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := filepath.Join(root, "proj", "dist")
	writeRegular(t, filepath.Join(dir, "bundle.js"))
	cfg := builtInConfig(root)
	cfg.Paths[0].Targets = append(cfg.Paths[0].Targets, config.Target{Pattern: "**/dist"})
	c, ok := findCandidate(r4bJobs(t, cfg), dir)
	if !ok || c.Pattern != "**/dist" {
		t.Fatalf("pattern-only target should still match: %+v ok=%v", c, ok)
	}
}

// --- relocated Go build cache ----------------------------------------------

func TestRelocatedGo_ReviewHomeCacheMatches(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedGo(t, filepath.Join(root, "gonimbus-devrev-home.x1", "Library", "Caches", "go-build"))
	ageTree(t, dir, 2*time.Hour)
	c := assertSignature(t, r4bJobs(t, builtInConfig(root)), dir, relocatedGoID)
	if c.State != CandidateStatePrunable || c.Pattern != "marker:README" {
		t.Fatalf("unexpected candidate: %+v", c)
	}
}

func TestRelocatedGo_EvidenceRefusals(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"missing trim.txt": func(t *testing.T, dir string) {
			writeContent(t, filepath.Join(dir, "README"), goReadmeContent)
			mkdirs(t, filepath.Join(dir, "0a"))
		},
		"trim.txt is a directory": func(t *testing.T, dir string) {
			writeContent(t, filepath.Join(dir, "README"), goReadmeContent)
			mkdirs(t, filepath.Join(dir, "trim.txt"), filepath.Join(dir, "0a"))
		},
		"no shard directory": func(t *testing.T, dir string) {
			writeContent(t, filepath.Join(dir, "README"), goReadmeContent)
			writeRegular(t, filepath.Join(dir, "trim.txt"))
			mkdirs(t, filepath.Join(dir, "zz"))
		},
		"README not Go's": func(t *testing.T, dir string) {
			writeContent(t, filepath.Join(dir, "README"), "my notes\n")
			writeRegular(t, filepath.Join(dir, "trim.txt"))
			mkdirs(t, filepath.Join(dir, "0a"))
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			isolateLiveCaches(t)
			root := t.TempDir()
			dir := filepath.Join(root, "fb-prov-go-cache")
			build(t, dir)
			assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
		})
	}
	if runtime.GOOS != "windows" {
		t.Run("trim.txt is a symlink", func(t *testing.T) {
			isolateLiveCaches(t)
			root := t.TempDir()
			dir := filepath.Join(root, "fb-prov-go-cache")
			writeContent(t, filepath.Join(dir, "README"), goReadmeContent)
			mkdirs(t, filepath.Join(dir, "0a"))
			writeRegular(t, filepath.Join(root, "elsewhere"))
			if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(dir, "trim.txt")); err != nil {
				t.Fatal(err)
			}
			assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
		})
	}
}

// The live default build cache under the real home stays diagnostic-only.
func TestRelocatedGo_LiveHomeCacheExcluded(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	notSharedTemp(t)
	setHome(t, home)
	t.Setenv("GOCACHE", "")
	live := relocatedGo(t, filepath.Join(home, "Library", "Caches", "go-build"))
	linux := relocatedGo(t, filepath.Join(home, ".cache", "go-build"))
	other := relocatedGo(t, filepath.Join(root, "review-home", "Library", "Caches", "go-build"))
	jobs := r4bJobs(t, builtInConfig(root))
	assertAbsent(t, jobs, live)
	assertAbsent(t, jobs, linux)
	assertSignature(t, jobs, other, relocatedGoID)
}

// The directory GOCACHE names is excluded, whatever its name.
func TestRelocatedGo_GOCACHEExcluded(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	live := relocatedGo(t, filepath.Join(root, "my-go-cache"))
	t.Setenv("GOCACHE", live)
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), live)
}

// A .gocache under go.mod keeps the repo-local id even when it is complete
// enough to satisfy the relocated signature.
func TestRelocatedGo_RepoLocalKeepsItsID(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	writeRegular(t, filepath.Join(root, "repo", "go.mod"))
	dir := relocatedGo(t, filepath.Join(root, "repo", ".gocache"))
	assertSignature(t, r4bJobs(t, builtInConfig(root)), dir, "development.go.repo-local-gocache")
}

// The signature floor holds with no configured min_age: a cache written in
// the last hour is withheld for age even when its root directory is old.
func TestRelocated_FloorWithholdsRecentWritesByDefault(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	cargo := relocatedCargo(t, filepath.Join(root, "scratch-target"))
	gocache := relocatedGo(t, filepath.Join(root, "scratch-gocache"))
	ageTree(t, cargo, 48*time.Hour)
	ageTree(t, gocache, 48*time.Hour)
	writeRegular(t, filepath.Join(cargo, "debug", "build", "x", "fresh"))
	writeRegular(t, filepath.Join(gocache, "0a", "fresh-d"))
	old := time.Now().Add(-48 * time.Hour)
	for _, d := range []string{cargo, gocache} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}
	jobs := r4bJobs(t, builtInConfig(root))
	for _, d := range []string{cargo, gocache} {
		c, ok := findCandidate(jobs, d)
		if !ok || c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonAge {
			t.Fatalf("%s must be withheld for age: %+v ok=%v", d, c, ok)
		}
	}
}

// Project or VCS markers, or an entry outside Go's cache layout, refuse the
// match even with complete marker evidence.
func TestRelocated_ProjectMarkersRefuse(t *testing.T) {
	for _, extra := range []string{".git", "go.mod", "Cargo.toml", "src", "notes.txt"} {
		t.Run("go+"+extra, func(t *testing.T) {
			isolateLiveCaches(t)
			root := t.TempDir()
			dir := relocatedGo(t, filepath.Join(root, "seeded"))
			writeRegular(t, filepath.Join(dir, extra))
			ageTree(t, dir, 2*time.Hour)
			assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
		})
	}
	for _, extra := range []string{".git", "Cargo.toml", "go.mod", "src"} {
		t.Run("cargo+"+extra, func(t *testing.T) {
			isolateLiveCaches(t)
			root := t.TempDir()
			dir := relocatedCargo(t, filepath.Join(root, "seeded"))
			mkdirs(t, filepath.Join(dir, extra))
			ageTree(t, dir, 2*time.Hour)
			assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
		})
	}
	t.Run("go real layout extras admitted", func(t *testing.T) {
		isolateLiveCaches(t)
		root := t.TempDir()
		dir := relocatedGo(t, filepath.Join(root, "real"))
		writeRegular(t, filepath.Join(dir, "testexpire.txt"))
		mkdirs(t, filepath.Join(dir, "fuzz"), filepath.Join(dir, "ff"))
		assertSignature(t, r4bJobs(t, builtInConfig(root)), dir, relocatedGoID)
	})
}

// A marker-only signature never matches the home directory or a parent of it.
func TestRelocated_HomeAndAncestorsRefused(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "users", "me")
	notSharedTemp(t)
	setHome(t, home)
	t.Setenv("GOCACHE", "")
	relocatedCargo(t, home)
	relocatedGo(t, filepath.Join(root, "users"))
	jobs := r4bJobs(t, builtInConfig(root))
	assertAbsent(t, jobs, home)
	assertAbsent(t, jobs, filepath.Join(root, "users"))
}

// --- overrides cannot loosen -----------------------------------------------

// A user overlay reusing either relocated id cannot drop marker lines, the
// required child files, or the live-cache exclusions.
func TestRelocated_ReusedIDOverlayKeepsEvidence(t *testing.T) {
	t.Run("cargo marker line 2", func(t *testing.T) {
		isolateLiveCaches(t)
		root := t.TempDir()
		dir := filepath.Join(root, "scratch-target")
		writeContent(t, filepath.Join(dir, "CACHEDIR.TAG"),
			"Signature: 8a477f597d28d172789f06886806bc55\n# created by othertool\n")
		mkdirs(t, filepath.Join(dir, "debug"))
		cfg := builtInConfig(root)
		cfg.Signatures = config.SignatureCatalog{"development": {"rust": {"cargo-target-relocated": {
			CandidatePatterns: []string{"**/*-target"},
			ContentMarker:     &config.ContentMarker{Path: "CACHEDIR.TAG", FirstLine: "Signature: 8a477f597d28d172789f06886806bc55"},
			AnyChildPaths:     []string{"debug"},
			SafeToPrune:       true,
		}}}}
		assertAbsent(t, r4bJobs(t, cfg), dir)
	})
	t.Run("go trim.txt and exclusions", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		notSharedTemp(t)
		setHome(t, home)
		t.Setenv("GOCACHE", "")
		live := relocatedGo(t, filepath.Join(home, "Library", "Caches", "go-build"))
		noTrim := filepath.Join(root, "scratch-go")
		writeContent(t, filepath.Join(noTrim, "README"), goReadmeContent)
		mkdirs(t, filepath.Join(noTrim, "0a"))
		cfg := builtInConfig(root)
		cfg.Signatures = config.SignatureCatalog{"development": {"go": {"build-cache-relocated": {
			ContentMarker: &config.ContentMarker{Path: "README", FirstLine: config.GoBuildCacheReadmeFirstLine},
			SafeToPrune:   true,
		}}}}
		jobs := r4bJobs(t, cfg)
		assertAbsent(t, jobs, live)
		assertAbsent(t, jobs, noTrim)
	})
}

// A new-id signature with no patterns and no marker never matches anything.
func TestMarkerDiscovery_RequiresAMarker(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := filepath.Join(root, "anything")
	mkdirs(t, filepath.Join(dir, "debug"))
	cfg := &config.Config{
		Version: 1,
		Signatures: config.SignatureCatalog{"development": {"custom": {"loose": {
			AnyChildPaths: []string{"debug"}, SafeToPrune: true,
		}}}},
		Paths: []config.PathProfile{{Path: root, MaxDepth: 12, Targets: []config.Target{{Signature: "development.custom.loose"}}}},
	}
	if got := r4bCandidates(t, cfg); len(got) != 0 {
		t.Fatalf("a pattern-less signature without a marker must match nothing: %v", got)
	}
}

func TestContentMarker_ExpectedLines(t *testing.T) {
	cases := []struct {
		name string
		m    config.ContentMarker
		want int
	}{
		{"first line only", config.ContentMarker{FirstLine: "a"}, 1},
		{"lines only", config.ContentMarker{Lines: []string{"a", "b"}}, 2},
		{"consistent both", config.ContentMarker{FirstLine: "a", Lines: []string{"a", "b"}}, 2},
		{"conflicting both", config.ContentMarker{FirstLine: "x", Lines: []string{"a", "b"}}, 0},
		{"empty", config.ContentMarker{}, 0},
		{"empty line", config.ContentMarker{Lines: []string{"a", ""}}, 0},
	}
	for _, c := range cases {
		if got := len(c.m.ExpectedLines()); got != c.want {
			t.Errorf("%s: %d expected lines, want %d", c.name, got, c.want)
		}
	}
}

// Exact-plan replay applies the marker signature's floor even when the
// operator opts out of the handoff floor (--min-age none).
func TestRelocated_ExactPlanKeepsSignatureFloor(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedCargo(t, filepath.Join(root, "scratch-target"))
	plan, err := PlanFromExact(context.Background(), ExactPlanInput{
		RequestedMinAge: RequestedMinAgeNone,
		AnalysisRoot:    root,
		Entries: []ExactEntry{{Path: dir, Signature: relocatedCargoID,
			EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := assertSignature(t, plan.Candidates, dir, relocatedCargoID)
	if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonAge {
		t.Fatalf("a fresh relocated target must be withheld for age on replay: %+v", c)
	}
}

// Each live-cache source excludes on its own (they overlap on some
// platforms, so end-to-end fixtures cannot tell them apart).
func TestExcludedLiveCache_EachSourceAlone(t *testing.T) {
	t.Run("user cache dir", func(t *testing.T) {
		isolateLiveCaches(t)
		t.Setenv("XDG_CACHE_HOME", "")
		t.Setenv("LocalAppData", "")
		dir, err := os.UserCacheDir()
		if err != nil {
			t.Skip("no user cache dir on this platform")
		}
		live := relocatedGo(t, filepath.Join(dir, "go-build"))
		if !excludedLiveCache(live, config.Signature{ExcludeLiveGoCache: true}) {
			t.Fatal("the user cache dir's go-build must be excluded")
		}
	})
	t.Run("home path", func(t *testing.T) {
		isolateLiveCaches(t)
		home, _ := os.UserHomeDir()
		live := relocatedGo(t, filepath.Join(home, "Library", "Caches", "go-build"))
		if !excludedLiveCache(live, config.Signature{ExcludedHomePaths: []string{"Library/Caches/go-build"}}) {
			t.Fatal("a home-relative exclusion must exclude")
		}
	})
	t.Run("unknown home fails closed", func(t *testing.T) {
		root := t.TempDir()
		notSharedTemp(t)
		setHome(t, "")
		dir := relocatedGo(t, filepath.Join(root, "c"))
		if !excludedLiveCache(dir, config.Signature{ExcludedHomePaths: []string{".cache/go-build"}}) {
			t.Fatal("an unresolvable home must exclude (fail closed)")
		}
	})
	t.Run("unrelated cache not excluded", func(t *testing.T) {
		isolateLiveCaches(t)
		dir := relocatedGo(t, filepath.Join(t.TempDir(), "scratch"))
		if excludedLiveCache(dir, config.Signature{ExcludedHomePaths: []string{"Library/Caches/go-build"}, ExcludeLiveGoCache: true}) {
			t.Fatal("an unrelated cache must not be excluded")
		}
	})
}

// With no resolvable home, a marker-only signature matches nothing, even
// one with no home exclusions of its own.
func TestRelocated_UnknownHomeRefusesMarkerSignatures(t *testing.T) {
	root := t.TempDir()
	notSharedTemp(t)
	setHome(t, "")
	t.Setenv("GOCACHE", "")
	dir := relocatedCargo(t, filepath.Join(root, "scratch-target"))
	ageTree(t, dir, 2*time.Hour)
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
}

// Under shared temp a relocated cache is never a candidate in any state, the
// plan says why, and an exact-plan entry for it is refused at replay.
func TestRelocated_SharedTempIsDiagnosticOnly(t *testing.T) {
	isolateLiveCaches(t)
	shared := t.TempDir()
	withSharedTemp(t, shared)
	cargo := relocatedCargo(t, filepath.Join(shared, "rsfulmen-pr30-devrev-target"))
	gocache := relocatedGo(t, filepath.Join(shared, "fb-prov-go-cache"))
	ageTree(t, cargo, 2*time.Hour)
	ageTree(t, gocache, 2*time.Hour)

	plan, err := BuildPrunePlan(context.Background(), builtInConfig(shared))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{cargo, gocache} {
		assertAbsent(t, plan.Candidates, d)
		warned := false
		for _, w := range plan.Warnings {
			if strings.Contains(w.Error(), d) && strings.Contains(w.Error(), "shared temp") {
				warned = true
			}
		}
		if !warned {
			t.Fatalf("no shared-temp warning for %s: %v", d, plan.Warnings)
		}
	}

	exact, err := PlanFromExact(context.Background(), ExactPlanInput{
		RequestedMinAge: RequestedMinAgeNone,
		AnalysisRoot:    shared,
		Entries: []ExactEntry{{Path: cargo, Signature: relocatedCargoID,
			EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone}},
	})
	if err == nil {
		assertAbsent(t, exact.Candidates, cargo)
	}
}

// A user target list that names the relocated id first still leaves a
// conventional target/ with the conventional id.
func TestRelocated_UserTargetOrderKeepsConventionalID(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	writeRegular(t, filepath.Join(root, "repo", "Cargo.toml"))
	dir := relocatedCargo(t, filepath.Join(root, "repo", "target"))
	cfg := &config.Config{Version: 1, Paths: []config.PathProfile{{Path: root, MaxDepth: 12, Targets: []config.Target{
		{Signature: relocatedCargoID}, {Signature: "development.rust.cargo-target"},
	}}}}
	assertSignature(t, r4bJobs(t, cfg), dir, "development.rust.cargo-target")
}
