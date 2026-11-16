//go:build darwin

package cmd

import "golang.org/x/sys/unix"

// stateFSType names the filesystem holding path and reports whether the
// journal store accepts it (recordprims: apfs, hfs).
func stateFSType(path string) (string, bool, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "", false, err
	}
	name := unix.ByteSliceToString(st.Fstypename[:])
	return name, name == "apfs" || name == "hfs", nil
}
