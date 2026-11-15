package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Round-2 review regressions for the relocated cache signatures.

// realSharedTemp restores the production shared-temp rules for this test,
// after isolateLiveCaches/setHome switched them off for /tmp fixtures.
func realSharedTemp(t *testing.T) {
	t.Helper()
	saved, savedSticky := sharedTempPaths, stickyAncestorCheck
	sharedTempPaths = []string{"/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp"}
	stickyAncestorCheck = true
	t.Cleanup(func() { sharedTempPaths, stickyAncestorCheck = saved, savedSticky })
}

func darwinPrivateTmp(t *testing.T, prefix string) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("darwin /private/tmp spellings")
	}
	base, err := os.MkdirTemp("/private/tmp", prefix)
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	return base
}

// B1: a workspace holding project repos one level down is never one cache.
func TestRelocatedRegress2_B1_CargoWorkspaceOfRepos(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	ws := filepath.Join(root, "work")
	writeRegular(t, filepath.Join(ws, "repoA", ".git", "HEAD"))
	writeRegular(t, filepath.Join(ws, "repoA", "Cargo.toml"))
	writeRegular(t, filepath.Join(ws, "notes", "important.md"))
	relocatedCargo(t, ws)
	ageTree(t, ws, 48*time.Hour)
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), ws)
}

// A dashed directory name (target-triple shaped) still gets the inner check.
func TestRelocatedRegress2_B1_TripleShapedRepoRefused(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	ws := filepath.Join(root, "work")
	writeRegular(t, filepath.Join(ws, "repo-a", ".git", "HEAD"))
	relocatedCargo(t, ws)
	ageTree(t, ws, 48*time.Hour)
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), ws)
}

// Unknown top-level files refuse (a Python project tagged by Cargo).
func TestRelocatedRegress2_PythonProjectAsTargetDir(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedCargo(t, filepath.Join(root, "pyproj"))
	writeRegular(t, filepath.Join(dir, "pyproject.toml"))
	writeRegular(t, filepath.Join(dir, "app", "main.py"))
	ageTree(t, dir, 72*time.Hour)
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
}

// Real target shapes stay prunable: profile dirs, a triple holding a
// profile, custom profiles (deps/.fingerprint), rustdoc's doc/src, cargo
// package output.
func TestRelocatedRegress2_RealTargetShapesAdmitted(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedCargo(t, filepath.Join(root, "yamlprims-target"))
	mkdirs(t,
		filepath.Join(dir, "aarch64-apple-darwin", "release", ".fingerprint"),
		filepath.Join(dir, "secrev-r5", ".fingerprint"),
		filepath.Join(dir, "doc", "src", "crate"),
		filepath.Join(dir, "tmp"),
		filepath.Join(dir, "flycheck0"),
	)
	writeRegular(t, filepath.Join(dir, "package", "crate-0.1.0", "Cargo.toml"))
	writeRegular(t, filepath.Join(dir, ".cargo-lock"))
	ageTree(t, dir, 48*time.Hour)
	c := assertSignature(t, r4bJobs(t, builtInConfig(root)), dir, relocatedCargoID)
	if c.State != CandidateStatePrunable {
		t.Fatalf("real target shape should be prunable: %+v", c)
	}
}

// A directory Cargo did not create (tool output, user data) leaves the
// layout unrecognised: visible, withheld for layout, never prunable.
func TestRelocatedRegress2_UnrecognisedDirWithheldForLayout(t *testing.T) {
	for _, extra := range []string{"napi-rs", "Photos", "my-projects/app", "aarch64-apple-darwin/notes"} {
		t.Run(extra, func(t *testing.T) {
			isolateLiveCaches(t)
			root := t.TempDir()
			dir := relocatedCargo(t, filepath.Join(root, "reused-target"))
			writeRegular(t, filepath.Join(dir, filepath.FromSlash(extra), "data.bin"))
			ageTree(t, dir, 48*time.Hour)
			c := assertSignature(t, r4bJobs(t, builtInConfig(root)), dir, relocatedCargoID)
			if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonLayout {
				t.Fatalf("unrecognised layout must withhold for layout: %+v", c)
			}
		})
	}
}

// The layout gate also holds on exact-plan replay.
func TestRelocatedRegress2_ExactPlanWithholdsLayout(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedCargo(t, filepath.Join(root, "reused-target"))
	writeRegular(t, filepath.Join(dir, "Photos", "a.jpg"))
	ageTree(t, dir, 48*time.Hour)
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
	if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonLayout {
		t.Fatalf("exact replay must withhold an unrecognised layout: %+v", c)
	}
}

// Files never match a pattern: hyphenated or flycheck-like names refuse.
func TestRelocatedRegress2_PatternShapedFilesRefused(t *testing.T) {
	for _, f := range []string{"tax-return-2025.pdf", "flycheck-notes.txt", "debug"} {
		t.Run(f, func(t *testing.T) {
			isolateLiveCaches(t)
			root := t.TempDir()
			dir := relocatedCargo(t, filepath.Join(root, "t"))
			if f == "debug" {
				f = "release" // a file named like a known dir
			}
			writeRegular(t, filepath.Join(dir, f))
			ageTree(t, dir, 48*time.Hour)
			assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
		})
	}
}

// A project hidden two levels down in Go's fuzz/ refuses the Go match.
func TestRelocatedRegress2_GoFuzzHidesRepoRefused(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedGo(t, filepath.Join(root, "gc"))
	writeRegular(t, filepath.Join(dir, "fuzz", "repo", ".git", "HEAD"))
	writeRegular(t, filepath.Join(dir, "fuzz", "repo", "go.mod"))
	ageTree(t, dir, 48*time.Hour)
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
}

// A world-writable ancestor without the sticky bit is shared too.
func TestRelocatedRegress2_WorldWritableNonStickyIsShared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix modes")
	}
	isolateLiveCaches(t)
	realSharedTemp(t)
	sharedTempPaths = nil
	base := t.TempDir()
	open := filepath.Join(base, "open")
	mkdirs(t, open)
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	dir := relocatedCargo(t, filepath.Join(open, "x-target"))
	if !underSharedTemp(dir) {
		t.Fatal("a world-writable ancestor must make the path shared")
	}
}

// B2: another spelling of a home ancestor is still refused.
func TestRelocatedRegress2_B2_HomeAncestorOtherSpelling(t *testing.T) {
	isolateLiveCaches(t)
	base := t.TempDir()
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil || resolved == base {
		t.Skip("temp dir has no alternate spelling here")
	}
	notSharedTemp(t)
	setHome(t, filepath.Join(base, "users", "someone"))
	writeRegular(t, filepath.Join(base, "users", "someone", ".zshrc"))
	users := filepath.Join(resolved, "users")
	relocatedCargo(t, users)
	ageTree(t, users, 48*time.Hour)
	assertAbsent(t, r4bJobs(t, builtInConfig(resolved)), users)
}

// B3: a relative HOME fails closed.
func TestRelocatedRegress2_B3_RelativeHomeFailsClosed(t *testing.T) {
	isolateLiveCaches(t)
	t.Setenv("HOME", "relative/home")
	t.Setenv("USERPROFILE", "relative/home")
	if !isHomeOrAncestor(t.TempDir()) {
		t.Fatal("a relative HOME must count as unknown (fail closed)")
	}
}

// B4: case and firmlink spellings of /private/tmp are shared temp.
func TestRelocatedRegress2_B4_SharedTempSpellings(t *testing.T) {
	realSharedTemp(t)
	base := darwinPrivateTmp(t, "fdl-spanwit-sp-")
	name := strings.TrimPrefix(base, "/private/tmp/")
	for _, p := range []string{
		"/PRIVATE/TMP/" + name,
		"/private/TMP/" + name,
		"/System/Volumes/Data/private/tmp/" + name,
		"/tmp/" + name,
	} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if !underSharedTemp(p) {
			t.Errorf("underSharedTemp(%q) = false", p)
		}
	}
}

// B4b: end to end, a planted cache under /private/tmp scanned through other
// spellings is never a candidate.
func TestRelocatedRegress2_B4b_SharedTempSpellingsEndToEnd(t *testing.T) {
	isolateLiveCaches(t)
	realSharedTemp(t)
	base := darwinPrivateTmp(t, "fdl-spanwit-shared-")
	relocatedCargo(t, filepath.Join(base, "x-target"))
	ageTree(t, base, 72*time.Hour)
	name := filepath.Base(base)
	for _, root := range []string{
		filepath.Join("/private/TMP", name),
		filepath.Join("/System/Volumes/Data/private/tmp", name),
		filepath.Join("/tmp", name),
	} {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		for _, c := range r4bJobs(t, builtInConfig(root)) {
			if strings.HasSuffix(c.Path, "x-target") {
				t.Errorf("%s: shared-temp relocated cache is a candidate (%s)", root, c.State)
			}
		}
	}
}

// Any sticky, world-writable ancestor makes a path shared, on any platform.
func TestRelocatedRegress2_StickyWorldWritableAncestorIsShared(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix modes")
	}
	isolateLiveCaches(t)
	realSharedTemp(t)
	sharedTempPaths = nil // identity roots off: the mode alone must decide
	base := t.TempDir()
	shm := filepath.Join(base, "shm")
	mkdirs(t, shm)
	if err := os.Chmod(shm, 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	dir := relocatedCargo(t, filepath.Join(shm, "x-target"))
	ageTree(t, dir, 48*time.Hour)
	if !underSharedTemp(dir) {
		t.Fatal("a path under a sticky world-writable directory must be shared")
	}
	assertAbsent(t, r4bJobs(t, builtInConfig(shm)), dir)
}

// An unknown top-level file refuses a Cargo match, even with no manifest.
func TestRelocatedRegress2_UnknownTopLevelFileRefused(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedCargo(t, filepath.Join(root, "work"))
	writeRegular(t, filepath.Join(dir, "notes.txt"))
	ageTree(t, dir, 48*time.Hour)
	assertAbsent(t, r4bJobs(t, builtInConfig(root)), dir)
}

// A path whose identity cannot be read counts as shared (fail closed).
func TestRelocatedRegress2_UnstattablePathIsShared(t *testing.T) {
	notSharedTemp(t)
	if !underSharedTemp(filepath.Join(t.TempDir(), "missing", "x")) {
		t.Fatal("an unstattable path must count as shared temp")
	}
}

// A deps/ folder is not a profile marker: projects and user folders have one.
func TestRelocatedRegress2_DepsIsNotAProfileMarker(t *testing.T) {
	cases := map[string][]string{
		"workspace with deps project": {"projects/myapp/.git/HEAD", "projects/myapp/mix.exs", "projects/myapp/deps/x"},
		"user folder with deps":       {"Thesis/deps/figures.zip", "Thesis/chapter1.tex"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			isolateLiveCaches(t)
			root := t.TempDir()
			dir := relocatedCargo(t, filepath.Join(root, "reused-target"))
			for _, f := range files {
				writeRegular(t, filepath.Join(dir, filepath.FromSlash(f)))
			}
			ageTree(t, dir, 48*time.Hour)
			assertNeverPrunable(t, r4bJobs(t, builtInConfig(root)), dir)
		})
	}
}

// A triple-shaped child that supplies the marker must itself hold no project.
func TestRelocatedRegress2_NestedMarkerChildMustBeClean(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedCargo(t, filepath.Join(root, "reused-target"))
	mkdirs(t, filepath.Join(dir, "work", "app", ".fingerprint"))
	writeRegular(t, filepath.Join(dir, "work", "app", "go.mod"))
	ageTree(t, dir, 48*time.Hour)
	assertNeverPrunable(t, r4bJobs(t, builtInConfig(root)), dir)
}

// debug/ and release/ must hold .fingerprint: a known name alone does not
// vouch for user data placed there.
func TestRelocatedRegress2_KnownProfileDirNeedsFingerprint(t *testing.T) {
	isolateLiveCaches(t)
	root := t.TempDir()
	dir := relocatedCargo(t, filepath.Join(root, "reused-target"))
	writeRegular(t, filepath.Join(dir, "release", "Photos", "img001.jpg"))
	ageTree(t, dir, 48*time.Hour)
	c := assertSignature(t, r4bJobs(t, builtInConfig(root)), dir, relocatedCargoID)
	if c.State != CandidateStateWithheld || c.WithheldReason != WithheldReasonLayout {
		t.Fatalf("release/ without .fingerprint must withhold for layout: %+v", c)
	}
}
