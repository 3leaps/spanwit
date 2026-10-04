//go:build darwin

package capacity

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func filesystemBlockSize(st *unix.Statfs_t) int64 {
	return int64(st.Bsize)
}

func filesystemMount(st *unix.Statfs_t) string {
	return cString(st.Mntonname[:])
}

func filesystemVolumeID(st *unix.Statfs_t) string {
	return fmt.Sprintf("fsid:%d:%d", st.Fsid.Val[0], st.Fsid.Val[1])
}

func filesystemType(st *unix.Statfs_t) string {
	return cString(st.Fstypename[:])
}
