package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthorizeDeletion_DescendantOK(t *testing.T) {
	root := t.TempDir()
	target := mkdir(t, filepath.Join(root, "app", "target"))
	tok, err := AuthorizeDeletion(DeletionRequest{
		Target:     target,
		Boundary:   root,
		Provenance: ProvenanceConfigProfile,
	})
	if err != nil {
		t.Fatalf("expected authorization, got %v", err)
	}
	if tok.Target() != filepath.Clean(target) {
		t.Fatalf("token target %q", tok.Target())
	}
}

func TestAuthorizeDeletion_MissingProvenanceFailsClosed(t *testing.T) {
	root := t.TempDir()
	target := mkdir(t, filepath.Join(root, "target"))
	if _, err := AuthorizeDeletion(DeletionRequest{Target: target, Boundary: root}); err == nil {
		t.Fatal("empty provenance must fail closed")
	}
	if _, err := AuthorizeDeletion(DeletionRequest{Target: target, Boundary: root, Provenance: "made-up"}); err == nil {
		t.Fatal("unknown provenance must fail closed")
	}
}

func TestAuthorizeDeletion_BoundaryEqualityRefusedForCurrentProvenances(t *testing.T) {
	root := t.TempDir()
	target := mkdir(t, filepath.Join(root, "cache"))
	for _, prov := range []string{ProvenanceConfigProfile, ProvenanceExactPlanAnalysis} {
		// Strict descendant (default): target == boundary is refused.
		if _, err := AuthorizeDeletion(DeletionRequest{
			Target: target, Boundary: target, Provenance: prov,
		}); err == nil {
			t.Fatalf("%s: boundary equality must be refused", prov)
		}
		// Config discovery and exact-plan replay may not request self-or-descendant
		// at all — only a future registered capability may.
		if _, err := AuthorizeDeletion(DeletionRequest{
			Target: target, Boundary: target, Relation: ContainmentSelfOrDescendant, Provenance: prov,
		}); err == nil {
			t.Fatalf("%s: self-or-descendant must not be permitted", prov)
		}
	}
}

// checkContainment still supports self-or-descendant for a future code-registered
// capability, even though no current provenance may request it.
func TestCheckContainment_SelfOrDescendantEquality(t *testing.T) {
	if err := checkContainment("/a/b", "/a/b", ContainmentSelfOrDescendant); err != nil {
		t.Fatalf("self-or-descendant should permit equality at the containment layer: %v", err)
	}
	if err := checkContainment("/a/b", "/a/b", ContainmentDescendant); err == nil {
		t.Fatal("strict descendant must refuse equality")
	}
}

func TestAuthorizeDeletion_RelativeOrUncleanRejected(t *testing.T) {
	root := t.TempDir()
	target := mkdir(t, filepath.Join(root, "target"))
	if _, err := AuthorizeDeletion(DeletionRequest{
		Target: "relative/target", Boundary: root, Provenance: ProvenanceConfigProfile,
	}); err == nil {
		t.Fatal("relative target must be rejected (authority not reinterpreted vs cwd)")
	}
	unclean := root + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(root)
	if _, err := AuthorizeDeletion(DeletionRequest{
		Target: target, Boundary: unclean, Provenance: ProvenanceConfigProfile,
	}); err == nil {
		t.Fatal("unclean boundary (contains ..) must be rejected")
	}
}

func TestRefuseNestedMounts_DifferentDeviceRefused(t *testing.T) {
	root := t.TempDir()
	nested := mkdir(t, filepath.Join(root, "sub", "mnt"))
	deviceIDForTest = func(path string) (uint64, bool, error) {
		if path == nested {
			return 2, true, nil // simulate a nested mount on a different device
		}
		return 1, true, nil
	}
	defer func() { deviceIDForTest = nil }()
	if err := refuseNestedMounts(root); err == nil {
		t.Fatal("expected refusal when a nested subtree is on a different device")
	}
	deviceIDForTest = func(path string) (uint64, bool, error) { return 1, true, nil }
	if err := refuseNestedMounts(root); err != nil {
		t.Fatalf("uniform device must not be refused: %v", err)
	}
}

func TestAuthorizeDeletion_SiblingPrefixConfusion(t *testing.T) {
	root := t.TempDir()
	boundary := mkdir(t, filepath.Join(root, "a"))
	target := mkdir(t, filepath.Join(root, "ab")) // string-prefix of boundary path but NOT a descendant
	if _, err := AuthorizeDeletion(DeletionRequest{
		Target: target, Boundary: boundary, Provenance: ProvenanceConfigProfile,
	}); err == nil {
		t.Fatal("/root/ab must not be considered contained under /root/a")
	}
}

func TestAuthorizeDeletion_EscapeRefused(t *testing.T) {
	root := t.TempDir()
	boundary := mkdir(t, filepath.Join(root, "authorized"))
	target := mkdir(t, filepath.Join(root, "elsewhere", "target"))
	if _, err := AuthorizeDeletion(DeletionRequest{
		Target: target, Boundary: boundary, Provenance: ProvenanceConfigProfile,
	}); err == nil {
		t.Fatal("target outside boundary must be refused")
	}
}

func TestAuthorizeDeletion_NonDirRefused(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeDeletion(DeletionRequest{
		Target: file, Boundary: root, Provenance: ProvenanceConfigProfile,
	}); err == nil {
		t.Fatal("non-directory target must be refused")
	}
}

func TestAuthorizeDeletion_VolumeRootAndHomeRefused(t *testing.T) {
	if _, err := AuthorizeDeletion(DeletionRequest{
		Target: string(filepath.Separator), Boundary: string(filepath.Separator), Provenance: ProvenanceConfigProfile,
	}); err == nil {
		t.Fatal("volume root target must be refused")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if _, err := AuthorizeDeletion(DeletionRequest{
			Target: home, Boundary: filepath.Dir(home), Provenance: ProvenanceConfigProfile,
		}); err == nil {
			t.Fatal("home directory target must be refused")
		}
	}
}

func TestAuthorizeDeletion_SymlinkLeafRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlink fixture")
	}
	root := t.TempDir()
	real := mkdir(t, filepath.Join(root, "real"))
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeDeletion(DeletionRequest{
		Target: link, Boundary: root, Provenance: ProvenanceConfigProfile,
	}); err == nil {
		t.Fatal("symlink leaf target must be refused")
	}
}

func TestRemoveGuarded_DeletesAuthorizedDir(t *testing.T) {
	root := t.TempDir()
	target := mkdir(t, filepath.Join(root, "app", "target", "debug"))
	tok, err := AuthorizeDeletion(DeletionRequest{
		Target: filepath.Join(root, "app", "target"), Boundary: root, Provenance: ProvenanceConfigProfile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveGuarded(tok); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("expected removal, stat err=%v", err)
	}
}

func TestRemoveGuarded_IdentityReplacementRejected(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	mkdir(t, filepath.Join(target, "debug"))
	// Probed before the swap: os.SameFile compares device+inode, and Linux reuses
	// inode numbers freely, so on a filesystem that reports no creation time a
	// replacement can be genuinely undetectable. That residual is documented on
	// RemoveGuarded; assert the refusal only where detection is actually claimed.
	witness, werr := birthWitness(target)
	if werr != nil {
		t.Fatalf("creation-time witness: %v", werr)
	}
	tok, err := AuthorizeDeletion(DeletionRequest{
		Target: target, Boundary: root, Provenance: ProvenanceConfigProfile,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Between authorization and deletion, swap the directory for a fresh one with
	// a different inode identity.
	guardPreDeleteHook = func(path string) {
		_ = os.RemoveAll(path)
		mkdir(t, filepath.Join(path, "sentinel"))
	}
	defer func() { guardPreDeleteHook = nil }()

	err = RemoveGuarded(tok)
	if err == nil && !witness.ok {
		t.Skip("platform reports no inode creation time; replacement that reused the inode is an accepted, documented residual here")
	}
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("expected identity-change refusal, got %v", err)
	}
	// The replacement directory must survive (we refused to delete it).
	if _, statErr := os.Lstat(filepath.Join(target, "sentinel")); statErr != nil {
		t.Fatalf("replacement dir should be intact: %v", statErr)
	}
}

// The creation-time witness is what makes replacement detection survive inode
// reuse, so cover the discriminator directly rather than only through
// RemoveGuarded (which cannot force an inode to be reused on demand).
func TestBirthWitness_DistinguishesRecreatedDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	mkdir(t, target)

	before, err := birthWitness(target)
	if err != nil {
		t.Fatalf("witness: %v", err)
	}
	if !before.ok {
		t.Skip("platform reports no inode creation time")
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	mkdir(t, target)

	after, err := birthWitness(target)
	if err != nil {
		t.Fatalf("witness: %v", err)
	}
	if !after.ok {
		t.Fatal("witness became unavailable for the same filesystem")
	}
	if before.equal(after) {
		t.Fatal("recreated directory must not share the original's creation time")
	}
}

func TestRemoveGuarded_AncestorSymlinkRedirectRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlink fixture")
	}
	root := t.TempDir()
	realParent := mkdir(t, filepath.Join(root, "realparent"))
	target := mkdir(t, filepath.Join(realParent, "target"))
	tok, err := AuthorizeDeletion(DeletionRequest{
		Target: target, Boundary: root, Provenance: ProvenanceConfigProfile,
	})
	if err != nil {
		t.Fatal(err)
	}
	// After authorization, turn an ANCESTOR component into a symlink that still
	// resolves to the same target leaf inode. The leaf identity is unchanged, but
	// a path component is now a symlink — the pre-delete symlink-component
	// re-check must refuse.
	guardPreDeleteHook = func(path string) {
		moved := filepath.Join(root, "realparent_moved")
		_ = os.Rename(realParent, moved)
		_ = os.Symlink(moved, realParent)
	}
	defer func() { guardPreDeleteHook = nil }()

	if err := RemoveGuarded(tok); err == nil {
		t.Fatal("expected refusal when an ancestor becomes a symlink after authorization")
	}
	if _, statErr := os.Lstat(filepath.Join(root, "realparent_moved", "target")); statErr != nil {
		t.Fatalf("target should remain intact under the moved parent: %v", statErr)
	}
}

func TestRemoveGuarded_SymlinkReplacementRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlink fixture")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	mkdir(t, target)
	other := mkdir(t, filepath.Join(root, "other"))
	tok, err := AuthorizeDeletion(DeletionRequest{
		Target: target, Boundary: root, Provenance: ProvenanceConfigProfile,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Replace the authorized directory with a symlink after authorization.
	guardPreDeleteHook = func(path string) {
		_ = os.RemoveAll(path)
		_ = os.Symlink(other, path)
	}
	defer func() { guardPreDeleteHook = nil }()

	if err := RemoveGuarded(tok); err == nil {
		t.Fatal("expected refusal when target becomes a symlink")
	}
	// The symlink target contents must be untouched.
	if _, statErr := os.Lstat(other); statErr != nil {
		t.Fatalf("symlink destination should be intact: %v", statErr)
	}
}
