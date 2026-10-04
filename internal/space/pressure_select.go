package space

import (
	"os"
	"path/filepath"
)

// primaryWritePath returns the path used for primary capacity pressure.
// Prefer the user home directory (default write volume on developer machines);
// fall back to the analysis root when home cannot be resolved.
func primaryWritePath(analysisRoot string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return analysisRoot
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return home
	}
	return abs
}

// sameVolume reports whether two pressure measurements refer to the same
// filesystem/volume. Prefer opaque VolumeID, then mount labels; never treat
// two different paths as different volumes solely because path strings differ
// (Linux/Windows often leave mount empty).
func sameVolume(a, b Pressure) bool {
	if a.VolumeID != "" && b.VolumeID != "" {
		return a.VolumeID == b.VolumeID
	}
	if a.Mount != "" && b.Mount != "" {
		return a.Mount == b.Mount
	}
	// Last resort: if only one side has a volume id, do not invent a path-based
	// difference — treat as same only when cleaned paths match exactly.
	return filepath.Clean(a.Path) == filepath.Clean(b.Path)
}
