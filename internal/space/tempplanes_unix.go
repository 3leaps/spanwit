//go:build unix

package space

import (
	"os"
	"syscall"
)

// admitTempPlane admits a candidate temp root only when its final component is
// a real directory (not a symlink). A per-user plane must also be owned by the
// real uid, so a redirected TMPDIR cannot point the probe at someone else's
// tree.
func admitTempPlane(path, kind string) (dirIdentity, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return dirIdentity{}, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return dirIdentity{}, false
	}
	if kind == TempPlaneKindUser && int(st.Uid) != os.Getuid() {
		return dirIdentity{}, false
	}
	return dirIdentity{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true //nolint:unconvert // Dev width differs by platform
}

// statDirIdentity returns the identity of path (following symlinks, since the
// analysis root is already admitted by the caller).
func statDirIdentity(path string) (dirIdentity, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return dirIdentity{}, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return dirIdentity{}, false
	}
	return dirIdentity{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true //nolint:unconvert // Dev width differs by platform
}

func deviceOf(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true //nolint:unconvert // Dev width differs by platform
}
