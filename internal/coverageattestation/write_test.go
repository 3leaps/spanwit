package coverageattestation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/inventory"
)

func TestWriteDocumentPublishesValidatedNoClobberFile(t *testing.T) {
	parent := t.TempDir()
	destination := filepath.Join(parent, "attestation.json")
	if err := WriteDocument(destination, testDocument(), publicationEvidence(t)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeValidated(data); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o want 600", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(parent, ".spanwit-coverage-attestation-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("stage residue=%v", matches)
	}

	before := append([]byte(nil), data...)
	if err := WriteDocument(destination, testDocument(), publicationEvidence(t)); err == nil {
		t.Fatal("expected existing destination rejection")
	}
	after, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("existing destination changed")
	}
}

func TestWriteDocumentFailsBeforeCreation(t *testing.T) {
	parent := t.TempDir()
	t.Run("invalid document", func(t *testing.T) {
		destination := filepath.Join(parent, "invalid.json")
		document := testDocument()
		document.Claims[0].Volume.Observed = -1
		if err := WriteDocument(destination, document, publicationEvidence(t)); err == nil {
			t.Fatal("expected validation failure")
		}
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			t.Fatalf("destination created after validation failure: %v", err)
		}
	})

	t.Run("stdout", func(t *testing.T) {
		if err := WriteDocument("-", testDocument(), publicationEvidence(t)); err == nil {
			t.Fatal("expected stdout rejection")
		}
	})

	t.Run("inside patient root", func(t *testing.T) {
		root := t.TempDir()
		canonicalRoot, err := canonicalExistingPath(root)
		if err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(root, "attestation.json")
		if err := WriteDocument(destination, testDocument(), publicationEvidence(t, canonicalRoot)); err == nil ||
			!strings.Contains(err.Error(), "outside enumerated root") {
			t.Fatalf("expected patient-root rejection, got %v", err)
		}
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			t.Fatalf("destination created in patient root: %v", err)
		}
	})

	t.Run("existing symlink", func(t *testing.T) {
		target := filepath.Join(parent, "target")
		if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(parent, "link.json")
		if err := os.Symlink(target, destination); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := WriteDocument(destination, testDocument(), publicationEvidence(t)); err == nil {
			t.Fatal("expected existing symlink rejection")
		}
	})
}

func TestWriteDocumentFailsClosedWithoutAdmittedRootIdentity(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "attestation.json")
	if err := WriteDocument(destination, testDocument(), Evidence{}); err == nil ||
		!strings.Contains(err.Error(), "admitted-root identities") {
		t.Fatalf("expected admitted-root identity rejection, got %v", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination created without admitted-root identity: %v", err)
	}
}

func TestCanonicalAdmittedRootsSurviveUserRootSymlinkRetarget(t *testing.T) {
	base := t.TempDir()
	patient := filepath.Join(base, "patient")
	output := filepath.Join(base, "output")
	if err := os.Mkdir(patient, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalPatient, err := canonicalExistingPath(patient)
	if err != nil {
		t.Fatal(err)
	}
	requestedRoot := filepath.Join(base, "requested-root")
	if err := os.Symlink(patient, requestedRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	destination := filepath.Join(output, "attestation.json")
	evidence := publicationEvidence(t, canonicalPatient)
	earlyDestination, err := PreflightDestination(destination, []string{requestedRoot})
	if err != nil {
		t.Fatalf("early raw-root preflight: %v", err)
	}
	if err := os.Remove(requestedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(output, requestedRoot); err != nil {
		t.Fatal(err)
	}

	if _, err := PreflightDestination(earlyDestination, []string{requestedRoot}); err == nil {
		t.Fatal("retargeted raw root unexpectedly remained safe")
	}
	if _, err := preflightCanonicalDestination(earlyDestination, []string{canonicalPatient}); err != nil {
		t.Fatalf("canonical admitted-root preflight changed with retargeted input: %v", err)
	}
	if err := WriteDocument(earlyDestination, testDocument(), evidence); err != nil {
		t.Fatalf("publication against canonical admitted root: %v", err)
	}
	if _, err := os.Stat(earlyDestination); err != nil {
		t.Fatalf("published attestation missing: %v", err)
	}
}

func TestWriteDocumentFailsClosedWhenCanonicalRootIsReplaced(t *testing.T) {
	base := t.TempDir()
	patient := filepath.Join(base, "patient")
	output := filepath.Join(base, "output")
	if err := os.Mkdir(patient, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalPatient, err := canonicalExistingPath(patient)
	if err != nil {
		t.Fatal(err)
	}
	evidence := publicationEvidence(t, canonicalPatient)
	if err := os.Rename(patient, filepath.Join(base, "patient-moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(output, patient); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	destination := filepath.Join(output, "attestation.json")
	if err := WriteDocument(destination, testDocument(), evidence); err == nil ||
		!strings.Contains(err.Error(), "admitted roots") {
		t.Fatalf("expected admitted-root identity rejection, got %v", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination created after root replacement: %v", err)
	}
}

func TestPinnedParentRejectsReplacementBeforeStaging(t *testing.T) {
	base, patient, parent, outside := publicationPaths(t)
	destination := filepath.Join(parent, "attestation.json")
	moved := filepath.Join(base, "pinned-original")
	var swapErr error
	err := writeDocument(destination, testDocument(), publicationEvidence(t, patient), publicationHooks{
		beforeStage: func() {
			swapErr = replaceParentPath(parent, moved, patient)
		},
	})
	if swapErr != nil {
		t.Skipf("parent replacement fixture unavailable: %v", swapErr)
	}
	if err == nil || !strings.Contains(err.Error(), "parent pathname") {
		t.Fatalf("expected pinned-parent rejection, got %v", err)
	}
	assertNoPublicationIn(t, patient, outside, moved)
}

func TestPinnedParentRejectsReplacementAfterStageSync(t *testing.T) {
	base, patient, parent, outside := publicationPaths(t)
	destination := filepath.Join(parent, "attestation.json")
	moved := filepath.Join(base, "pinned-original")
	var swapErr error
	err := writeDocument(destination, testDocument(), publicationEvidence(t, patient), publicationHooks{
		afterStageSync: func(string) {
			swapErr = replaceParentPath(parent, moved, patient)
		},
	})
	if swapErr != nil {
		t.Skipf("parent replacement fixture unavailable: %v", swapErr)
	}
	if err == nil || !strings.Contains(err.Error(), "parent pathname") {
		t.Fatalf("expected pinned-parent rejection, got %v", err)
	}
	assertNoPublicationIn(t, patient, outside, moved)
}

func TestPinnedParentRejectsReplacementWithOutsideDirectory(t *testing.T) {
	base, patient, parent, outside := publicationPaths(t)
	destination := filepath.Join(parent, "attestation.json")
	moved := filepath.Join(base, "pinned-original")
	var swapErr error
	err := writeDocument(destination, testDocument(), publicationEvidence(t, patient), publicationHooks{
		beforeStage: func() {
			swapErr = replaceParentPath(parent, moved, outside)
		},
	})
	if swapErr != nil {
		t.Skipf("parent replacement fixture unavailable: %v", swapErr)
	}
	if err == nil || !strings.Contains(err.Error(), "parent pathname") {
		t.Fatalf("expected pinned-parent rejection, got %v", err)
	}
	assertNoPublicationIn(t, patient, outside, moved)
}

func TestPinnedParentDetectsReplacementAfterLink(t *testing.T) {
	base, patient, parent, outside := publicationPaths(t)
	destination := filepath.Join(parent, "attestation.json")
	moved := filepath.Join(base, "pinned-original")
	var swapErr error
	err := writeDocument(destination, testDocument(), publicationEvidence(t, patient), publicationHooks{
		afterLink: func() {
			swapErr = replaceParentPath(parent, moved, outside)
		},
	})
	if swapErr != nil {
		t.Skipf("parent replacement fixture unavailable: %v", swapErr)
	}
	if err == nil || (!strings.Contains(err.Error(), "parent pathname") &&
		!strings.Contains(err.Error(), "public coverage-attestation destination")) {
		t.Fatalf("expected post-link pathname rejection, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "attestation.json")); !os.IsNotExist(err) {
		t.Fatalf("replacement target received final publication: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(patient, "attestation.json")); !os.IsNotExist(err) {
		t.Fatalf("patient received final publication: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moved, "attestation.json")); err != nil {
		t.Fatalf("pinned parent lost final publication after post-link replacement: %v", err)
	}
}

func TestPinnedStageRejectsReplacementWithoutDeletingAttackerEntry(t *testing.T) {
	_, patient, parent, outside := publicationPaths(t)
	destination := filepath.Join(parent, "attestation.json")
	var stagePath string
	err := writeDocument(destination, testDocument(), publicationEvidence(t, patient), publicationHooks{
		afterStageSync: func(stage string) {
			stagePath = filepath.Join(parent, stage)
			if removeErr := os.Remove(stagePath); removeErr != nil {
				t.Fatalf("replace stage remove: %v", removeErr)
			}
			if linkErr := os.Symlink(filepath.Join(outside, "attacker"), stagePath); linkErr != nil {
				t.Skipf("symlink unavailable: %v", linkErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "stage identity changed") {
		t.Fatalf("expected stage identity rejection, got %v", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("stage replacement produced final publication: %v", err)
	}
	info, err := os.Lstat(stagePath)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("attacker stage replacement was removed or changed: info=%v err=%v", info, err)
	}
}

func TestPinnedParentRefusesDestinationAppearingAtFinalSeam(t *testing.T) {
	_, patient, parent, _ := publicationPaths(t)
	destination := filepath.Join(parent, "attestation.json")
	attackerPayload := []byte("attacker destination")
	err := writeDocument(destination, testDocument(), publicationEvidence(t, patient), publicationHooks{
		beforeLink: func(string) {
			if writeErr := os.WriteFile(destination, attackerPayload, 0o600); writeErr != nil {
				t.Fatalf("create final seam destination: %v", writeErr)
			}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("expected no-clobber rejection, got %v", err)
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != string(attackerPayload) {
		t.Fatalf("existing destination changed: payload=%q err=%v", got, readErr)
	}
	matches, globErr := filepath.Glob(filepath.Join(parent, ".spanwit-coverage-attestation-*.tmp"))
	if globErr != nil || len(matches) != 0 {
		t.Fatalf("expected pinned-stage cleanup after no-clobber rejection: matches=%v err=%v", matches, globErr)
	}
}

func TestPinnedParentRechecksRootsAfterLink(t *testing.T) {
	base, patient, parent, outside := publicationPaths(t)
	destination := filepath.Join(parent, "attestation.json")
	patientMoved := filepath.Join(base, "patient-moved")
	var retargetErr error
	err := writeDocument(destination, testDocument(), publicationEvidence(t, patient), publicationHooks{
		afterLink: func() {
			if renameErr := os.Rename(patient, patientMoved); renameErr != nil {
				retargetErr = renameErr
				return
			}
			retargetErr = os.Symlink(outside, patient)
		},
	})
	if retargetErr != nil {
		t.Skipf("root retarget fixture unavailable: %v", retargetErr)
	}
	if err == nil || !strings.Contains(err.Error(), "admitted roots") {
		t.Fatalf("expected post-link root identity rejection, got %v", err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("post-link failure removed final publication: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "attestation.json")); !os.IsNotExist(err) {
		t.Fatalf("root retarget redirected final publication: %v", err)
	}
}

func publicationPaths(t *testing.T) (base, patient, parent, outside string) {
	t.Helper()
	base = t.TempDir()
	patient = filepath.Join(base, "patient")
	parent = filepath.Join(base, "output")
	outside = filepath.Join(base, "outside")
	for _, path := range []string{patient, parent, outside} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	canonicalPatient, err := canonicalExistingPath(patient)
	if err != nil {
		t.Fatal(err)
	}
	return base, canonicalPatient, parent, outside
}

func replaceParentPath(parent, moved, replacement string) error {
	if err := os.Rename(parent, moved); err != nil {
		return err
	}
	return os.Symlink(replacement, parent)
}

func assertNoPublicationIn(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(filepath.Join(path, "attestation.json")); !os.IsNotExist(err) {
			t.Fatalf("unexpected final publication under %s: %v", path, err)
		}
		matches, err := filepath.Glob(filepath.Join(path, ".spanwit-coverage-attestation-*.tmp"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 {
			t.Fatalf("stage residue under %s: %v", path, matches)
		}
	}
}

func publicationEvidence(t *testing.T, paths ...string) Evidence {
	t.Helper()
	if len(paths) == 0 {
		paths = []string{t.TempDir()}
	}
	roots := make([]inventory.Root, 0, len(paths))
	for index, path := range paths {
		roots = append(roots, inventory.Root{ID: fmt.Sprintf("root-%d", index+1), Path: path})
	}
	collector, err := NewCollector(&recordingSink{}, DefaultMaxGaps)
	if err != nil {
		t.Fatal(err)
	}
	if err := collector.Header(inventory.Header{RunID: "publication-test", Roots: roots}); err != nil {
		t.Fatal(err)
	}
	if err := collector.Summary(inventory.Summary{Lifecycle: inventory.LifecycleComplete}); err != nil {
		t.Fatal(err)
	}
	evidence, err := collector.Evidence()
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}
