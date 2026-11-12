//go:build darwin

package daemon

import "golang.org/x/sys/unix"

func peerUID(fd int) (uint32, error) {
	c, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, err
	}
	if c.Version != 0 /* XUCRED_VERSION in Darwin sys/ucred.h */ {
		return 0, unix.EACCES
	}
	return c.Uid, nil
}
