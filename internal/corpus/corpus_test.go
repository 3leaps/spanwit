package corpus

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildTemp builds a corpus in a per-test temporary directory and registers
// cleanup that can remove sealed directories.
func buildTemp(t *testing.T, spec Spec) *Manifest {
	t.Helper()
	root := filepath.Join(t.TempDir(), "corpus")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	m, err := Build(root, spec)
	if err != nil {
		t.Fatalf("build %s: %v", spec.Kind, err)
	}
	t.Cleanup(func() {
		if err := Remove(m); err != nil {
			t.Errorf("remove corpus: %v", err)
		}
	})
	return m
}

// walkCounts walks a built corpus the way a naive consumer would and reports
// what it observed, plus the paths it was denied.
type walkCounts struct {
	dirs     int64
	files    int64
	symlinks int64
	bytes    int64
	denied   int64
}

func walkCorpus(t *testing.T, root string) walkCounts {
	t.Helper()
	var c walkCounts
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			c.denied++
			return nil //nolint:nilerr // recording the denial is the point
		}
		if p == root {
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			c.symlinks++
		case d.IsDir():
			c.dirs++
		default:
			c.files++
			info, err := d.Info()
			if err == nil {
				c.bytes += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return c
}

// TestManifestMatchesObservableTree is the central check: for shapes with no
// unreachable objects, a walk of the built tree must agree exactly with the
// manifest that was computed before the tree existed.
func TestManifestMatchesObservableTree(t *testing.T) {
	cases := []Spec{
		{Kind: KindWide, Scale: 12, FileSize: 64},
		{Kind: KindDeep, Scale: 9, FileSize: 64},
		{Kind: KindManyEmptyDirs, Scale: 25},
		{Kind: KindDevTree, Scale: 40, FileSize: 32},
		{Kind: KindLargeFiles, Scale: 3, FileSize: 4096},
		{Kind: KindHardLinks, Scale: 4, FileSize: 128},
		{Kind: KindSymlinks, Scale: 5, FileSize: 64},
	}

	for _, spec := range cases {
		t.Run(string(spec.Kind), func(t *testing.T) {
			m := buildTemp(t, spec)
			if m.UnreachableObjects != 0 {
				t.Fatalf("shape %s unexpectedly sealed %d objects", spec.Kind, m.UnreachableObjects)
			}
			got := walkCorpus(t, m.Root)

			if got.dirs != m.Dirs {
				t.Errorf("dirs: walk saw %d, manifest planned %d", got.dirs, m.Dirs)
			}
			if got.files != m.Files {
				t.Errorf("files: walk saw %d, manifest planned %d", got.files, m.Files)
			}
			if got.symlinks != m.Symlinks {
				t.Errorf("symlinks: walk saw %d, manifest planned %d", got.symlinks, m.Symlinks)
			}
			if got.denied != 0 {
				t.Errorf("unexpected denials: %d", got.denied)
			}
		})
	}
}

// TestSealedObjectsAreUnreachable proves the mixed-permissions corpus really
// does hide objects, so a coverage-honesty test built on it is testing
// something. On a platform without permission enforcement the corpus must say
// so rather than appear complete.
func TestSealedObjectsAreUnreachable(t *testing.T) {
	m := buildTemp(t, Spec{Kind: KindMixedPermissions, Scale: 3, FileSize: 32})

	if !m.Capabilities.PermissionEnforcement {
		if m.Complete() {
			t.Fatal("corpus claims completeness without permission enforcement")
		}
		t.Skipf("permission enforcement unavailable: %s", m.Capabilities.Summary())
	}

	if m.UnreachableObjects == 0 {
		t.Fatal("mixed-permissions corpus sealed nothing")
	}
	if !m.Complete() {
		t.Errorf("corpus reports omissions despite full capability: %s", m.CoverageNote())
	}

	got := walkCorpus(t, m.Root)
	observableFiles := m.Files - m.UnreachableObjects
	if got.files != observableFiles {
		t.Errorf("walk saw %d files, expected %d observable of %d total",
			got.files, observableFiles, m.Files)
	}
	if got.denied == 0 {
		t.Error("walk reported no denials over a corpus with sealed directories")
	}
	if m.ObservableObjects() >= m.TotalObjects() {
		t.Error("ObservableObjects should be below TotalObjects when objects are sealed")
	}
}

// TestHardLinkAccounting checks the distinction entarch flagged: a sum over
// directory entries is not a sum over distinct inodes, and neither is
// reclaimable space.
func TestHardLinkAccounting(t *testing.T) {
	const groups, size = 4, 512
	m := buildTemp(t, Spec{Kind: KindHardLinks, Scale: groups, FileSize: size})

	if !m.Capabilities.HardLinks {
		if m.Complete() {
			t.Fatal("corpus claims completeness without hard-link support")
		}
		t.Skipf("hard links unavailable: %s", m.Capabilities.Summary())
	}

	if len(m.HardLinkGroups) != groups {
		t.Fatalf("expected %d link groups, got %d", groups, len(m.HardLinkGroups))
	}

	wantEntries := int64(groups * hardLinkFanout)
	if m.Files != wantEntries {
		t.Errorf("entry count: got %d, want %d", m.Files, wantEntries)
	}
	if m.UniqueFiles() != groups {
		t.Errorf("unique inode count: got %d, want %d", m.UniqueFiles(), groups)
	}

	wantApparent := wantEntries * size
	if m.ApparentBytes != wantApparent {
		t.Errorf("apparent bytes: got %d, want %d", m.ApparentBytes, wantApparent)
	}
	wantUnique := int64(groups * size)
	if m.UniqueApparentBytes() != wantUnique {
		t.Errorf("unique apparent bytes: got %d, want %d", m.UniqueApparentBytes(), wantUnique)
	}

	// The gap between the two is the whole point: a tool reporting
	// ApparentBytes as space it could free would overstate by this much.
	if m.ApparentBytes <= m.UniqueApparentBytes() {
		t.Error("hard-link corpus failed to produce a gap between entry sum and inode sum")
	}
}

// TestSparseAccounting checks that apparent size and allocated size diverge,
// which is the other way an entry-size sum stops meaning reclaimable space.
func TestSparseAccounting(t *testing.T) {
	const apparent = 8 << 20
	m := buildTemp(t, Spec{Kind: KindSparseFiles, Scale: 2, FileSize: apparent})

	if !m.Capabilities.SparseFiles {
		if m.Complete() {
			t.Fatal("corpus claims completeness without sparse-file support")
		}
		t.Skipf("sparse files unavailable: %s", m.Capabilities.Summary())
	}

	path := m.Path("sparse/sparse-0000.img")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sparse file: %v", err)
	}
	if info.Size() != apparent {
		t.Errorf("apparent size: got %d, want %d", info.Size(), apparent)
	}
	alloc := allocatedBytes(info)
	if alloc < 0 {
		t.Skip("allocated size not observable")
	}
	if alloc >= info.Size() {
		t.Errorf("sparse file allocated %d of %d apparent bytes; not sparse", alloc, info.Size())
	}
}

// TestSymlinkCycleExists confirms the corpus contains a cycle, so a traversal
// that follows directory symlinks without cycle detection has something to
// fail on.
func TestSymlinkCycleExists(t *testing.T) {
	m := buildTemp(t, Spec{Kind: KindSymlinks, Scale: 2, FileSize: 32})
	if !m.Capabilities.Symlinks {
		t.Skipf("symlinks unavailable: %s", m.Capabilities.Summary())
	}

	for _, rel := range []string{"links/self", "links/cycle-a", "links/cycle-b", "links/to-targets"} {
		info, err := os.Lstat(m.Path(rel))
		if err != nil {
			t.Fatalf("lstat %s: %v", rel, err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("%s is not a symlink", rel)
		}
	}

	// The dangling link must actually dangle.
	if _, err := os.Stat(m.Path("links/dangling-0000")); err == nil {
		t.Error("dangling symlink resolved; it should not")
	}
}

// TestDeterministicRebuild proves two builds from one spec produce identical
// plans, which is what lets two benchmark rows carrying the same corpus ID be
// compared.
func TestDeterministicRebuild(t *testing.T) {
	spec := Spec{Kind: KindDevTree, Scale: 30, FileSize: 64, RecordEntries: true}
	a := buildTemp(t, spec)
	b := buildTemp(t, spec)

	if a.ID != b.ID {
		t.Fatalf("corpus IDs differ: %q vs %q", a.ID, b.ID)
	}
	if a.TotalObjects() != b.TotalObjects() {
		t.Errorf("object counts differ: %d vs %d", a.TotalObjects(), b.TotalObjects())
	}
	if a.ApparentBytes != b.ApparentBytes {
		t.Errorf("apparent bytes differ: %d vs %d", a.ApparentBytes, b.ApparentBytes)
	}
	if len(a.Entries) != len(b.Entries) {
		t.Fatalf("entry counts differ: %d vs %d", len(a.Entries), len(b.Entries))
	}
	for i := range a.Entries {
		if a.Entries[i].RelPath != b.Entries[i].RelPath {
			t.Fatalf("entry %d differs: %q vs %q", i, a.Entries[i].RelPath, b.Entries[i].RelPath)
		}
		if !a.Entries[i].ModTime.Equal(b.Entries[i].ModTime) {
			t.Fatalf("entry %d modtime differs: %v vs %v",
				i, a.Entries[i].ModTime, b.Entries[i].ModTime)
		}
	}
}

// TestModTimesAreFixtureStable confirms modification times come from the
// fixture epoch rather than the wall clock, so age-window filters have a
// stable oracle.
func TestModTimesAreFixtureStable(t *testing.T) {
	m := buildTemp(t, Spec{Kind: KindWide, Scale: 3, FileSize: 32, RecordEntries: true})

	info, err := os.Stat(m.Path("wide-0/entry-000000.bin"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.ModTime().After(FixtureEpoch) {
		t.Errorf("modtime %v is after the fixture epoch %v", info.ModTime(), FixtureEpoch)
	}
	if time.Since(info.ModTime()) < 365*24*time.Hour {
		t.Error("fixture object is less than a year old; age-window tests would be unstable")
	}
}

// TestSpecValidation covers the refusals that keep an unbuildable spec from
// producing a partial corpus.
func TestSpecValidation(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
	}{
		{"no kind", Spec{Scale: 1}},
		{"unknown kind", Spec{Kind: "census", Scale: 1}},
		{"zero scale", Spec{Kind: KindWide}},
		{"negative scale", Spec{Kind: KindWide, Scale: -1}},
		{"negative file size", Spec{Kind: KindWide, Scale: 1, FileSize: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.spec.Validate(); err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

// TestAllKindsBuild is a breadth check that every declared kind builds and
// reports a manifest, at the smallest useful scale.
func TestAllKindsBuild(t *testing.T) {
	for _, kind := range AllKinds() {
		t.Run(string(kind), func(t *testing.T) {
			m := buildTemp(t, Spec{Kind: kind, Scale: 2, FileSize: 64})
			if m.ID == "" {
				t.Error("manifest has no ID")
			}
			if m.TotalObjects() == 0 {
				t.Error("corpus built nothing")
			}
			if m.Capabilities.Reasons == nil {
				t.Error("capabilities were not probed")
			}
			// Every kind must be able to state its coverage.
			if note := m.CoverageNote(); note == "" {
				t.Error("manifest produced no coverage note")
			}
		})
	}
}

// TestDeepCorpusRecordsPathLimit confirms that requesting a chain deeper than
// the platform allows yields a corpus at the depth actually reached, with an
// omission naming the ceiling, rather than a failed build or a silent
// truncation.
func TestDeepCorpusRecordsPathLimit(t *testing.T) {
	m := buildTemp(t, Spec{Kind: KindDeep, Scale: 5000, FileSize: 16})

	if m.Dirs == 0 {
		t.Fatal("deep corpus built no directories")
	}
	if m.Dirs >= 5000 {
		t.Skip("platform allowed the full requested depth; no ceiling to record")
	}

	if m.Complete() {
		t.Fatal("corpus truncated by a path limit still reports complete")
	}
	var found bool
	for _, o := range m.Omitted {
		if o.Element == "deep-levels" {
			found = true
			if o.Reason == "" {
				t.Error("path-limit omission gives no reason")
			}
		}
	}
	if !found {
		t.Errorf("no deep-levels omission recorded; omissions were %v", m.Omitted)
	}

	// The tree that was built must still be walkable and match the manifest.
	got := walkCorpus(t, m.Root)
	if got.dirs != m.Dirs {
		t.Errorf("dirs: walk saw %d, manifest planned %d", got.dirs, m.Dirs)
	}
	if got.files != m.Files {
		t.Errorf("files: walk saw %d, manifest planned %d", got.files, m.Files)
	}
}
