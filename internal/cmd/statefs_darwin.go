//go:build darwin

package cmd

import "syscall"

// stateFSType names the filesystem holding path and reports whether the
// journal store accepts it (recordprims: apfs, hfs).
func stateFSType(path string) (string, bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", false, err
	}
	b := make([]byte, 0, len(st.Fstypename))
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	name := string(b)
	return name, name == "apfs" || name == "hfs", nil
}
