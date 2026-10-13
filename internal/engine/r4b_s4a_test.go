package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/config"
)

// S4a fixtures beyond the S3 set: exact-plan floor bypasses (C1), user
// override safety (C2, C2b), sibling vouching (C3), marker edge cases (C4)
// and sibling-not-self. Built-in signatures only (no test catalog), so these
// exercise the shipped rules.

func exactFor(t *testing.T, root string, e ExactEntry) []PruneCandidate {
	t.Helper()
	plan, err := PlanFromExact(context.Background(), ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{e}})
	if err != nil {
		t.Fatalf("PlanFromExact: %v", err)
	}
	return plan.Candidates
}

// ageTree sets path and everything beneath it to the given age. An age floor
// reads the newest activity in the subtree, so a file inside must age too.
func ageTree(t *testing.T, path string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, when, when)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func builtInGrype(t *testing.T) (root, residue string) {
	t.Helper()
	root = t.TempDir()
	_, _, residue = grypeTree(t, root)
	return root, residue
}

// C1: a signature-backed exact entry carrying effective_min_age "none" for a
// fresh download revalidates as withheld for age: the floor comes from the
// catalog, not the plan.
func TestS4aExact_SignatureEntryCannotLowerFloor(t *testing.T) {
	root, residue := builtInGrype(t)
	ageTree(t, residue, 0)
	got := exactFor(t, root, ExactEntry{Path: residue, Signature: r4bGrypeSig,
		EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none"})
	if len(got) != 1 || got[0].State != CandidateStateWithheld || got[0].WithheldReason != WithheldReasonAge {
		t.Fatalf("fresh download via exact plan must be withheld for age, got %+v", got)
	}
}

// C1 control: an old download revalidates as prunable through the exact plan,
// proving path-anchored patterns revalidate against the analysis root.
func TestS4aExact_OldDownloadRevalidatesPrunable(t *testing.T) {
	root, residue := builtInGrype(t)
	ageTree(t, residue, 48*time.Hour)
	got := exactFor(t, root, ExactEntry{Path: residue, Signature: r4bGrypeSig,
		EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none"})
	if len(got) != 1 || got[0].State != CandidateStatePrunable {
		t.Fatalf("an old download should revalidate prunable, got %+v", got)
	}
}

// C1: a pattern-only exact entry whose path matches a floored signature is
// refused, so omitting the signature cannot drop the floor or the evidence.
func TestS4aExact_PatternOnlyEntryRefused(t *testing.T) {
	root, residue := builtInGrype(t)
	ageTree(t, residue, 48*time.Hour)
	plan, err := PlanFromExact(context.Background(), ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{{
		Path: residue, Pattern: "**/grype/db/grype-db-download*",
		EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("pattern-only entry on a floored signature must be refused, got %+v", plan.Candidates)
	}
	refused := false
	for _, w := range plan.Warnings {
		if strings.Contains(w.Error(), "age floor") {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("expected a refusal warning, got %v", plan.Warnings)
	}
}

// C2: a user config that reuses the built-in grype id without a floor still
// withholds a fresh download.
func TestS4a_UserOverrideCannotDropFloor(t *testing.T) {
	root, residue := builtInGrype(t)
	ageTree(t, residue, 0)
	cfg := &config.Config{Version: 1,
		Signatures: config.SignatureCatalog{"development": {"security": {"grype-db-download": {
			CandidatePatterns: []string{"**/grype/db/grype-db-download*"},
			AnyChildPaths:     []string{"vulnerability.db"},
			SafeToPrune:       true,
		}}}},
		Paths: []config.PathProfile{{Path: root, MaxDepth: 12, Targets: []config.Target{{Signature: r4bGrypeSig}}}},
	}
	jobs := r4bJobs(t, cfg)
	assertNeverPrunable(t, jobs, residue)
}

// C2b: a user override of the gocache id without a content marker still
// refuses a name-collision .gocache.
func TestS4a_UserOverrideCannotDropEvidence(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	writeRegular(t, filepath.Join(repo, "go.mod"))
	cache := filepath.Join(repo, ".gocache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "README"), []byte("my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Version: 1,
		Signatures: config.SignatureCatalog{"development": {"go": {"repo-local-gocache": {
			CandidatePatterns: []string{"**/.gocache"},
			AnyChildPaths:     []string{"README"},
			SafeToPrune:       true,
		}}}},
		Paths: []config.PathProfile{{Path: root, MaxDepth: 12, Targets: []config.Target{{Signature: r4bGocacheSig}}}},
	}
	assertAbsent(t, r4bJobs(t, cfg), cache)
}

// C3: two downloads, each with import.json and vulnerability.db, and no
// numeric live db/<N>: neither vouches for the other.
func TestS4a_DownloadsCannotVouchForEachOther(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "Library", "Caches", "grype", "db")
	a := filepath.Join(db, "grype-db-download1")
	b := filepath.Join(db, "grype-db-download2")
	for _, d := range []string{a, b} {
		writeRegular(t, filepath.Join(d, "vulnerability.db"))
		writeRegular(t, filepath.Join(d, "import.json"))
		ageTree(t, d, 48*time.Hour)
	}
	cfg := &config.Config{Version: 1, Paths: []config.PathProfile{{Path: root, MaxDepth: 12,
		Targets: []config.Target{{Signature: r4bGrypeSig}}}}}
	jobs := r4bJobs(t, cfg)
	assertAbsent(t, jobs, a)
	assertAbsent(t, jobs, b)
}

// C3: the parent scan is capped; an oversized parent fails sibling evidence
// closed rather than scanning without bound.
func TestS4a_SiblingScanIsCapped(t *testing.T) {
	root, residue := builtInGrype(t)
	ageTree(t, residue, 48*time.Hour)
	db := filepath.Dir(residue)
	// Fill the parent past the scan limit with plain files.
	for i := 0; i <= siblingScanLimit; i++ {
		writeRegular(t, filepath.Join(db, fmt.Sprintf("pad-%04d", i)))
	}
	cfg := &config.Config{Version: 1, Paths: []config.PathProfile{{Path: root, MaxDepth: 12,
		Targets: []config.Target{{Signature: r4bGrypeSig}}}}}
	assertAbsent(t, r4bJobs(t, cfg), residue)
}

// Sibling is never the candidate: a lone download that is itself numeric-named
// cannot vouch for itself.
func TestS4a_SiblingIsNeverSelf(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "Library", "Caches", "grype", "db")
	self := filepath.Join(db, "grype-db-download7")
	writeRegular(t, filepath.Join(self, "vulnerability.db"))
	writeRegular(t, filepath.Join(self, "import.json"))
	ageTree(t, self, 48*time.Hour)
	if _, ok := siblingEvidence(root, self, config.Signature{
		CandidatePatterns: []string{"**/grype/db/grype-db-download*"},
		SiblingEvidence:   []string{"*/import.json", "*/vulnerability.db"},
	}); ok {
		t.Fatal("a candidate must never be its own sibling")
	}
}

// C4: the marker must be a regular file, not a symlink, within the read cap,
// with a line end inside the cap, and an exact first line.
func TestS4a_ContentMarkerEdgeCases(t *testing.T) {
	marker := &config.ContentMarker{Path: "README", FirstLine: config.GoBuildCacheReadmeFirstLine}
	good := config.GoBuildCacheReadmeFirstLine + "\nRun go clean -cache\n"
	cases := map[string]func(dir string){
		"exact first line":          func(d string) { write(t, filepath.Join(d, "README"), good) },
		"crlf first line":           func(d string) { write(t, filepath.Join(d, "README"), config.GoBuildCacheReadmeFirstLine+"\r\nx\n") },
		"wrong first line":          func(d string) { write(t, filepath.Join(d, "README"), "my notes\n") },
		"leading space":             func(d string) { write(t, filepath.Join(d, "README"), " "+good) },
		"no newline within the cap": func(d string) { write(t, filepath.Join(d, "README"), strings.Repeat("a", markerReadLimit+10)) },
		"marker is a directory":     func(d string) { mkdirs(t, filepath.Join(d, "README")) },
	}
	want := map[string]bool{"exact first line": true, "crlf first line": true}
	for name, setup := range cases {
		dir := t.TempDir()
		setup(dir)
		if _, ok := contentMarkerEvidence(dir, marker); ok != want[name] {
			t.Errorf("%s: got %v, want %v", name, ok, want[name])
		}
	}
	if os.Getenv("GOOS") != "windows" {
		dir := t.TempDir()
		real := filepath.Join(t.TempDir(), "README")
		write(t, real, good)
		if err := os.Symlink(real, filepath.Join(dir, "README")); err == nil {
			if _, ok := contentMarkerEvidence(dir, marker); ok {
				t.Error("a symlinked marker must not be evidence")
			}
		}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// C3, isolated: a sibling directory that does not match the candidate pattern
// but is not the numeric live-db shape (a "backup" copy) never vouches. Only
// the live-db shape check rejects it.
func TestS4a_NonNumericSiblingNeverVouches(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "Library", "Caches", "grype", "db")
	residue := filepath.Join(db, "grype-db-download3")
	writeRegular(t, filepath.Join(residue, "vulnerability.db"))
	backup := filepath.Join(db, "backup")
	writeRegular(t, filepath.Join(backup, "vulnerability.db"))
	writeRegular(t, filepath.Join(backup, "import.json"))
	ageTree(t, residue, 48*time.Hour)
	if _, ok := siblingEvidence(root, residue, config.Signature{
		CandidatePatterns: []string{"**/grype/db/grype-db-download*"},
		SiblingEvidence:   []string{"*/import.json", "*/vulnerability.db"},
	}); ok {
		t.Fatal("a non-numeric sibling must not vouch, even when it does not match the candidate pattern")
	}
}

// Sibling-not-self, isolated: under a signature whose pattern matches numeric
// directories, a numeric candidate holding the evidence files must not vouch
// for itself. Only the self check rejects it.
func TestS4a_NumericCandidateCannotVouchForItself(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "store")
	self := filepath.Join(parent, "42")
	writeRegular(t, filepath.Join(self, "import.json"))
	writeRegular(t, filepath.Join(self, "vulnerability.db"))
	if _, ok := siblingEvidence(root, self, config.Signature{
		CandidatePatterns: []string{"**/store/other*"},
		SiblingEvidence:   []string{"*/import.json", "*/vulnerability.db"},
	}); ok {
		t.Fatal("a numeric candidate must never be its own sibling")
	}
}

// C2b, grype: a user override of the grype id without sibling_evidence still
// refuses a download with no numeric live db/<N>.
func TestS4a_UserOverrideCannotDropSiblingEvidence(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "Library", "Caches", "grype", "db")
	orphan := filepath.Join(db, "grype-db-download5")
	writeRegular(t, filepath.Join(orphan, "vulnerability.db"))
	ageTree(t, orphan, 48*time.Hour)
	cfg := &config.Config{Version: 1,
		Signatures: config.SignatureCatalog{"development": {"security": {"grype-db-download": {
			CandidatePatterns: []string{"**/grype/db/grype-db-download*"},
			AnyChildPaths:     []string{"vulnerability.db"},
			SafeToPrune:       true,
		}}}},
		Paths: []config.PathProfile{{Path: root, MaxDepth: 12, Targets: []config.Target{{Signature: r4bGrypeSig}}}},
	}
	assertAbsent(t, r4bJobs(t, cfg), orphan)
}

// B1: an in-flight download. The directory is old but vulnerability.db is
// being written now. A directory's mtime does not move when a file inside is
// written, so the floor must read descendant activity: the download is
// present, withheld for age, and never prunable, on both paths.
func TestS4a_InFlightDownloadWithheldOnBothPaths(t *testing.T) {
	root, residue := builtInGrype(t)
	ageTree(t, residue, 3*time.Hour)
	now := time.Now()
	if err := os.Chtimes(filepath.Join(residue, "vulnerability.db"), now, now); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Version: 1, Paths: []config.PathProfile{{Path: root, MaxDepth: 12,
		Targets: []config.Target{{Signature: r4bGrypeSig}}}}}
	check := func(where string, jobs []PruneCandidate) {
		t.Helper()
		matches := 0
		for _, j := range jobs {
			if j.Path != residue {
				continue
			}
			matches++
			if j.State != CandidateStateWithheld || j.WithheldReason != WithheldReasonAge {
				t.Fatalf("%s: in-flight download must be withheld for age, got %+v", where, j)
			}
		}
		if matches != 1 {
			t.Fatalf("%s: in-flight download must be present exactly once (got %d)", where, matches)
		}
	}
	check("build", r4bJobs(t, cfg))
	check("exact", exactFor(t, root, ExactEntry{Path: residue, Signature: r4bGrypeSig,
		EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none"}))
}

// B1: an activity read that cannot complete (an unreadable subdirectory)
// leaves the newest time unknown, so the floor withholds.
func TestS4a_IncompleteActivityReadWithholds(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	root, residue := builtInGrype(t)
	locked := filepath.Join(residue, "part")
	writeRegular(t, filepath.Join(locked, "chunk"))
	ageTree(t, residue, 48*time.Hour)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	cfg := &config.Config{Version: 1, Paths: []config.PathProfile{{Path: root, MaxDepth: 12,
		Targets: []config.Target{{Signature: r4bGrypeSig}}}}}
	// Present exactly once, withheld for age: never prunable and never
	// omitted, on both the build path and exact revalidation.
	requireWithheldAgeOnce := func(where string, jobs []PruneCandidate) {
		t.Helper()
		assertNeverPrunable(t, jobs, residue)
		n := 0
		for _, j := range jobs {
			if j.Path != residue {
				continue
			}
			n++
			if j.State != CandidateStateWithheld || j.WithheldReason != WithheldReasonAge {
				t.Fatalf("%s: must be withheld for age, got %+v", where, j)
			}
		}
		if n != 1 {
			t.Fatalf("%s: an unreadable in-flight download must stay visible exactly once (got %d)", where, n)
		}
	}
	requireWithheldAgeOnce("build", r4bJobs(t, cfg))
	requireWithheldAgeOnce("exact", exactFor(t, root, ExactEntry{Path: residue, Signature: r4bGrypeSig,
		EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none"}))
}

// P1: a pattern-only target or exact entry on a path a guarded signature
// covers is refused, so a name-collision .gocache cannot be admitted by
// pattern without the content marker.
func TestS4a_PatternOnlyOnGuardedPathRefused(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	writeRegular(t, filepath.Join(repo, "go.mod"))
	cache := filepath.Join(repo, ".gocache")
	write(t, filepath.Join(cache, "README"), "my notes\n")
	ageTree(t, cache, 48*time.Hour)
	cfg := &config.Config{Version: 1, Paths: []config.PathProfile{{Path: root, MaxDepth: 12,
		Targets: []config.Target{{Pattern: "**/.gocache"}}}}}
	assertAbsent(t, r4bJobs(t, cfg), cache)

	plan, err := PlanFromExact(context.Background(), ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{{
		Path: cache, Pattern: "**/.gocache",
		EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("pattern-only exact entry on a guarded path must be refused, got %+v", plan.Candidates)
	}
	// The exact path refuses explicitly with a reason the operator can act on,
	// not only as a silent non-match.
	explained := false
	for _, w := range plan.Warnings {
		if strings.Contains(w.Error(), "use the signature") {
			explained = true
		}
	}
	if !explained {
		t.Fatalf("exact refusal must explain itself, got %v", plan.Warnings)
	}
}

// P2: a reused built-in id cannot replace the candidate patterns. A pattern
// aimed at the live database never admits it.
func TestS4a_ReusedIDCannotRetargetLiveDB(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "Library", "Caches", "grype", "db")
	for _, n := range []string{"5", "6"} {
		writeRegular(t, filepath.Join(db, n, "import.json"))
		writeRegular(t, filepath.Join(db, n, "vulnerability.db"))
	}
	ageTree(t, db, 48*time.Hour)
	cfg := &config.Config{Version: 1,
		Signatures: config.SignatureCatalog{"development": {"security": {"grype-db-download": {
			CandidatePatterns: []string{"**/db/6"},
			AnyChildPaths:     []string{"vulnerability.db"},
			SafeToPrune:       true,
		}}}},
		Paths: []config.PathProfile{{Path: root, MaxDepth: 12, Targets: []config.Target{{Signature: r4bGrypeSig}}}},
	}
	jobs := r4bJobs(t, cfg)
	for _, n := range []string{"5", "6"} {
		assertAbsent(t, jobs, filepath.Join(db, n))
	}
}

// provenance is one way a candidate can reach the planner. B1, P1 and P2 each
// slipped through a different one, so the hard gates are asserted for every
// built-in reclaim signature under every provenance.
type provenance struct {
	name string
	plan func(t *testing.T, root, sigID, pattern string, candidate string) []PruneCandidate
}

func provenances() []provenance {
	build := func(targets []config.Target, sigs config.SignatureCatalog) func(t *testing.T, root, _, _ string, _ string) []PruneCandidate {
		return func(t *testing.T, root, _, _ string, _ string) []PruneCandidate {
			cfg := &config.Config{Version: 1, Signatures: sigs,
				Paths: []config.PathProfile{{Path: root, MaxDepth: 12, Targets: targets}}}
			return r4bJobs(t, cfg)
		}
	}
	return []provenance{
		{"built-in signature", func(t *testing.T, root, sigID, _, _ string) []PruneCandidate {
			return build([]config.Target{{Signature: sigID}}, nil)(t, root, "", "", "")
		}},
		{"reused-id override (loosened)", func(t *testing.T, root, sigID, pattern, _ string) []PruneCandidate {
			parts := strings.Split(sigID, ".")
			loose := config.SignatureCatalog{parts[0]: {parts[1]: {parts[2]: {
				CandidatePatterns: []string{pattern}, AnyChildPaths: []string{"vulnerability.db", "README"}, SafeToPrune: true,
			}}}}
			return build([]config.Target{{Signature: sigID}}, loose)(t, root, "", "", "")
		}},
		{"profile pattern target", func(t *testing.T, root, _, pattern, _ string) []PruneCandidate {
			return build([]config.Target{{Pattern: pattern}}, nil)(t, root, "", "", "")
		}},
		{"exact signature entry", func(t *testing.T, root, sigID, _, candidate string) []PruneCandidate {
			return exactFor(t, root, ExactEntry{Path: candidate, Signature: sigID,
				EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none"})
		}},
		{"exact pattern entry", func(t *testing.T, root, _, pattern, candidate string) []PruneCandidate {
			return exactFor(t, root, ExactEntry{Path: candidate, Pattern: pattern,
				EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none"})
		}},
	}
}

// The S3 hard gates hold for both new signatures under every provenance.
func TestS4a_HardGatesUnderEveryProvenance(t *testing.T) {
	const grypePattern = "**/grype/db/grype-db-download*"
	const gocachePattern = "**/.gocache"
	for _, p := range provenances() {
		t.Run(p.name, func(t *testing.T) {
			// Gate 1: the live db/<N> is never a candidate in any state: no
			// signature, override or pattern may name a guarded signature's
			// candidate or its live evidence directory. (A raw pattern on an
			// ancestor, such as **/grype/db, is the user's own whole-tree
			// intent and stays the documented pattern behaviour.)
			t.Run("live db never a candidate", func(t *testing.T) {
				root := t.TempDir()
				db := filepath.Join(root, "Library", "Caches", "grype", "db")
				for _, n := range []string{"5", "6"} {
					writeRegular(t, filepath.Join(db, n, "import.json"))
					writeRegular(t, filepath.Join(db, n, "vulnerability.db"))
				}
				ageTree(t, db, 48*time.Hour)
				live := filepath.Join(db, "6")
				for _, pat := range []string{grypePattern, "**/db/6"} {
					jobs := p.plan(t, root, r4bGrypeSig, pat, live)
					assertAbsent(t, jobs, live)
					assertAbsent(t, jobs, filepath.Join(db, "5"))
				}
			})
			// Gate 2: a name-collision .gocache (no Go marker) is never admitted.
			t.Run("name-collision gocache absent", func(t *testing.T) {
				root := t.TempDir()
				repo := filepath.Join(root, "repo")
				writeRegular(t, filepath.Join(repo, "go.mod"))
				cache := filepath.Join(repo, ".gocache")
				write(t, filepath.Join(cache, "README"), "my notes\n")
				ageTree(t, cache, 48*time.Hour)
				assertAbsent(t, p.plan(t, root, r4bGocacheSig, gocachePattern, cache), cache)
			})
			// Gate 3: a fresh (in-flight) download is never prunable; when it
			// is admitted at all it is withheld for age.
			t.Run("fresh download never prunable", func(t *testing.T) {
				root, residue := builtInGrype(t)
				ageTree(t, residue, 3*time.Hour)
				now := time.Now()
				if err := os.Chtimes(filepath.Join(residue, "vulnerability.db"), now, now); err != nil {
					t.Fatal(err)
				}
				jobs := p.plan(t, root, r4bGrypeSig, grypePattern, residue)
				assertNeverPrunable(t, jobs, residue)
				for _, j := range jobs {
					if j.Path == residue && (j.State != CandidateStateWithheld || j.WithheldReason != WithheldReasonAge) {
						t.Fatalf("an admitted fresh download must be withheld for age, got %+v", j)
					}
				}
			})
			// A grype download with no live sibling is never admitted.
			t.Run("no live sibling absent", func(t *testing.T) {
				root := t.TempDir()
				orphan := filepath.Join(root, "Library", "Caches", "grype", "db", "grype-db-download9")
				writeRegular(t, filepath.Join(orphan, "vulnerability.db"))
				ageTree(t, orphan, 48*time.Hour)
				assertAbsent(t, p.plan(t, root, r4bGrypeSig, grypePattern, orphan), orphan)
			})
		})
	}
}

// T1: the floor's own fail-closed branch. The residue is old and fully
// readable, so the main walk admits it and sizing finishes, but it nests
// deeper than the floor's activity bound. Only the floor can withhold it:
// present exactly once, withheld for age, on both paths.
func TestS4a_FloorActivityBoundWithholds(t *testing.T) {
	root, residue := builtInGrype(t)
	deep := residue
	for i := 0; i <= floorScanDepth+1; i++ {
		deep = filepath.Join(deep, fmt.Sprintf("d%d", i))
	}
	writeRegular(t, filepath.Join(deep, "chunk"))
	ageTree(t, residue, 48*time.Hour)
	cfg := &config.Config{Version: 1, Paths: []config.PathProfile{{Path: root, MaxDepth: 12,
		Targets: []config.Target{{Signature: r4bGrypeSig}}}}}
	check := func(where string, jobs []PruneCandidate) {
		t.Helper()
		n := 0
		for _, j := range jobs {
			if j.Path != residue {
				continue
			}
			n++
			if j.State != CandidateStateWithheld || j.WithheldReason != WithheldReasonAge {
				t.Fatalf("%s: an unbounded activity read must withhold for age, got %+v", where, j)
			}
		}
		if n != 1 {
			t.Fatalf("%s: residue must be present exactly once (got %d)", where, n)
		}
	}
	check("build", r4bJobs(t, cfg))
	check("exact", exactFor(t, root, ExactEntry{Path: residue, Signature: r4bGrypeSig,
		EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none"}))
}
