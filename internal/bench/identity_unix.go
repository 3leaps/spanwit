//go:build unix

package bench

import (
	"io/fs"
	"syscall"
)

// identityOf returns a file's device and inode, which identify it regardless
// of the path it was reached by.
func identityOf(info fs.FileInfo) (fileIdentity, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, false
	}
	return fileIdentity{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true
}
