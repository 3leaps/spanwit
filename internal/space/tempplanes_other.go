//go:build !unix

package space

import "os"

// Temp-plane coverage is darwin/linux only; other platforms admit nothing.
func admitTempPlane(string, string) (dirIdentity, bool) { return dirIdentity{}, false }

func statDirIdentity(string) (dirIdentity, bool) { return dirIdentity{}, false }

func deviceOf(os.FileInfo) (uint64, bool) { return 0, false }
