//go:build unix

package capacity

import (
	"fmt"
	"math"
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
	total, okTotal := blocksToBytes(uint64(st.Blocks), bsize)     //nolint:unconvert // field width differs by platform
	available, okAvail := blocksToBytes(uint64(st.Bavail), bsize) //nolint:unconvert // field width differs by platform
	free, okFree := blocksToBytes(uint64(st.Bfree), bsize)        //nolint:unconvert // field width differs by platform
	if !okTotal || !okAvail || !okFree {
		return FilesystemSample{}, fmt.Errorf("statfs %s: block counts exceed the representable byte range", path)
	}
	used := total - free
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

// blocksToBytes converts a statfs block count to bytes, refusing a count or
// product that does not fit in int64 rather than wrapping to a wrong size.
func blocksToBytes(blocks uint64, bsize int64) (int64, bool) {
	if bsize <= 0 || blocks > uint64(math.MaxInt64/bsize) { // #nosec G115 -- bsize > 0, so the quotient is non-negative
		return 0, false
	}
	return int64(blocks) * bsize, true // #nosec G115 -- bounded above by MaxInt64/bsize
}

func cString(b []byte) string {
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	return strings.TrimSpace(string(b[:n]))
}
