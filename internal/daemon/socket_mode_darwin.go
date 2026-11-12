//go:build darwin

package daemon

import "golang.org/x/sys/unix"

func setSocketMode(path string) error {
	return unix.Fchmodat(unix.AT_FDCWD, path, 0600, unix.AT_SYMLINK_NOFOLLOW)
}
