package inventory

import "math"

// blockSizeBytes is the POSIX st_blocks unit: bytes per allocated block.
const blockSizeBytes = 512

// blockBytes converts a block count to bytes with checked arithmetic. A
// negative or overflowing count reports false so callers keep the value
// unmeasured instead of wrapping into a negative or wrapped byte claim.
func blockBytes(blocks int64) (int64, bool) {
	if blocks < 0 || blocks > math.MaxInt64/blockSizeBytes {
		return 0, false
	}
	return blocks * blockSizeBytes, true
}
