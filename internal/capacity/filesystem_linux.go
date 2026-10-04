//go:build linux

package capacity

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func filesystemBlockSize(st *unix.Statfs_t) int64 {
	if st.Bsize > 0 {
		return int64(st.Bsize)
	}
	return int64(st.Frsize)
}

func filesystemMount(*unix.Statfs_t) string {
	return ""
}

func filesystemVolumeID(st *unix.Statfs_t) string {
	return fmt.Sprintf("fsid:%d:%d", st.Fsid.Val[0], st.Fsid.Val[1])
}

func filesystemType(*unix.Statfs_t) string {
	return ""
}
