//go:build unix

package bench

import (
	"bytes"
	"strings"

	"golang.org/x/sys/unix"
)

// kernelVersion returns the running kernel's name and release, or "unknown"
// when uname is unavailable. "unknown" is a stated value rather than an empty
// one: validation rejects absence, and an operator reading "unknown" knows the
// harness tried.
func kernelVersion() string {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return "unknown"
	}
	sysname := cstring(uts.Sysname[:])
	release := cstring(uts.Release[:])
	if sysname == "" && release == "" {
		return "unknown"
	}
	return strings.TrimSpace(sysname + " " + release)
}

// cstring converts a NUL-padded byte field to a Go string.
func cstring(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}
