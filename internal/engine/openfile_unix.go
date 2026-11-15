//go:build unix

package engine

import (
	"os"
	"syscall"
)

// openNoFollow opens path read-only and refuses a symlink at the final
// component (O_NOFOLLOW), closing the swap window between inspect and open.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) // #nosec G304 -- validated, symlink-free evidence path
}

// openNoFollowNonBlock opens path read-only without following a symlink and
// without blocking on a FIFO swapped in after an Lstat check.
func openNoFollowNonBlock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) // #nosec G304 -- caller checked a regular file and re-checks identity after open
}
