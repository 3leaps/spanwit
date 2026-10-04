//go:build darwin

package engine

import (
	"fmt"
	"os"
	"syscall"
)

// birthWitness reads the inode creation time from the darwin stat birthtime
// field. See the linux implementation for why creation time is the witness of
// record: device+inode alone cannot detect a replacement under inode reuse, and
// mtime/ctime move whenever the directory's own entries change.
func birthWitness(path string) (birthTime, error) {
	// Lstat: the leaf has already been rejected as a symlink, and the witness must
	// describe that directory itself rather than anything it might resolve to.
	info, err := os.Lstat(path)
	if err != nil {
		return birthTime{}, fmt.Errorf("stat %s: %w", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return birthTime{}, nil
	}
	return birthTime{sec: st.Birthtimespec.Sec, nsec: int64(st.Birthtimespec.Nsec), ok: true}, nil
}
