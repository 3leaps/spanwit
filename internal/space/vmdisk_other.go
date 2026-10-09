//go:build !unix

package space

import "os"

// fileAllocation is unavailable off unix: allocated block counts are unknown.
func fileAllocation(_ os.FileInfo) (logical, allocated int64, ok bool) {
	return 0, 0, false
}
