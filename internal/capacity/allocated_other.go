//go:build !unix

package capacity

import "os"

func allocatedBytes(info os.FileInfo) int64 {
	return info.Size()
}
