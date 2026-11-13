//go:build darwin

package userservice

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRenderDarwinPlistPassesPlutil(t *testing.T) {
	plan, err := Render(fixture(PlatformDarwin))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "service.plist")
	if err := os.WriteFile(file, []byte(plan.Content), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("/usr/bin/plutil", "-lint", file).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v: %s", err, output)
	}
}
