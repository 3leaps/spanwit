package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Temp-root admission: whether a per-user temp directory may become a
// verified walk root, widening delete reach. Admission is a property of the
// filesystem now, never of a report, so space and prune both call
// AdmitTempRoot (prune re-admits each carried temp plan's root at prune time).
// The kind label "user temp" is not authority; only these checks are.

// Temp-root refusal reasons (closed vocabulary; see the vocabulary registry).
const (
	TempRootNotOwned           = "not_owned"
	TempRootSharedPath         = "shared_path"
	TempRootGroupOtherWritable = "group_or_other_writable"
	TempRootSticky             = "sticky"
	TempRootSymlink            = "symlink"
	TempRootNotDirectory       = "not_directory"
	TempRootOverlapsAnalysis   = "overlaps_analysis_root"
	TempRootUnsupported        = "unsupported_platform"
	TempRootNotRequested       = "not_requested"
	TempRootMode               = "mode"
	TempRootEuidRoot           = "euid_root"
	// TempRootNotUserTemp: at prune time the carried root is not the user's
	// os.TempDir(), recomputed now. A private directory is not a temp root.
	// Prune-warning only; never written to a report's temp_roots, so it is
	// not in the temp_root_reason report vocabulary.
	TempRootNotUserTemp = "not_user_temp"
)

// sharedTempPaths are never verified walk roots, nor is anything under or
// above them.
var sharedTempPaths = []string{"/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp"}

// TempRootAdmission is the outcome for one candidate temp root.
type TempRootAdmission struct {
	// Root is the canonical path walked and passed as the boundary when
	// admitted; the requested path otherwise.
	Root     string
	Admitted bool
	Reason   string
}

// AdmitTempRoot decides whether path may be a verified walk root, given the
// analysis root it must not overlap. It never resolves arbitrary symlinks:
// only the trusted OS volume aliases are rewritten.
func AdmitTempRoot(path, analysisRoot string) TempRootAdmission {
	return admitTempRoot(path, analysisRoot, runtime.GOOS, os.Geteuid(), os.Getuid(), os.Lstat)
}

func admitTempRoot(path, analysisRoot, goos string, euid, uid int, lstat func(string) (os.FileInfo, error)) TempRootAdmission {
	out := TempRootAdmission{Root: path}
	if goos != "darwin" && goos != "linux" {
		out.Reason = TempRootUnsupported
		return out
	}
	if euid == 0 {
		out.Reason = TempRootEuidRoot
		return out
	}
	canon := canonicalTempRoot(path, goos)
	out.Root = canon
	if isSharedTempPath(canon) {
		out.Reason = TempRootSharedPath
		return out
	}
	info, err := lstat(canon)
	if err != nil {
		out.Reason = TempRootNotDirectory
		return out
	}
	if info.Mode()&os.ModeSymlink != 0 {
		out.Reason = TempRootSymlink
		return out
	}
	if !info.IsDir() {
		out.Reason = TempRootNotDirectory
		return out
	}
	if err := ValidatePathNoSymlinkComponents(canon); err != nil {
		out.Reason = TempRootSymlink
		return out
	}
	if owner, ok := fileOwner(info); !ok || owner != uid {
		out.Reason = TempRootNotOwned
		return out
	}
	perm := info.Mode()
	switch {
	case perm&os.ModeSticky != 0:
		out.Reason = TempRootSticky
		return out
	case perm.Perm()&0o022 != 0:
		out.Reason = TempRootGroupOtherWritable
		return out
	case perm.Perm() != 0o700:
		out.Reason = TempRootMode
		return out
	}
	if parentUnsafe(canon, lstat) {
		out.Reason = TempRootGroupOtherWritable
		return out
	}
	if analysisRoot != "" {
		a := canonicalTempRoot(analysisRoot, goos)
		if canon == a || pathWithinDir(canon, a) || pathWithinDir(a, canon) {
			out.Reason = TempRootOverlapsAnalysis
			return out
		}
	}
	out.Admitted = true
	return out
}

// canonicalTempRoot cleans path and lexically rewrites the trusted macOS
// /tmp and /var aliases to /private/...; it resolves nothing else, so the
// walk and the ancestor-evidence search use one exact root string.
func canonicalTempRoot(p, goos string) string {
	p = filepath.Clean(p)
	if goos != "darwin" {
		return p
	}
	for _, alias := range []string{"/tmp", "/var"} {
		if p == alias || strings.HasPrefix(p, alias+"/") {
			return "/private" + p
		}
	}
	return p
}

// isSharedTempPath reports whether p equals, is under, or is an ancestor of a
// shared temp path.
func isSharedTempPath(p string) bool {
	for _, s := range sharedTempPaths {
		if p == s || pathWithinDir(p, s) || pathWithinDir(s, p) {
			return true
		}
	}
	return false
}

// parentUnsafe reports whether the root's parent is group- or other-writable
// without the sticky bit, which would let another user rename the root out
// from under the walk.
func parentUnsafe(root string, lstat func(string) (os.FileInfo, error)) bool {
	info, err := lstat(filepath.Dir(root))
	if err != nil {
		return true
	}
	m := info.Mode()
	return m.Perm()&0o022 != 0 && m&os.ModeSticky == 0
}

// pathWithinDir reports whether child is strictly under parent.
func pathWithinDir(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ReadmitCarriedTempRoot re-decides, at prune time, whether a temp plan's
// carried root may still be walked. It must equal the canonical os.TempDir()
// recomputed now (a report cannot nominate another private directory) and
// pass AdmitTempRoot now.
func ReadmitCarriedTempRoot(carried, analysisRoot string) TempRootAdmission {
	return readmitCarriedTempRoot(carried, analysisRoot, os.TempDir(), runtime.GOOS)
}

func readmitCarriedTempRoot(carried, analysisRoot, userTemp, goos string) TempRootAdmission {
	want := canonicalTempRoot(userTemp, goos)
	if canonicalTempRoot(carried, goos) != want {
		return TempRootAdmission{Root: carried, Reason: TempRootNotUserTemp}
	}
	return AdmitTempRoot(carried, analysisRoot)
}
