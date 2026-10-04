//go:build !linux && !darwin

package engine

// birthWitness cannot report an inode creation time portably on these
// platforms. It reports no witness rather than fabricating one, so the identity
// re-check falls back to the device+inode comparison and the documented residual
// (a replacement that reuses an inode is not detected) applies.
func birthWitness(path string) (birthTime, error) {
	return birthTime{}, nil
}
