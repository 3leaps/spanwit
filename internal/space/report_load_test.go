package space

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/capacity"
)

func readSchemaFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "schema-validation", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestLoadSpaceReportJSON_FullV2ExactPlanReplayParity(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	now := time.Date(2026, 7, 30, 19, 0, 0, 0, time.UTC)
	v1, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v1.PruneHandoff == nil || !v1.PruneHandoff.Present {
		t.Fatal("fixture needs exact-plan handoff")
	}
	collected, err := (capacity.Collector{
		Platform: "linux",
		Now:      func() time.Time { return now },
		FilesystemSampler: func(_ context.Context, path string) (capacity.FilesystemSample, error) {
			return capacity.FilesystemSample{
				Path: path, Mount: v1.Pressure.Mount, VolumeID: v1.Pressure.VolumeID,
				FSType: "ext4", Total: v1.Pressure.TotalBytes, Used: v1.Pressure.UsedBytes,
				Available: v1.Pressure.AvailBytes,
			}, nil
		},
	}).Collect(context.Background(), capacity.Request{
		TargetPath: v1.Pressure.Path,
		Tool:       capacity.ToolIdentity{Name: "spanwit", Version: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var carrier map[string]any
	if err := json.Unmarshal(mustJSON(t, v1), &carrier); err != nil {
		t.Fatal(err)
	}
	carrier["$schema"] = SchemaV2ID
	carrier["version"] = float64(2)
	carrier["capture_mode"] = CaptureModeFull
	accountingRaw, _ := json.Marshal(collected.Accounting)
	var accounting map[string]any
	_ = json.Unmarshal(accountingRaw, &accounting)
	carrier["capacity_accounting"] = accounting
	raw, _ := json.Marshal(carrier)
	got, err := LoadSpaceReportJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.PruneHandoff == nil || !got.PruneHandoff.Present ||
		len(got.PruneHandoff.ExactPlan.Candidates) != len(v1.PruneHandoff.ExactPlan.Candidates) {
		t.Fatalf("v2 replay changed exact plan: %#v", got.PruneHandoff)
	}
}

func TestLoadSpaceReportJSON_V2DispatchAndCapacityFailClosed(t *testing.T) {
	capacityOnly := readSchemaFixture(t, "space-report-v2-capacity-only.json")
	if _, err := LoadSpaceReportJSON(capacityOnly); !errors.Is(err, ErrCapacityOnlyNotPruneCarrier) {
		t.Fatalf("capacity-only err=%v", err)
	}

	full := readSchemaFixture(t, "space-report-v2-full.json")
	report, err := LoadSpaceReportJSON(full)
	if err != nil {
		t.Fatalf("full v2: %v", err)
	}
	if report.Version != 2 || report.Schema != SchemaV2ID {
		t.Fatalf("full v2 identity lost: %#v", report)
	}
}

func TestLoadSpaceReportJSON_RejectsIdentityAndCapacityMismatches(t *testing.T) {
	var base map[string]any
	if err := json.Unmarshal(readSchemaFixture(t, "space-report-v2-capacity-only.json"), &base); err != nil {
		t.Fatal(err)
	}
	clone := func() map[string]any {
		raw, _ := json.Marshal(base)
		var value map[string]any
		_ = json.Unmarshal(raw, &value)
		return value
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"v2 schema version mismatch", func(v map[string]any) { v["version"] = float64(1) }},
		{"unknown pair", func(v map[string]any) {
			v["$schema"] = "https://schemas.example.invalid/report.json"
		}},
		{"time mismatch", func(v map[string]any) { v["generated_at"] = "2026-07-30T15:00:01Z" }},
		{"target mismatch", func(v map[string]any) {
			v["capacity_accounting"].(map[string]any)["target"].(map[string]any)["volume_id"] = "fs:other"
		}},
		{"same observation mismatch", func(v map[string]any) {
			v["capacity_accounting"].(map[string]any)["planes"].(map[string]any)["filesystem"].(map[string]any)["available"].(map[string]any)["bytes"] = float64(1)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := clone()
			tt.mutate(value)
			raw, _ := json.Marshal(value)
			if _, err := LoadSpaceReportJSON(raw); err == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}

	var v1 map[string]any
	if err := json.Unmarshal(mustJSON(t, validMinimalReport(t)), &v1); err != nil {
		t.Fatal(err)
	}
	v1["version"] = float64(2)
	raw, _ := json.Marshal(v1)
	if _, err := LoadSpaceReportJSON(raw); err == nil {
		t.Fatal("v1 schema/version mismatch accepted")
	}
}

func TestLoadSpaceReportJSON_ValidRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
		Now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadSpaceReportJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.PruneHandoff == nil || !got.PruneHandoff.Present {
		t.Fatal("expected handoff")
	}
}

func TestLoadSpaceReportJSON_RejectsMinimalTruncated(t *testing.T) {
	// Malformed report missing required policy fields (schema must reject).
	raw := []byte(`{
  "prune_handoff": {
    "present": true,
    "exact_plan": {
      "candidates": [
        {"path": "/tmp/x/target", "signature": "development.rust.cargo-target"}
      ]
    }
  }
}`)
	_, err := LoadSpaceReportJSON(raw)
	if err == nil {
		t.Fatal("expected schema rejection of truncated report")
	}
	if !strings.Contains(err.Error(), "schema validation failed") {
		t.Fatalf("want schema error, got %v", err)
	}
}

func TestLoadSpaceReportJSON_RejectsMissingFiltersEvenIfSchemaBypassed(t *testing.T) {
	// Build a structurally valid report then strip filters from exact_plan.
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
		Now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.PruneHandoff == nil || report.PruneHandoff.ExactPlan == nil {
		t.Fatal("need handoff")
	}
	// Clear filters
	for i := range report.PruneHandoff.ExactPlan.Candidates {
		report.PruneHandoff.ExactPlan.Candidates[i].EffectiveMinSize = ""
		report.PruneHandoff.ExactPlan.Candidates[i].EffectiveMinAge = ""
		report.PruneHandoff.ExactPlan.Candidates[i].EffectiveMaxAge = ""
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	// Schema requires those fields — expect schema or invariant failure.
	_, err = LoadSpaceReportJSON(raw)
	if err == nil {
		t.Fatal("expected rejection of missing filters")
	}
}

func TestLoadSpaceReportJSON_RejectsHandoffExactMismatch(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
		Now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Add extra exact_plan path not in handoff candidates
	report.PruneHandoff.ExactPlan.Candidates = append(report.PruneHandoff.ExactPlan.Candidates, ExactCandidate{
		Path:             "/evil/target",
		Signature:        "development.rust.cargo-target",
		EffectiveMinSize: "none",
		EffectiveMinAge:  "none",
		EffectiveMaxAge:  "none",
		SizeBytes:        1,
		SizeHuman:        "1B",
		ReclaimScope:     "whole",
	})
	report.PruneHandoff.PrunableCount = len(report.PruneHandoff.Candidates) // leave counts inconsistent with exact
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadSpaceReportJSON(raw)
	if err == nil {
		t.Fatal("expected mismatch rejection")
	}
}

func TestLoadSpaceReportJSON_RejectsReclaimScopeMismatch(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
		Now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.PruneHandoff == nil || !report.PruneHandoff.Present {
		t.Fatal("need handoff")
	}
	// Outer handoff says whole, exact row flipped to incremental without parent — invalid.
	// Even with matching paths, scope disagreement must fail closed.
	report.PruneHandoff.Candidates[0].ReclaimScope = "whole"
	report.PruneHandoff.Candidates[0].ParentPath = ""
	report.PruneHandoff.ExactPlan.Candidates[0].ReclaimScope = "incremental"
	report.PruneHandoff.ExactPlan.Candidates[0].ParentPath = report.PruneHandoff.Candidates[0].Path
	// Keep plan-level whole so candidate disagrees with exact_plan.reclaim_scope too.
	report.PruneHandoff.ExactPlan.ReclaimScope = "whole"
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadSpaceReportJSON(raw)
	if err == nil {
		t.Fatal("expected reclaim_scope mismatch rejection")
	}
	if !strings.Contains(err.Error(), "reclaim_scope") {
		t.Fatalf("want reclaim_scope error, got %v", err)
	}
}

func TestLoadSpaceReportJSON_RejectsParentPathMismatch(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	inc := filepath.Join(tmp, "app", "target", "debug", "incremental")
	writeFile(t, filepath.Join(inc, "x"), 64)
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1B", IncludeHomeCaches: false, MaxDepth: 8,
		ReclaimScope: "incremental",
		Now:          time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.PruneHandoff == nil || !report.PruneHandoff.Present || len(report.PruneHandoff.Candidates) == 0 {
		t.Fatalf("need incremental handoff, got %#v", report.PruneHandoff)
	}
	// Divergent parent_path between handoff and exact with same path.
	report.PruneHandoff.ExactPlan.Candidates[0].ParentPath = filepath.Join(tmp, "other-parent")
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadSpaceReportJSON(raw)
	if err == nil {
		t.Fatal("expected parent_path mismatch rejection")
	}
	if !strings.Contains(err.Error(), "parent_path") {
		t.Fatalf("want parent_path error, got %v", err)
	}
}

func TestAnalyze_CarriesSortAndReclaimScope(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
		SortMode: "size", ReclaimScope: "whole",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.SortMode != "size" || report.ReclaimScope != "whole" {
		t.Fatalf("report policy: sort=%s scope=%s", report.SortMode, report.ReclaimScope)
	}
	if report.PruneHandoff == nil || report.PruneHandoff.ExactPlan == nil {
		return
	}
	ep := report.PruneHandoff.ExactPlan
	if ep.SortMode != "size" || ep.ReclaimScope != "whole" {
		t.Fatalf("exact_plan policy: sort=%s scope=%s", ep.SortMode, ep.ReclaimScope)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpaceReportJSON(raw); err != nil {
		t.Fatalf("round-trip: %v", err)
	}
}

func TestAnalyze_CriticalBudgetForced(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)

	var note string
	report, err := Analyze(context.Background(), Options{
		Path:               tmp,
		IncludeHomeCaches:  false,
		MaxDepth:           4, // already under cap — still must note unknown skip
		ForcePressureLevel: PressureCritical,
		OnPartial: func(u PartialUpdate) {
			note = u.WorkBudgetNote
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if note == "" || !strings.Contains(note, "unknown") {
		t.Fatalf("expected unknown skip disclosure, got %q", note)
	}
	if len(report.Unknown) != 0 {
		t.Fatalf("unknown should be empty under critical, got %d", len(report.Unknown))
	}
	found := false
	for _, n := range report.Notes {
		if strings.Contains(n, "unknown") {
			found = true
		}
	}
	if !found {
		t.Fatalf("report notes missing unknown disclosure: %v", report.Notes)
	}
}

func TestAnalyze_CriticalBudgetCapsDepthForced(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	report, err := Analyze(context.Background(), Options{
		Path:               tmp,
		IncludeHomeCaches:  false,
		MaxDepth:           20,
		ForcePressureLevel: PressureCritical,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.MaxDepth != CriticalDeepMaxDepth {
		t.Fatalf("max_depth=%d", report.MaxDepth)
	}
}

func TestLoadSpaceReportJSON_RejectsInjectedHotspotUnderPrunableFilter(t *testing.T) {
	// Entarch repro: applied_filters.classes=["prunable"] + injected diagnostic hotspot.
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 4096)
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
		ClassFilters: []string{"prunable"},
		Now:          time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.AppliedFilters == nil {
		t.Fatal("need applied_filters")
	}
	report.Hotspots = append(report.Hotspots, Entry{
		Path:               filepath.Join(tmp, "fake-cache"),
		SizeBytes:          100,
		SizeHuman:          "100B",
		State:              StateDiagnosticOnly,
		Label:              "injected",
		RebuildExpectation: RebuildHigh,
		CatalogID:          "development.go.go-build",
		Domain:             "development",
		Ecosystem:          "go",
	})
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadSpaceReportJSON(raw)
	if err == nil {
		t.Fatal("loader must reject diagnostic hotspot under applied_filters.classes=prunable")
	}
	if !strings.Contains(err.Error(), "hotspot") && !strings.Contains(err.Error(), "applied_filters") {
		t.Fatalf("want hotspot/applied_filters error, got %v", err)
	}
}

func TestLoadSpaceReportJSON_RejectsPositivePrunableAggregateUnderDiagnosticFilter(t *testing.T) {
	// Entarch repro: classes=["diagnostic-only"] with empty handoff but positive
	// verified prunable aggregates — contradictory carrier must fail closed.
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 4096)
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: true, DisableRecipes: true, MaxDepth: 6,
		ClassFilters: []string{"diagnostic-only"},
		Now:          time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.AppliedFilters == nil {
		t.Fatal("need applied_filters")
	}
	// Leave handoff empty/absent; claim prunable inventory in aggregates only.
	report.Verified.PrunableCandidates = 3
	report.Verified.PrunableBytes = 999
	report.Verified.PrunableHuman = "999B"
	if report.PruneHandoff != nil && report.PruneHandoff.Present {
		t.Fatal("fixture expects empty handoff under diagnostic-only")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadSpaceReportJSON(raw)
	if err == nil {
		t.Fatal("loader must reject positive prunable aggregates under diagnostic-only filter")
	}
	if !strings.Contains(err.Error(), "prunable") {
		t.Fatalf("want prunable aggregate error, got %v", err)
	}
}

func TestLoadSpaceReportJSON_RejectsContradictoryDomainField(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 4096)
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
		ClassFilters: []string{"prunable"},
		Now:          time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Verified.Candidates) == 0 {
		t.Fatal("need verified candidate")
	}
	report.Verified.Candidates[0].Domain = "media" // contradicts development.rust.*
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadSpaceReportJSON(raw)
	if err == nil {
		t.Fatal("loader must reject domain that contradicts signature spine")
	}
}

func TestLoadSpaceReportJSON_RejectsEmptyDisplayWithPositivePrunableTotal(t *testing.T) {
	// Devrev PR final: positive totals + full handoff with displayed prunables stripped.
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "a", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "a", "target", "debug", "x"), 4096)
	writeFile(t, filepath.Join(tmp, "b", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "b", "target", "debug", "y"), 4096)
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "10B", IncludeHomeCaches: false, MaxDepth: 6,
		ClassFilters: []string{"prunable"}, Top: 1,
		Now: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verified.PrunableCandidates < 2 {
		t.Fatalf("need ≥2 prunables for top truncation fixture, got %d", report.Verified.PrunableCandidates)
	}
	if report.PruneHandoff == nil || report.PruneHandoff.PrunableCount < 2 {
		t.Fatal("need full handoff > top")
	}
	// Honest truncated display must load.
	rawOK, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpaceReportJSON(rawOK); err != nil {
		t.Fatalf("honest top-truncated filtered report must load: %v", err)
	}
	// Strip displayed prunables while keeping totals + handoff.
	report.Verified.Candidates = []Entry{}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadSpaceReportJSON(raw)
	if err == nil {
		t.Fatal("loader must reject empty display with positive prunable total/handoff")
	}
	if !strings.Contains(err.Error(), "display cardinality") && !strings.Contains(err.Error(), "prunable") {
		t.Fatalf("want display cardinality error, got %v", err)
	}
}

func TestLoadSpaceReportJSON_RejectsEmptyUnverifiedDisplayWithPositiveCount(t *testing.T) {
	tmp := t.TempDir()
	// Name-shaped unverified only (no Cargo.toml).
	writeFile(t, filepath.Join(tmp, "other", "target", "b"), 4096)
	// Also a cargo tree so class filter on unverified alone can still be schema-valid.
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1B", IncludeHomeCaches: false, MaxDepth: 6,
		ClassFilters: []string{"unverified"}, Top: 5,
		Now: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Unverified.Count < 1 {
		t.Fatalf("need unverified count, got %#v", report.Unverified)
	}
	// Tamper: positive count, empty entries.
	report.Unverified.Entries = []Entry{}
	// Keep Count/bytes positive.
	if report.Unverified.Count == 0 {
		report.Unverified.Count = 1
		report.Unverified.TotalBytes = 100
		report.Unverified.TotalHuman = "100B"
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LoadSpaceReportJSON(raw)
	if err == nil {
		t.Fatal("loader must reject unverified count>0 with empty entries under applied_filters")
	}
	if !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("want unverified cardinality error, got %v", err)
	}
}

func TestLoadSpaceReportJSON_AcceptsHonestTopTruncation(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(tmp, name, "Cargo.toml"), 32)
		writeFile(t, filepath.Join(tmp, name, "target", "debug", "x"), 4096)
	}
	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
		ClassFilters: []string{"prunable"}, Top: 1,
		Now: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verified.PrunableCandidates < 2 {
		t.Fatalf("want full total > top, got %d", report.Verified.PrunableCandidates)
	}
	var nP int
	for _, c := range report.Verified.Candidates {
		if c.State == StatePrunable {
			nP++
		}
	}
	if nP != 1 {
		t.Fatalf("display prunable rows want 1, got %d", nP)
	}
	if report.PruneHandoff == nil || report.PruneHandoff.PrunableCount != report.Verified.PrunableCandidates {
		t.Fatalf("handoff must be full total, handoff=%v total=%d", report.PruneHandoff, report.Verified.PrunableCandidates)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpaceReportJSON(raw); err != nil {
		t.Fatalf("honest top-truncated report rejected: %v", err)
	}
}
