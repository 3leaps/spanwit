//go:build !unix

package corpus

import "io/fs"

// allocatedBytes reports that allocated block counts are unavailable off unix.
// Callers must treat -1 as "not observable" rather than as zero allocation:
// reporting an unmeasured figure as zero would turn a missing measurement into
// a false sparseness claim.
func allocatedBytes(_ fs.FileInfo) int64 {
	return -1
}

// inodeIdentity reports that inode identity is unavailable off unix, so
// hard-link grouping cannot be verified from the filesystem on these
// platforms. The manifest's own link groups remain authoritative, since they
// come from the construction plan.
func inodeIdentity(_ fs.FileInfo) (dev uint64, ino uint64, ok bool) {
	return 0, 0, false
}
