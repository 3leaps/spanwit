package space

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/catalog"
)

// An incomplete hotspot row survives the top-N budget in both the early
// (OnPartial) and final (Report) projections: with Top=1, the complete row
// keeps its slot and the partial row rides outside the budget.
func TestAnalyze_PartialHotspotSurvivesTopCut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cat, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	// Portable entries with per-OS locations; skip where they do not apply.
	completeRel := catalogRel(t, cat, "development.go.go-build")
	deepRel := catalogRel(t, cat, "development.security.grype")
	if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(completeRel)), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(home, filepath.FromSlash(deepRel), "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var early []Entry
	report, err := Analyze(context.Background(), Options{
		Path:               root,
		Top:                1,
		MaxDepth:           1,
		IncludeHomeCaches:  true,
		DisableRecipes:     true,
		ForcePressureLevel: PressureOK,
		OnPartial: func(u PartialUpdate) {
			if u.Phase == PhaseHotspots {
				early = append([]Entry(nil), u.Hotspots...)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPartialKept := func(name string, rows []Entry) {
		t.Helper()
		var completeKept, partialKept bool
		for _, row := range rows {
			switch row.CatalogID {
			case "development.go.go-build":
				completeKept = true
			case "development.security.grype":
				if !row.SizeIncomplete {
					t.Errorf("%s: grype row should be incomplete (depth bound)", name)
				}
				partialKept = true
			}
		}
		if !completeKept || !partialKept {
			t.Errorf("%s: complete=%v partial=%v, want both kept at Top=1: %#v",
				name, completeKept, partialKept, rows)
		}
	}
	assertPartialKept("early", early)
	assertPartialKept("final", report.Hotspots)
}

func catalogRel(t *testing.T, cat *catalog.Catalog, id string) string {
	t.Helper()
	e, ok := cat.EntryByID(id)
	if !ok {
		t.Fatalf("catalog entry %q missing", id)
	}
	rel, ok := e.LocationFor(runtime.GOOS)
	if !ok {
		t.Skipf("entry %q has no location on %s", id, runtime.GOOS)
	}
	return rel
}

// Missing applications are ordinary: an empty home yields no hotspot rows,
// no warnings, and no error — never a failure.
func TestCollectHotspots_MissingPathsAreOrdinary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cat, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	rows, warnings := collectHotspots(context.Background(), cat, 0, 0, newObservationWalker(Options{}))
	if len(rows) != 0 {
		t.Fatalf("empty home: got %d hotspot rows, want 0", len(rows))
	}
	if len(warnings) != 0 {
		t.Fatalf("empty home: got warnings %v, want none", warnings)
	}
}

// A present named cache resolves through the catalog to a sized row.
func TestCollectHotspots_PresentDirResolves(t *testing.T) {
	cat, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	rel := catalogRel(t, cat, "development.node.yarn-cache")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	rows, warnings := collectHotspots(context.Background(), cat, 0, 0, newObservationWalker(Options{}))
	if len(warnings) != 0 {
		t.Fatalf("got warnings %v, want none", warnings)
	}
	found := false
	for _, row := range rows {
		if row.CatalogID == "development.node.yarn-cache" {
			found = true
			if row.State != StateDiagnosticOnly {
				t.Errorf("yarn row state = %q, want diagnostic-only", row.State)
			}
		}
	}
	if !found {
		t.Fatalf("yarn-cache row missing: %#v", rows)
	}
}

// A never-read root is coverage-only, not a fabricated measured zero.
func TestCollectHotspots_DeniedRootIsCoverageNotZero(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	cat, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	rel := catalogRel(t, cat, "development.node.yarn-cache")
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blob"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	for _, depth := range []int{-1, 0, 6} {
		rows, warnings := collectHotspots(context.Background(), cat, 0, depth, newObservationWalker(Options{}))
		if len(warnings) != 1 || !strings.Contains(warnings[0], "permission=1; wholly_unmeasured=1") {
			t.Fatalf("depth %d: missing denied-root coverage: %v", depth, warnings)
		}
		for _, row := range rows {
			if row.CatalogID == "development.node.yarn-cache" {
				t.Errorf("depth %d: never-read root became numeric row: %+v", depth, row)
			}
		}
	}
}
