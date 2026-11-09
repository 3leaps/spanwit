//go:build !unix

package engine

import "io/fs"

// AllocatedSizesSupported reports whether this build can read allocated
// bytes at all. This build never observes allocation.
const AllocatedSizesSupported = false

func allocatedBytes(_ fs.FileInfo) (int64, bool) { return 0, false }
