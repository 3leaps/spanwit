package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/3leaps/spanwit/internal/config"
)

func writeRegular(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSignatureEvidence_PlainAncestorAndChildMatch(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	target := filepath.Join(app, "target")
	writeRegular(t, filepath.Join(app, "Cargo.toml"))
	if err := os.MkdirAll(filepath.Join(target, "debug"), 0o755); err != nil {
		t.Fatal(err)
	}
	sig := config.Signature{
		RequiredAncestorFiles: []string{"Cargo.toml"},
		AnyChildPaths:         []string{"debug"},
	}
	ev, ok := signatureEvidence(root, target, sig)
	if !ok {
		t.Fatalf("expected plain evidence to match; ev=%v", ev)
	}
}

func TestSignatureEvidence_SymlinkedAncestorRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlink fixture")
	}
	root := t.TempDir()
	app := filepath.Join(root, "app")
	target := filepath.Join(app, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// Planted symlink standing in for the required ancestor file.
	realFile := filepath.Join(root, "real-cargo.toml")
	writeRegular(t, realFile)
	if err := os.Symlink(realFile, filepath.Join(app, "Cargo.toml")); err != nil {
		t.Fatal(err)
	}
	sig := config.Signature{RequiredAncestorFiles: []string{"Cargo.toml"}}
	if _, ok := signatureEvidence(root, target, sig); ok {
		t.Fatal("symlinked required ancestor must not satisfy evidence")
	}
}

func TestSignatureEvidence_SymlinkedChildRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlink fixture")
	}
	root := t.TempDir()
	target := filepath.Join(root, "app", "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(target, "debug")); err != nil {
		t.Fatal(err)
	}
	sig := config.Signature{AnyChildPaths: []string{"debug"}}
	if _, ok := signatureEvidence(root, target, sig); ok {
		t.Fatal("symlinked child path must not satisfy evidence")
	}
}

func TestSignatureEvidence_IntermediateSymlinkChildRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlink fixture")
	}
	root := t.TempDir()
	target := filepath.Join(root, "app", "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// The child path "mid/marker" resolves through an intermediate symlink `mid`.
	realmid := filepath.Join(root, "realmid")
	writeRegular(t, filepath.Join(realmid, "marker"))
	if err := os.Symlink(realmid, filepath.Join(target, "mid")); err != nil {
		t.Fatal(err)
	}
	sig := config.Signature{AnyChildPaths: []string{"mid/marker"}}
	if _, ok := signatureEvidence(root, target, sig); ok {
		t.Fatal("child evidence resolving through an intermediate symlink must be rejected")
	}
}

func TestSignatureEvidence_DirectoryAsRequiredAncestorRejected(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	target := filepath.Join(app, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory must not satisfy a required ancestor FILE.
	if err := os.MkdirAll(filepath.Join(app, "markerdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	sig := config.Signature{RequiredAncestorFiles: []string{"markerdir"}}
	if _, ok := signatureEvidence(root, target, sig); ok {
		t.Fatal("a directory must not satisfy a required ancestor file")
	}
}

func TestSignatureEvidence_MaliciousEvidenceNamesFailClosed(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "app", "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []config.Signature{
		{RequiredAncestorFiles: []string{"/etc/passwd"}},
		{RequiredAncestorFiles: []string{"../../../../etc/passwd"}},
		{AnyChildPaths: []string{"../sibling"}},
		{AnyChildPaths: []string{"/tmp"}},
	}
	for i, sig := range cases {
		if _, ok := signatureEvidence(root, target, sig); ok {
			t.Fatalf("case %d: absolute/escape evidence name must fail closed", i)
		}
	}
}
