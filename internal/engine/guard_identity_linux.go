//go:build linux

package engine

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// birthWitness reads the inode creation time via statx(STATX_BTIME).
//
// os.SameFile compares only device+inode, which Linux readily reuses: a
// directory removed and immediately recreated frequently lands on the same
// inode number, so device+inode alone cannot distinguish the authorized
// directory from a replacement. Creation time is fixed for an inode's life, so
// it discriminates where device+inode cannot — and, unlike mtime/ctime, it does
// not move when the directory's own entries change, so ordinary activity
// between authorization and deletion does not cause a spurious refusal.
//
// A kernel or filesystem that does not report btime yields ok=false rather than
// an error; the caller then falls back to the device+inode comparison and the
// documented residual applies.
func birthWitness(path string) (birthTime, error) {
	var stx unix.Statx_t
	// NOFOLLOW: the leaf has already been rejected as a symlink, and the witness
	// must describe that directory itself rather than anything it might resolve to.
	err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW|unix.AT_STATX_SYNC_AS_STAT, unix.STATX_BTIME, &stx)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EPERM) {
			return birthTime{}, nil
		}
		return birthTime{}, fmt.Errorf("statx %s: %w", path, err)
	}
	if stx.Mask&unix.STATX_BTIME == 0 {
		return birthTime{}, nil
	}
	return birthTime{sec: stx.Btime.Sec, nsec: int64(stx.Btime.Nsec), ok: true}, nil
}
