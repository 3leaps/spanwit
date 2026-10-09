//go:build unix

package space

import (
	"os"
	"syscall"
)

// fileAllocation reports a regular file's logical size and the bytes actually
// allocated to it. Sparse VM disks have far fewer allocated than logical bytes.
func fileAllocation(info os.FileInfo) (logical, allocated int64, ok bool) {
	stat, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat || !info.Mode().IsRegular() {
		return 0, 0, false
	}
	return info.Size(), stat.Blocks * 512, true
}
