package corpus

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Capabilities records which adversarial shape elements the current platform
// and user can actually produce.
//
// Every field is set by attempting the operation in a scratch directory, not
// by branching on GOOS. A guess about what a filesystem supports is exactly
// the kind of unverified claim these fixtures exist to catch: tmpfs, APFS,
// ext4, and a CI container running as root each differ, and the difference is
// not predictable from the OS name alone.
type Capabilities struct {
	// Symlinks reports whether symbolic links can be created.
	Symlinks bool

	// HardLinks reports whether additional directory entries can be linked
	// to an existing inode.
	HardLinks bool

	// SparseFiles reports whether a file can allocate materially fewer
	// blocks than its apparent size.
	SparseFiles bool

	// PermissionEnforcement reports whether mode bits actually deny access
	// to the building user. False when running as root, where a 0000
	// directory remains readable and a "should be unreadable" fixture would
	// silently become readable.
	PermissionEnforcement bool

	// AllocatedSizeObservable reports whether allocated block counts can be
	// read for a file, which sparse-file accounting checks require.
	AllocatedSizeObservable bool

	// Reasons explains each unavailable capability, keyed by capability name.
	Reasons map[string]string
}

// Capability names used in Reasons and in Omission.Element values.
const (
	CapSymlinks              = "symlinks"
	CapHardLinks             = "hard-links"
	CapSparseFiles           = "sparse-files"
	CapPermissionEnforcement = "permission-enforcement"
	CapAllocatedSize         = "allocated-size-observable"
)

// Unavailable lists the capability names that probing found missing, sorted.
func (c Capabilities) Unavailable() []string {
	var out []string
	for name, reason := range c.Reasons {
		if reason != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Summary renders the capability set as a single line for embedding beside a
// measurement or a manifest.
func (c Capabilities) Summary() string {
	missing := c.Unavailable()
	if len(missing) == 0 {
		return "all fixture capabilities available"
	}
	return "unavailable: " + strings.Join(missing, ", ")
}

// Probe determines fixture capabilities by performing each operation in a
// scratch directory created under dir. The scratch directory is removed before
// returning.
func Probe(dir string) (Capabilities, error) {
	scratch, err := os.MkdirTemp(dir, "corpus-probe-")
	if err != nil {
		return Capabilities{}, fmt.Errorf("corpus: create probe dir: %w", err)
	}
	defer func() {
		// Best effort: restore any mode we changed so removal succeeds.
		_ = filepath.WalkDir(scratch, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
		_ = os.RemoveAll(scratch)
	}()

	caps := Capabilities{Reasons: map[string]string{}}

	base := filepath.Join(scratch, "base")
	if err := os.WriteFile(base, []byte("probe"), 0o600); err != nil {
		return Capabilities{}, fmt.Errorf("corpus: write probe file: %w", err)
	}

	// Symlinks.
	if err := os.Symlink(base, filepath.Join(scratch, "link")); err != nil {
		caps.Reasons[CapSymlinks] = errReason(err)
	} else {
		caps.Symlinks = true
	}

	// Hard links.
	if err := os.Link(base, filepath.Join(scratch, "hardlink")); err != nil {
		caps.Reasons[CapHardLinks] = errReason(err)
	} else {
		caps.HardLinks = true
	}

	// Allocated size observability and sparseness, checked together: a
	// sparse claim we cannot measure is a claim we should not make.
	sparsePath := filepath.Join(scratch, "sparse")
	allocated, apparent, err := probeSparse(sparsePath)
	switch {
	case err != nil:
		caps.Reasons[CapAllocatedSize] = errReason(err)
		caps.Reasons[CapSparseFiles] = "allocated size not observable"
	case allocated < 0:
		caps.Reasons[CapAllocatedSize] = "platform does not expose allocated block counts"
		caps.Reasons[CapSparseFiles] = "allocated size not observable"
	default:
		caps.AllocatedSizeObservable = true
		// Require a large margin: a filesystem that allocates most of the
		// range is not giving us a sparse file regardless of what it
		// reports.
		if allocated*2 < apparent {
			caps.SparseFiles = true
		} else {
			caps.Reasons[CapSparseFiles] = fmt.Sprintf(
				"filesystem allocated %d of %d apparent bytes", allocated, apparent)
		}
	}

	// Permission enforcement.
	if os.Geteuid() == 0 {
		caps.Reasons[CapPermissionEnforcement] = "running as root; mode bits do not deny access"
	} else {
		denied, err := probePermissionDenial(scratch)
		switch {
		case err != nil:
			caps.Reasons[CapPermissionEnforcement] = errReason(err)
		case !denied:
			caps.Reasons[CapPermissionEnforcement] = "filesystem does not enforce mode bits"
		default:
			caps.PermissionEnforcement = true
		}
	}

	return caps, nil
}

// probeSparse creates a file with a large hole and reports its allocated and
// apparent sizes. An allocated size of -1 means the platform does not expose
// block counts.
func probeSparse(path string) (allocated, apparent int64, err error) {
	const apparentSize = 8 << 20 // 8 MiB of hole, one byte of data

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Seek(apparentSize-1, 0); err != nil {
		return 0, 0, err
	}
	if _, err := f.Write([]byte{0}); err != nil {
		return 0, 0, err
	}
	if err := f.Sync(); err != nil {
		return 0, 0, err
	}

	info, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}
	return allocatedBytes(info), info.Size(), nil
}

// probePermissionDenial creates an unreadable directory and reports whether
// listing it actually fails.
func probePermissionDenial(scratch string) (bool, error) {
	dir := filepath.Join(scratch, "noaccess")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(dir, "hidden"), []byte("x"), 0o600); err != nil {
		return false, err
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		return false, err
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	if _, err := os.ReadDir(dir); err != nil {
		return true, nil
	}
	return false, nil
}

// errReason renders an error as a short reason string.
func errReason(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
