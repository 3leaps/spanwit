//go:build !unix && !windows

package observe

import (
	"os"
)

// Portable fallback: exclusive create of a sibling holder file.
// Not as strong as flock/LockFileEx, but keeps non-unix/non-windows builds compiling.
func tryLockFile(f *os.File) error {
	holder := f.Name() + ".held"
	h, err := os.OpenFile(holder, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return errLockBusy
		}
		return err
	}
	// Keep holder open via the main file's path marker stored in unused content.
	_, _ = h.WriteString("held\n")
	// Close holder handle but leave exclusive create semantics via file existence —
	// pair with unlockFile removing the holder. Between create and write another
	// process won't O_EXCL-succeed.
	_ = h.Close()
	return nil
}

func unlockFile(f *os.File) error {
	return os.Remove(f.Name() + ".held")
}
