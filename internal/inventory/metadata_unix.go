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
	allocated, ok := blockBytes(int64(st.Blocks))
	if !ok {
		return fileMetadata{
			deviceID: strconv.FormatUint(uint64(st.Dev), 10),
			fileID:   strconv.FormatUint(uint64(st.Ino), 10),
		}
	}
	return fileMetadata{
		allocated: &allocated,
		deviceID:  strconv.FormatUint(uint64(st.Dev), 10),
		fileID:    strconv.FormatUint(uint64(st.Ino), 10),
	}
}

// AllocatedSizesSupported reports whether this platform's file metadata can
// carry allocated sizes at all. Individual filesystems may still omit them.
const AllocatedSizesSupported = true
