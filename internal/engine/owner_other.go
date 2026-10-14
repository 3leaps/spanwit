//go:build !unix

package engine

import "os"

// fileOwner is unknown off unix; temp-root admission then refuses.
func fileOwner(os.FileInfo) (int, bool) { return 0, false }
