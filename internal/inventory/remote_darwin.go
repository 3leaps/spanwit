//go:build darwin

package inventory

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// sfDataless marks a File Provider placeholder whose contents are not local;
// opening it asks the provider to materialize them (SF_DATALESS, sys/stat.h).
const sfDataless = 0x40000000

func isDatalessDir(info os.FileInfo) bool {
	if info == nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Flags&sfDataless != 0
}

func fsTypeOf(path string) (string, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "", false
	}
	return unix.ByteSliceToString(st.Fstypename[:]), true
}
