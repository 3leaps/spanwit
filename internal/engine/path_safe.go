package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ValidatePathNoSymlinkComponents rejects paths where any path component is a
// symlink, except for a short allowlist of known platform volume aliases
// (macOS /var → /private/var and /tmp → /private/tmp). User-controlled
// shallow symlinks such as /tmp/evil-link are rejected.
func ValidatePathNoSymlinkComponents(path string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}
	abs = filepath.Clean(abs)
	if abs == string(filepath.Separator) {
		return fmt.Errorf("refusing filesystem root")
	}

	var chain []string
	cur := abs
	for {
		chain = append(chain, cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	for i := len(chain) - 1; i >= 0; i-- {
		p := chain[i]
		info, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("stat path component %s: %w", p, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if isTrustedPlatformSymlinkComponent(p) {
			continue
		}
		return fmt.Errorf("refusing symlink path component: %s", p)
	}
	return nil
}

// isTrustedPlatformSymlinkComponent allows only well-known OS volume aliases.
// It deliberately does NOT allow arbitrary shallow symlinks under /tmp or /var.
func isTrustedPlatformSymlinkComponent(p string) bool {
	p = filepath.Clean(p)
	switch runtime.GOOS {
	case "darwin", "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
		switch p {
		case "/tmp", "/var", "/private/tmp", "/private/var":
			return true
		}
	}
	return false
}
