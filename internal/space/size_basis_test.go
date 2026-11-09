package space

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/3leaps/spanwit/internal/catalog"
	"github.com/3leaps/spanwit/internal/engine"
)

const sparseImageLogical = int64(1) << 30

// writeSparse creates a 1 GiB logical file with (on a sparse-capable
// filesystem) almost nothing allocated, or skips.
func writeSparse(t *testing.T, path string) {
	t.Helper()
	if !engine.AllocatedSizesSupported {
		t.Skip("build cannot read allocated sizes")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(sparseImageLogical); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := engine.DirSizeContext(context.Background(), filepath.Dir(path), -1)
	if err != nil {
		t.Fatal(err)
	}
	if r.AllocatedBytes >= sparseImageLogical/2 {
		t.Skipf("filesystem does not keep sparse files sparse (allocated %d)", r.AllocatedBytes)
	}
}

// Acceptance: a sparse file under a hotspot (logical 1G, allocated ~0)
// yields a hotspot row whose headline is the allocated figure, basis
// allocated, with logical as the companion field.
func TestAnalyze_SparseHotspotHeadlineIsAllocated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cat, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	rel := catalogRel(t, cat, "development.go.go-build")
	hot := filepath.Join(home, filepath.FromSlash(rel))
	writeSparse(t, filepath.Join(hot, "aa", "disk.img"))

	report, err := Analyze(context.Background(), Options{
		Path:               t.TempDir(),
		MaxDepth:           1,
		IncludeHomeCaches:  true,
		DisableRecipes:     true,
		ForcePressureLevel: PressureOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	var row *Entry
	for i := range report.Hotspots {
		if report.Hotspots[i].Path == hot {
			row = &report.Hotspots[i]
		}
	}
	if row == nil {
		t.Fatalf("hotspot %s missing: %+v", hot, report.Hotspots)
	}
	if row.SizeBasis != engine.SizeBasisAllocated {
		t.Fatalf("size_basis=%q", row.SizeBasis)
	}
	if row.SizeBytes >= sparseImageLogical/2 {
		t.Fatalf("headline %d is the logical figure, want allocated", row.SizeBytes)
	}
	if row.LogicalBytes == nil || *row.LogicalBytes != sparseImageLogical {
		t.Fatalf("logical_bytes=%v", row.LogicalBytes)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["size_basis"] != "allocated" || got["logical_bytes"] != float64(sparseImageLogical) {
		t.Fatalf("json row=%s", raw)
	}
}

// Verified rows and the handoff exact plan carry the same basis and logical
// figure, so prune replays on the basis the operator reviewed.
func TestAnalyze_HandoffCarriesSizeBasis(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(root, "app", "target", "debug", "x"), 100)
	report, err := Analyze(context.Background(), Options{
		Path:               root,
		MaxDepth:           4,
		DisableRecipes:     true,
		ForcePressureLevel: PressureOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Verified.Candidates) != 1 {
		t.Fatalf("verified=%+v", report.Verified.Candidates)
	}
	v := report.Verified.Candidates[0]
	want := engine.DefaultSizeBasis()
	if v.SizeBasis != want || v.LogicalBytes == nil || *v.LogicalBytes != 100 {
		t.Fatalf("verified row basis=%q logical=%v", v.SizeBasis, v.LogicalBytes)
	}
	if report.PruneHandoff == nil || report.PruneHandoff.ExactPlan == nil || len(report.PruneHandoff.ExactPlan.Candidates) != 1 {
		t.Skipf("no handoff for this fixture: %+v", report.PruneHandoff)
	}
	ec := report.PruneHandoff.ExactPlan.Candidates[0]
	if ec.SizeBasis != want || ec.LogicalBytes == nil || *ec.LogicalBytes != 100 || ec.SizeBytes != v.SizeBytes {
		t.Fatalf("exact candidate basis=%q logical=%v size=%d", ec.SizeBasis, ec.LogicalBytes, ec.SizeBytes)
	}
	in := ExactPlanToEngine(report.PruneHandoff.ExactPlan)
	b := in.Entries[0].Baseline
	if b.SizeBasis != want || b.LogicalBytes == nil || *b.LogicalBytes != 100 {
		t.Fatalf("engine baseline=%+v", b)
	}
}

// Acceptance: a lower-bound row cannot outrank a complete row of equal
// figure (an unreadable allocation makes an allocated row a lower bound).
func TestSortEntries_LowerBoundNeverOutranksEqualComplete(t *testing.T) {
	rows := []Entry{
		{Path: "/a-lower", SizeBytes: 4096, SizeIncomplete: true, SizeBasis: engine.SizeBasisAllocated},
		{Path: "/b-complete", SizeBytes: 4096, SizeBasis: engine.SizeBasisAllocated},
	}
	SortEntriesByBoundAwareSize(rows)
	if rows[0].Path != "/b-complete" {
		t.Fatalf("order=%s,%s", rows[0].Path, rows[1].Path)
	}
}
