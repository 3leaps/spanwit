package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates the whole package from the real user's home: every
// --execute takes the coordinator store under ~/.local/state, which must
// never be the developer's live state during tests.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "spanwit-cmd-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	home, _ = filepath.EvalSymlinks(home)
	_ = os.Chmod(home, 0o700)
	_ = os.Setenv("HOME", home)
	_ = os.Unsetenv("XDG_STATE_HOME")
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
