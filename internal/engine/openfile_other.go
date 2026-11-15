//go:build !unix

package engine

import "os"

// openNoFollow opens path read-only. Platforms without O_NOFOLLOW rely on the
// caller's Lstat-then-SameFile check to reject a file swapped after
// inspection.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path) // #nosec G304 -- validated, symlink-free evidence path
}

// openNoFollowNonBlock opens path read-only; callers check a regular file
// before and after opening.
func openNoFollowNonBlock(path string) (*os.File, error) {
	return os.Open(path) // #nosec G304 -- caller checked a regular file and re-checks identity after open
}
