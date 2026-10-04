//go:build unix

package corpus

import (
	"io/fs"
	"syscall"
)

// blockUnit is the fixed 512-byte unit st_blocks is reported in, independent
// of the filesystem's own block size.
const blockUnit = 512

// allocatedBytes returns the bytes actually allocated to a file, or -1 when
// the platform does not expose block counts. It is the figure that differs
// from apparent size for sparse files, and the reason entry-size sums must
// never be presented as reclaimable space.
func allocatedBytes(info fs.FileInfo) int64 {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int64(st.Blocks) * blockUnit
}

// inodeIdentity returns the device and inode of a file, plus whether the
// platform supplied them. Hard-linked entries share an identity, which is what
// makes counting their bytes once possible.
func inodeIdentity(info fs.FileInfo) (dev uint64, ino uint64, ok bool) {
	st, sysOK := info.Sys().(*syscall.Stat_t)
	if !sysOK {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true
}
