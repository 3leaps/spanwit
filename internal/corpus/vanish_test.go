package corpus

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestVanishDuringWalk exercises the race the vanishing shape exists to
// create: an entry present when its directory is listed and gone when it is
// stated. The assertion is a reconciliation, not an exact total, because an
// exact total is precisely what a mutating tree cannot promise.
func TestVanishDuringWalk(t *testing.T) {
	m := buildTemp(t, Spec{Kind: KindWide, Scale: 20, FileSize: 32, RecordEntries: true})

	victims := VanishableFiles(m, 5)
	if len(victims) != 5 {
		t.Fatalf("expected 5 vanishable files, got %d", len(victims))
	}
	v := NewVanisher(m, victims)

	var seen, missed int64
	err := filepath.WalkDir(m.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a vanished entry is not a fatal walk error
		}
		if p == m.Root || d.IsDir() {
			return nil
		}
		// Remove a pending victim, then stat this entry. Some stats will
		// land on an object already removed.
		if _, err := v.VanishNext(); err != nil {
			t.Errorf("vanish: %v", err)
		}
		if _, err := os.Lstat(p); err != nil {
			if os.IsNotExist(err) {
				missed++
				return nil
			}
			return err
		}
		seen++
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	removed := int64(v.Count())
	if removed == 0 {
		t.Fatal("vanisher removed nothing; the race was never created")
	}
	if fails := v.Failures(); len(fails) != 0 {
		t.Errorf("vanisher failures: %v", fails)
	}

	// Reconciliation: everything the walk observed, plus everything that
	// vanished before it could be observed, must account for the corpus.
	// Entries removed after being seen are counted in seen, so the sum is a
	// bound rather than an equality.
	if seen+missed > m.Files {
		t.Errorf("walk accounted for %d entries (%d seen, %d vanished) over a corpus of %d files",
			seen+missed, seen, missed, m.Files)
	}
	if seen+removed < m.Files {
		t.Errorf("walk lost entries: saw %d, %d removed, corpus had %d files",
			seen, removed, m.Files)
	}
}

// TestVanisherRecordsFailures confirms a vanisher that cannot remove reports
// it, rather than letting a test pass while exercising no race at all.
func TestVanisherRecordsFailures(t *testing.T) {
	m := buildTemp(t, Spec{Kind: KindWide, Scale: 2, FileSize: 16})

	v := NewVanisher(m, []string{"wide-0/does-not-exist.bin"})
	if _, err := v.VanishNext(); err == nil {
		t.Fatal("expected an error removing a nonexistent path")
	}
	if v.Count() != 0 {
		t.Errorf("count should be 0 after a failed removal, got %d", v.Count())
	}
	fails := v.Failures()
	if len(fails) != 1 {
		t.Fatalf("expected 1 recorded failure, got %d", len(fails))
	}
	if fails[0].RelPath != "wide-0/does-not-exist.bin" || fails[0].Reason == "" {
		t.Errorf("failure poorly recorded: %+v", fails[0])
	}

	// Draining a vanisher with nothing pending is not an error.
	empty := NewVanisher(m, nil)
	rel, err := empty.VanishNext()
	if err != nil || rel != "" {
		t.Errorf("empty vanisher: got (%q, %v), want (\"\", nil)", rel, err)
	}
}

// TestMountBoundaryOmission checks that an environment with no mount boundary
// below the root yields an omission rather than a silent pass. A device-
// boundary measurement run on such a root has not tested device boundaries.
func TestMountBoundaryOmission(t *testing.T) {
	m := buildTemp(t, Spec{Kind: KindWide, Scale: 3, FileSize: 16})

	boundaries, observable, err := MountBoundariesUnder(m.Root, 4)
	if err != nil {
		t.Fatalf("scan for boundaries: %v", err)
	}

	omission, omitted := FixtureOmission(observable, len(boundaries))
	switch {
	case !observable:
		if !omitted {
			t.Error("unobservable device identity must produce an omission")
		}
	case len(boundaries) == 0:
		if !omitted {
			t.Error("a root with no mount boundary must produce an omission")
		}
		if omission.Element != "mount-boundaries" || omission.Reason == "" {
			t.Errorf("omission poorly formed: %+v", omission)
		}
	default:
		if omitted {
			t.Errorf("found %d boundaries but still reported an omission", len(boundaries))
		}
		for _, b := range boundaries {
			if b.Device == b.ParentDevice {
				t.Errorf("boundary %s has the same device as its parent", b.Path)
			}
		}
	}
}

// TestCoverageNoteStatesOmissions confirms a corpus with omissions says so in
// its one-line note, which is what a benchmark row embeds.
func TestCoverageNoteStatesOmissions(t *testing.T) {
	m := buildTemp(t, Spec{Kind: KindWide, Scale: 2, FileSize: 16})
	if !m.Complete() {
		t.Skipf("unexpected omissions on a synthetic shape: %s", m.CoverageNote())
	}
	if note := m.CoverageNote(); note == "" {
		t.Fatal("complete corpus produced no coverage note")
	}

	// Inject an omission the way a capability probe would, and confirm the
	// note changes character rather than staying silently reassuring.
	m.Omitted = append(m.Omitted, Omission{Element: "sparse-files", Reason: "unsupported filesystem"})
	if m.Complete() {
		t.Error("manifest with an omission still reports complete")
	}
	note := m.CoverageNote()
	if note == "" || note == m.ID+": all requested shape elements present" {
		t.Errorf("coverage note did not reflect the omission: %q", note)
	}
}
