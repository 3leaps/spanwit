//go:build !unix

package bench

import "io/fs"

// identityOf reports that file identity is not observable here. Stream
// exclusion then does nothing, and an operator redirecting harness output into
// a witnessed root will see it reported as an attributable write.
func identityOf(_ fs.FileInfo) (fileIdentity, bool) {
	return fileIdentity{}, false
}
