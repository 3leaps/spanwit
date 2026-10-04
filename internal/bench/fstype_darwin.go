//go:build darwin

package bench

import "golang.org/x/sys/unix"

// filesystemType reads the filesystem name for path. Darwin reports it
// directly as a string, so no magic-number mapping is needed.
func filesystemType(path string) (string, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "", false
	}
	name := cstring(st.Fstypename[:])
	if name == "" {
		return "", false
	}
	return name, true
}

// deviceClass cannot be determined on Darwin without IOKit, which this harness
// does not link. An operator must declare it.
//
// Returning false here rather than guessing "ssd" is deliberate: nearly every
// Mac this runs on will be flash-backed, and that near-certainty is exactly
// what would make a wrong guess on an external rotating disk survive review
// unnoticed.
func deviceClass(_ string) (DeviceClass, bool) {
	return "", false
}
