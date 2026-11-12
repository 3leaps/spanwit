//go:build darwin || linux

package daemon

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
)

var ErrBusy = errors.New("writer_busy")

// Ownership is held across application transitions, not just journal appends.
// The lock inode is never removed: removing it could admit two writers.
type Ownership struct {
	file *os.File
	Dir  string
}

func Acquire(dir string) (*Ownership, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("state directory must be absolute")
	}
	// Traverse the requested path from a pinned root descriptor. Never resolve
	// a symlink supplied by another user, even if its target would be private.
	canonical := filepath.Clean(dir)
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	components := strings.Split(strings.TrimPrefix(canonical, "/"), "/")
	for _, component := range components {
		if component == "" {
			return nil, fmt.Errorf("invalid state directory")
		}
		var parent unix.Stat_t
		if err = unix.Fstat(fd, &parent); err != nil {
			return nil, err
		}
		if err = admitAncestor(parent); err != nil {
			return nil, err
		}
		next, e := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(e, unix.ENOENT) {
			if e = unix.Mkdirat(fd, component, 0700); e != nil && !errors.Is(e, unix.EEXIST) {
				return nil, e
			}
			next, e = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if e != nil {
			return nil, e
		}
		_ = unix.Close(fd)
		fd = next
	}
	var ds unix.Stat_t
	if err = unix.Fstat(fd, &ds); err != nil {
		return nil, err
	}
	if ds.Uid != uint32(os.Getuid()) || ds.Mode&0777 != 0700 {
		return nil, fmt.Errorf("state directory must be owned by this user with mode 0700")
	}
	lock, err := unix.Openat(fd, "coordinator.lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(lock), "coordinator.lock")
	var fs unix.Stat_t
	if err = unix.Fstat(lock, &fs); err != nil {
		_ = f.Close()
		return nil, err
	}
	if fs.Uid != uint32(os.Getuid()) || fs.Mode&unix.S_IFMT != unix.S_IFREG || fs.Mode&0777 != 0600 || fs.Nlink != 1 {
		_ = f.Close()
		return nil, fmt.Errorf("unsafe coordinator lock")
	}
	if err = unix.Flock(lock, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return &Ownership{file: f, Dir: canonical}, nil
}
func (o *Ownership) Close() error {
	if o.file == nil {
		return nil
	}
	f := o.file
	o.file = nil
	return f.Close()
}

func admitAncestor(st unix.Stat_t) error {
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || (st.Uid != 0 && st.Uid != uint32(os.Getuid())) {
		return fmt.Errorf("untrusted state ancestor")
	}
	if st.Mode&0022 != 0 && (st.Uid != 0 || st.Mode&unix.S_ISVTX == 0) {
		return fmt.Errorf("writable state ancestor")
	}
	return nil
}
