package config

import (
	"path/filepath"
	"testing"
)

func TestFindProjectRoot_CIWorkspaceBoundaryHint(t *testing.T) {
	// Simulate CI.
	t.Setenv("CI", "true")

	// When running `go test`, the working directory is the package directory.
	// For internal/config, the repo root is ../..
	expectedRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("failed to resolve expected root: %v", err)
	}

	// Provide a CI boundary hint (must contain the start path).
	t.Setenv("FULMEN_WORKSPACE_ROOT", expectedRoot)

	root, err := FindProjectRoot()
	if err != nil {
		t.Fatalf("FindProjectRoot returned error: %v", err)
	}

	if filepath.Clean(root) != filepath.Clean(expectedRoot) {
		t.Fatalf("expected root %q, got %q", expectedRoot, root)
	}
}
