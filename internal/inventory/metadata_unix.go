//go:build unix

package inventory

import (
	"io/fs"
	"strconv"
	"syscall"
)

func metadataOf(info fs.FileInfo) fileMetadata {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileMetadata{}
	}
	// #nosec G115 -- device and inode numbers are identities compared only for equality; the conversion keeps the bit pattern.
	dev, ino := uint64(st.Dev), uint64(st.Ino)
	meta := fileMetadata{
		deviceID: strconv.FormatUint(dev, 10),
		fileID:   strconv.FormatUint(ino, 10),
	}
	if allocated, ok := blockBytes(int64(st.Blocks)); ok {
		meta.allocated = &allocated
	}
	return meta
}

// AllocatedSizesSupported reports whether this platform's file metadata can
// carry allocated sizes at all. Individual filesystems may still omit them.
const AllocatedSizesSupported = true
