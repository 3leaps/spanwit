package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fulmenhq/gofulmen/pathfinder"
)

// FindProjectRoot walks up from the current working directory to find the
// repository root.
//
// It uses gofulmen/pathfinder.FindRepositoryRoot() which provides:
// - max-depth protection
// - boundary enforcement
// - symlink loop protections
//
// CI boundary hint behavior (per gofulmen v0.1.21 app note):
//   - In CI only, we treat common workspace env vars as a *boundary hint*, never
//     as an unconditional root.
//   - We still require repository markers (go.mod / .git).
func FindProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	return FindProjectRootFrom(cwd)
}

// FindProjectRootFrom finds the repository root starting from startPath.
func FindProjectRootFrom(startPath string) (string, error) {
	markers := []string{"go.mod", ".git"}

	cleanStart := filepath.Clean(startPath)

	// CI-only boundary hint pattern.
	isCI := strings.EqualFold(strings.TrimSpace(os.Getenv("GITHUB_ACTIONS")), "true") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("CI")), "true")
	if isCI {
		boundaryKeys := []string{"FULMEN_WORKSPACE_ROOT", "GITHUB_WORKSPACE", "CI_PROJECT_DIR", "WORKSPACE", "PROJECT_ROOT"}
		for _, key := range boundaryKeys {
			boundary := strings.TrimSpace(os.Getenv(key))
			if boundary == "" {
				continue
			}
			boundary = filepath.Clean(boundary)
			if !filepath.IsAbs(boundary) {
				continue
			}
			st, err := os.Stat(boundary)
			if err != nil || !st.IsDir() {
				continue
			}
			// Only accept a boundary that contains the start path.
			if rel, err := filepath.Rel(boundary, cleanStart); err != nil || strings.HasPrefix(rel, "..") {
				continue
			}

			rootPath, err := pathfinder.FindRepositoryRoot(cleanStart, markers,
				pathfinder.WithBoundary(boundary),
				pathfinder.WithMaxDepth(20),
			)
			if err == nil {
				return rootPath, nil
			}
		}
	}

	rootPath, err := pathfinder.FindRepositoryRoot(cleanStart, markers, pathfinder.WithMaxDepth(10))
	if err != nil {
		return "", fmt.Errorf("project root not found: %w", err)
	}

	return rootPath, nil
}
