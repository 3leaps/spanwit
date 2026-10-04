//go:build !unix

package engine

// sameMount cannot determine device identity portably off unix; it does not
// block cross-mount deletion on these platforms. The documented residual
// TOCTOU/mount posture applies. Lexical containment (checkContainment) and the
// volume-name check still apply on all platforms.
func sameMount(boundary, target string) (bool, error) {
	return true, nil
}

// deviceID cannot report a device on non-unix platforms; the nested-mount
// preflight therefore does not block there.
func deviceID(path string) (uint64, bool, error) {
	return 0, false, nil
}
