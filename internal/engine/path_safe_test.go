package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidatePathNoSymlinkComponents_Plain(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "a", "b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePathNoSymlinkComponents(dir); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePathNoSymlinkComponents_RejectsAncestorSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture simplified for unix")
	}
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside", "app")
	if err := os.MkdirAll(filepath.Join(outside, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "root", "link")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(link, "target")
	if err := ValidatePathNoSymlinkComponents(viaLink); err == nil {
		t.Fatal("expected rejection of symlink ancestor")
	}
}

func TestValidatePathNoSymlinkComponents_RejectsShallowUserSymlinkUnderTmp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix")
	}
	// Symlinked ancestor directory must be rejected (not a trusted platform alias).
	base, err := os.MkdirTemp("", "spanwit-shallow-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(base); err != nil {
			t.Errorf("cleanup temp dir: %v", err)
		}
	}()

	outside := filepath.Join(base, "outside", "app")
	if err := os.MkdirAll(filepath.Join(outside, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Link next to outside: base/link → outside/app
	link := filepath.Join(base, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	via := filepath.Join(link, "target")
	if err := ValidatePathNoSymlinkComponents(via); err == nil {
		t.Fatal("user symlink component must be rejected even when shallow under temp")
	}
}

func TestValidatePathNoSymlinkComponents_RejectsLeafSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix")
	}
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "leaflink")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePathNoSymlinkComponents(link); err == nil {
		t.Fatal("expected leaf symlink rejection")
	}
}

func TestTrustedPlatformSymlinkComponents(t *testing.T) {
	if !isTrustedPlatformSymlinkComponent("/tmp") && runtime.GOOS != "windows" {
		// On darwin/linux /tmp is trusted
		if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
			t.Fatal("/tmp should be trusted platform alias")
		}
	}
	if isTrustedPlatformSymlinkComponent("/tmp/evil-link") {
		t.Fatal("/tmp/evil-link must not be trusted")
	}
	if isTrustedPlatformSymlinkComponent("/var/folders/xx") {
		t.Fatal("only exact /var is trusted, not children")
	}
}
