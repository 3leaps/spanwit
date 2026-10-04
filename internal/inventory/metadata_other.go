//go:build !unix

package inventory

import "io/fs"

func metadataOf(_ fs.FileInfo) fileMetadata {
	return fileMetadata{}
}

// AllocatedSizesSupported reports whether this platform's file metadata can
// carry allocated sizes at all. This build never observes allocation.
const AllocatedSizesSupported = false
