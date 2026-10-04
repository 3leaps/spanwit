//go:build unix

package observe

import (
	"os"
	"syscall"
)

func tryLockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
		return errLockBusy
	}
	return err
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
