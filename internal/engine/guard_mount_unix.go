//go:build unix

package engine

import (
	"fmt"
	"os"
	"syscall"
)

// sameMount reports whether boundary and target reside on the same device.
// Both paths have already had their symlink components rejected, so os.Stat is
// equivalent to Lstat here. When device identity cannot be determined it returns
// true (do not block) rather than fabricating a mismatch.
func sameMount(boundary, target string) (bool, error) {
	bDev, ok, err := deviceID(boundary)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	tDev, ok, err := deviceID(target)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	return bDev == tDev, nil
}

func deviceID(path string) (uint64, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false, fmt.Errorf("stat %s: %w", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false, nil
	}
	return uint64(st.Dev), true, nil
}
