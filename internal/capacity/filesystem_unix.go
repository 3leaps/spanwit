//go:build unix

package capacity

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

func sampleFilesystem(path string) (FilesystemSample, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return FilesystemSample{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	bsize := filesystemBlockSize(&st)
	if bsize <= 0 {
		return FilesystemSample{}, fmt.Errorf("statfs %s: invalid block size", path)
	}
	total := int64(st.Blocks) * bsize
	available := int64(st.Bavail) * bsize
	used := total - int64(st.Bfree)*bsize
	if used < 0 {
		used = total - available
	}
	return FilesystemSample{
		Path:      path,
		Mount:     filesystemMount(&st),
		VolumeID:  filesystemVolumeID(&st),
		FSType:    filesystemType(&st),
		Total:     total,
		Used:      used,
		Available: available,
	}, nil
}

func cString(b []byte) string {
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	return strings.TrimSpace(string(b[:n]))
}
