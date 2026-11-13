//go:build darwin || linux

package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var ErrBusy = errors.New("writer_busy")

const maxExecutableBytes = 256 << 20

// ExecutableIdentity binds a verified on-disk executable path to its observed
// content digest and inode identity.
type ExecutableIdentity struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Dev    string `json:"dev"`
	Ino    string `json:"ino"`
}

// Ownership is held across application transitions, not just journal appends.
// The lock inode is never removed: removing it could admit two writers.
type Ownership struct {
	file *os.File
	Dir  string
}

func Acquire(dir string) (*Ownership, error) {
	return AcquireNamedLock(dir, "coordinator.lock")
}

// AcquireNamedLock acquires an exclusive flock on lockName inside dir after
// verifying every ancestor without following symlinks. Service-control actions
// use a distinct lock name so they never contend with or masquerade as the
// running coordinator's writer lock.
func AcquireNamedLock(dir, lockName string) (*Ownership, error) {
	if lockName == "" || strings.Contains(lockName, "/") || lockName == "." || lockName == ".." {
		return nil, fmt.Errorf("invalid lock name")
	}
	fd, canonical, err := OpenVerifiedDir(dir, true, 0700)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	lock, err := unix.Openat(fd, lockName, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(lock), lockName)
	var fs unix.Stat_t
	if err = unix.Fstat(lock, &fs); err != nil {
		_ = f.Close()
		return nil, err
	}
	if int64(fs.Uid) != int64(os.Getuid()) || fs.Mode&unix.S_IFMT != unix.S_IFREG || fs.Mode&0777 != 0600 || fs.Nlink != 1 {
		_ = f.Close()
		return nil, fmt.Errorf("unsafe %s", lockName)
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

// OpenVerifiedDir traverses dir from "/" using O_NOFOLLOW and verifies ancestor
// ownership/permissions. When create is true, missing directories are created
// with mode (which must be 0700 or 0755); when create is false, traversal is
// strictly read-only. The returned descriptor must be closed by the caller.
func OpenVerifiedDir(dir string, create bool, mode uint32) (int, string, error) {
	if !filepath.IsAbs(dir) {
		return -1, "", fmt.Errorf("directory must be absolute")
	}
	canonical := filepath.Clean(dir)
	if canonical == "/" {
		return -1, "", fmt.Errorf("invalid target directory")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", err
	}
	components := strings.Split(strings.TrimPrefix(canonical, "/"), "/")
	for _, component := range components {
		if component == "" {
			_ = unix.Close(fd)
			return -1, "", fmt.Errorf("invalid target directory")
		}
		var parent unix.Stat_t
		if err = unix.Fstat(fd, &parent); err != nil {
			_ = unix.Close(fd)
			return -1, "", err
		}
		if err = admitAncestor(parent); err != nil {
			_ = unix.Close(fd)
			return -1, "", err
		}
		next, e := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(e, unix.ENOENT) && create {
			if e = unix.Mkdirat(fd, component, mode); e != nil && !errors.Is(e, unix.EEXIST) {
				_ = unix.Close(fd)
				return -1, "", e
			}
			next, e = unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if e != nil {
			_ = unix.Close(fd)
			return -1, "", e
		}
		_ = unix.Close(fd)
		fd = next
	}
	var ds unix.Stat_t
	if err = unix.Fstat(fd, &ds); err != nil {
		_ = unix.Close(fd)
		return -1, "", err
	}
	if mode == 0700 {
		if int64(ds.Uid) != int64(os.Getuid()) || ds.Mode&0777 != 0700 {
			_ = unix.Close(fd)
			return -1, "", fmt.Errorf("state directory must be owned by this user with mode 0700")
		}
	} else {
		if int64(ds.Uid) != int64(os.Getuid()) || ds.Mode&0022 != 0 {
			_ = unix.Close(fd)
			return -1, "", fmt.Errorf("directory must be owned by this user and not group- or world-writable")
		}
	}
	return fd, canonical, nil
}

// InspectExecutable verifies that execPath is a clean, symlink-free absolute
// path in a non-writable parent directory (owned by root or the current user)
// pointing to a non-writable regular executable file, and computes its SHA-256.
func InspectExecutable(execPath string) (ExecutableIdentity, error) {
	if !filepath.IsAbs(execPath) || filepath.Clean(execPath) != execPath {
		return ExecutableIdentity{}, fmt.Errorf("executable must be a clean absolute path")
	}
	dir := filepath.Dir(execPath)
	base := filepath.Base(execPath)
	if base == "" || base == "." || base == "/" {
		return ExecutableIdentity{}, fmt.Errorf("invalid executable path")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return ExecutableIdentity{}, err
	}
	defer func() { _ = unix.Close(fd) }()
	if dir != "/" {
		for _, component := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
			if component == "" {
				return ExecutableIdentity{}, fmt.Errorf("invalid executable directory")
			}
			var parent unix.Stat_t
			if err = unix.Fstat(fd, &parent); err != nil {
				return ExecutableIdentity{}, err
			}
			if err = admitAncestor(parent); err != nil {
				return ExecutableIdentity{}, err
			}
			next, e := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if e != nil {
				return ExecutableIdentity{}, e
			}
			_ = unix.Close(fd)
			fd = next
		}
	}
	var dirStat unix.Stat_t
	if err = unix.Fstat(fd, &dirStat); err != nil {
		return ExecutableIdentity{}, err
	}
	if dirStat.Mode&unix.S_IFMT != unix.S_IFDIR || (dirStat.Uid != 0 && int64(dirStat.Uid) != int64(os.Getuid())) || dirStat.Mode&0022 != 0 {
		return ExecutableIdentity{}, fmt.Errorf("executable parent directory must be owned by user or root and not group- or world-writable")
	}
	fileFD, err := unix.Openat(fd, base, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return ExecutableIdentity{}, err
	}
	file := os.NewFile(uintptr(fileFD), execPath)
	defer func() { _ = file.Close() }()
	var st unix.Stat_t
	if err = unix.Fstat(fileFD, &st); err != nil {
		return ExecutableIdentity{}, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return ExecutableIdentity{}, fmt.Errorf("executable must be a regular file")
	}
	if st.Uid != 0 && int64(st.Uid) != int64(os.Getuid()) {
		return ExecutableIdentity{}, fmt.Errorf("executable must be owned by this user or root")
	}
	if st.Mode&0022 != 0 {
		return ExecutableIdentity{}, fmt.Errorf("executable must not be group- or world-writable")
	}
	if st.Mode&(unix.S_ISUID|unix.S_ISGID) != 0 {
		return ExecutableIdentity{}, fmt.Errorf("executable must not have setuid or setgid bits")
	}
	if st.Mode&0111 == 0 {
		return ExecutableIdentity{}, fmt.Errorf("executable must have execute permission")
	}
	if st.Size <= 0 || st.Size > maxExecutableBytes {
		return ExecutableIdentity{}, fmt.Errorf("executable size out of bounds")
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, maxExecutableBytes+1))
	if err != nil {
		return ExecutableIdentity{}, err
	}
	if written != st.Size {
		return ExecutableIdentity{}, fmt.Errorf("executable changed during verification")
	}
	return ExecutableIdentity{
		Path:   execPath,
		SHA256: hex.EncodeToString(hasher.Sum(nil)),
		Size:   st.Size,
		Dev:    fmt.Sprintf("%d", st.Dev),
		Ino:    fmt.Sprintf("%d", st.Ino),
	}, nil
}

// RuntimeConfigDigest returns the canonical SHA-256 digest of the daemon's
// runtime invocation parameters so service health checks can confirm the
// running instance matches the installed receipt.
func RuntimeConfigDigest(stateDir, samplePath string, interval time.Duration, roots []string) (string, error) {
	if !filepath.IsAbs(stateDir) || !filepath.IsAbs(samplePath) {
		return "", fmt.Errorf("runtime config paths must be absolute")
	}
	if interval < time.Minute {
		return "", fmt.Errorf("sample interval must be at least 60s")
	}
	cleanedRoots := make([]string, len(roots))
	for i, r := range roots {
		if !filepath.IsAbs(r) {
			return "", fmt.Errorf("attribution root must be absolute")
		}
		cleanedRoots[i] = filepath.Clean(r)
	}
	payload := struct {
		Version  int      `json:"version"`
		StateDir string   `json:"state_dir"`
		Path     string   `json:"path"`
		Interval string   `json:"interval"`
		Roots    []string `json:"roots"`
	}{
		Version:  1,
		StateDir: filepath.Clean(stateDir),
		Path:     filepath.Clean(samplePath),
		Interval: interval.String(),
		Roots:    cleanedRoots,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
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
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || (st.Uid != 0 && int64(st.Uid) != int64(os.Getuid())) {
		return fmt.Errorf("untrusted state ancestor")
	}
	if st.Mode&0022 != 0 && (st.Uid != 0 || st.Mode&unix.S_ISVTX == 0) {
		return fmt.Errorf("writable state ancestor")
	}
	return nil
}
