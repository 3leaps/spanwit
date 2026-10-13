//go:build !unix

package engine

import "os"

// openNoFollow opens path read-only. Platforms without O_NOFOLLOW rely on the
// caller's Lstat-then-SameFile check to reject a file swapped after
// inspection.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path) // #nosec G304 -- validated, symlink-free evidence path
}
