//go:build unix

package engine

import (
	"io/fs"
	"math"
	"syscall"
)

// AllocatedSizesSupported reports whether this build can read allocated
// bytes (st_blocks) at all. Individual entries may still lack them.
const AllocatedSizesSupported = true

// allocatedBytes returns the bytes allocated to an entry (st_blocks x 512).
// A missing stat, a negative count or an overflowing product reports false
// so the caller treats the allocated sum as unmeasured, never wrapped.
func allocatedBytes(info fs.FileInfo) (int64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	blocks := int64(st.Blocks) // #nosec G115 -- st_blocks is a non-negative block count on every unix target; negatives are rejected below.
	if blocks < 0 || blocks > math.MaxInt64/512 {
		return 0, false
	}
	return blocks * 512, true
}
