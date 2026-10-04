//go:build unix && !darwin && !linux

package capacity

import "golang.org/x/sys/unix"

func filesystemBlockSize(st *unix.Statfs_t) int64 {
	return int64(st.Bsize)
}

func filesystemMount(*unix.Statfs_t) string {
	return ""
}

func filesystemVolumeID(*unix.Statfs_t) string {
	return ""
}

func filesystemType(*unix.Statfs_t) string {
	return ""
}
