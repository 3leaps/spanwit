package space

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/3leaps/spanwit/internal/catalog"
)

type hotspotSpec struct {
	ID                 string
	CatalogID          string // full-spine domain.ecosystem.name id from the catalog
	RelHome            string // path relative to home for the current platform
	Label              string
	RebuildExpectation string
}

// hotspotSpecsFor derives home-cache hotspot specs for goos from a resolved
// catalog. The catalog is the SSOT for ids, labels, rebuild expectation, and
// per-platform locations; Go still owns path resolution, sizing, and guards.
func hotspotSpecsFor(cat *catalog.Catalog, goos string) []hotspotSpec {
	out := make([]hotspotSpec, 0)
	for _, e := range cat.HotspotsForOS(goos) {
		rel, ok := e.LocationFor(goos)
		if !ok {
			continue
		}
		out = append(out, hotspotSpec{
			ID:                 e.Name,
			CatalogID:          e.ID,
			RelHome:            rel,
			Label:              e.Label,
			RebuildExpectation: e.RebuildExpectation,
		})
	}
	return out
}

// knownHotspots returns built-in hotspot specs for the current platform,
// derived from the embedded catalog. Retained for callers/tests that do not
// resolve user overlays.
func knownHotspots() []hotspotSpec {
	cat, err := catalog.BuiltIn()
	if err != nil {
		return nil
	}
	return hotspotSpecsFor(cat, runtime.GOOS)
}

// collectHotspots returns the full hotspot set (not top-truncated).
// sizeDepth bounds recursive sizing under each hotspot root (same budget as space max-depth).
func collectHotspots(ctx context.Context, cat *catalog.Catalog, minSize int64, sizeDepth int, walker *observationWalker) ([]Entry, []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return []Entry{}, []string{"cannot resolve home for hotspot catalog: " + err.Error()}
	}
	out := make([]Entry, 0)
	coverage := newObservationCoverage()
	for _, spec := range hotspotSpecsFor(cat, runtime.GOOS) {
		path := filepath.Join(home, filepath.FromSlash(spec.RelHome))
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue // missing is normal
		}
		if err == nil && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		e := Entry{
			Path:               path,
			State:              StateDiagnosticOnly,
			Label:              spec.Label,
			RebuildExpectation: spec.RebuildExpectation,
			CatalogID:          spec.CatalogID,
		}
		AnnotateEntryTaxonomy(&e)
		out = append(out, e)
	}
	out = walker.size(ctx, out, sizeDepth, minSize, coverage)
	return out, coverage.warnings(PhaseHotspots)
}
