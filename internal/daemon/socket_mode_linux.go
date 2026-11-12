//go:build linux

package daemon

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

// O_PATH pins the socket inode even on kernels without fchmodat2. The procfs
// descriptor link addresses that inode, not the attacker-replaceable pathname.
// NOFOLLOW plus fstat rejects a final symlink rather than chmodding its target.
// Linux without procfs fails closed.
func setSocketMode(path string) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("unsafe socket inode")
	}
	return unix.Chmod(fmt.Sprintf("/proc/self/fd/%d", fd), 0600)
}
