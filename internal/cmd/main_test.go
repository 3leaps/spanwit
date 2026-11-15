package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// realHome is the user's home before TestMain replaces HOME. Fixtures that
// need a per-user directory outside shared temp (temp-root tests) use it.
var realHome string

// TestMain isolates the whole package from the real user's home: every
// --execute takes the coordinator store under ~/.local/state, which must
// never be the developer's live state during tests. Git keeps reading the
// real global config (repository trust for test builds).
func TestMain(m *testing.M) {
	realHome, _ = os.UserHomeDir()
	if realHome != "" && os.Getenv("GIT_CONFIG_GLOBAL") == "" {
		_ = os.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(realHome, ".gitconfig"))
	}
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
