package space

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

func validMinimalReport(t *testing.T) Report {
	t.Helper()
	tmp := t.TempDir()
	// Cargo-verified target (needs Cargo.toml ancestor + cargo-like child).
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a.o"), 2048)
	// Name-shaped only (no Cargo.toml).
	writeFile(t, filepath.Join(tmp, "other", "target", "b"), 2048)
	// Immediate unknown child mass.
	writeFile(t, filepath.Join(tmp, "misc", "blob"), 4096)
	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1K",
		Top:               10,
		IncludeHomeCaches: false,
		MaxDepth:          6,
		Now:               time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(report.Verified.Candidates) == 0 {
		t.Fatalf("expected verified candidate for schema tests, got %#v", report.Verified)
	}
	if len(report.Unverified.Entries) == 0 {
		t.Fatalf("expected unverified entry for schema tests, got %#v", report.Unverified)
	}
	if len(report.Unknown) == 0 {
		report.Unknown = []Entry{{
			Path:      filepath.Join(tmp, "misc"),
			SizeBytes: 4096,
			SizeHuman: "4.0K",
			State:     StateUnknown,
			Label:     "unclassified directory",
		}}
	}
	if len(report.Hotspots) == 0 {
		report.Hotspots = []Entry{{
			Path:               filepath.Join(tmp, "fake-cache"),
			SizeBytes:          4096,
			SizeHuman:          "4.0K",
			State:              StateDiagnosticOnly,
			Label:              "test hotspot",
			RebuildExpectation: RebuildHigh,
		}}
	}
	return report
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSpaceReportSchema_SectionStatePlacement(t *testing.T) {
	report := validMinimalReport(t)
	// Ensure handoff/recipes (when present) stay schema-valid and never flip trust states.
	if report.PruneHandoff != nil {
		for _, c := range report.PruneHandoff.Candidates {
			if c.State != StatePrunable {
				t.Fatalf("handoff candidate state=%q want prunable", c.State)
			}
		}
	}
	for _, r := range report.Recipes {
		if r.State != StateDiagnosticOnly {
			t.Fatalf("recipe state=%q want diagnostic-only", r.State)
		}
	}
	raw := mustJSON(t, report)
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
		t.Fatalf("valid report must pass schema: %v\n%s", err, raw)
	}

	// 1) hotspots cannot be prunable
	bad := report
	bad.Hotspots = append([]Entry(nil), report.Hotspots...)
	bad.Hotspots[0].State = StatePrunable
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, mustJSON(t, bad)); err == nil {
		t.Fatal("expected schema reject: hotspot state=prunable")
	}

	// 2) verified cannot be diagnostic-only
	bad = report
	bad.Verified.Candidates = append([]Entry(nil), report.Verified.Candidates...)
	bad.Verified.Candidates[0].State = StateDiagnosticOnly
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, mustJSON(t, bad)); err == nil {
		t.Fatal("expected schema reject: verified state=diagnostic-only")
	}

	// 3) unverified cannot be unknown
	bad = report
	bad.Unverified.Entries = append([]Entry(nil), report.Unverified.Entries...)
	bad.Unverified.Entries[0].State = StateUnknown
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, mustJSON(t, bad)); err == nil {
		t.Fatal("expected schema reject: unverified state=unknown")
	}

	// 4) unknown cannot be unverified
	bad = report
	bad.Unknown = append([]Entry(nil), report.Unknown...)
	bad.Unknown[0].State = StateUnverified
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, mustJSON(t, bad)); err == nil {
		t.Fatal("expected schema reject: unknown state=unverified")
	}
}

func TestUnverified_SizeIncompleteSurvivesTopTruncation(t *testing.T) {
	tmp := t.TempDir()
	// Incomplete diagnostic rows retain coverage outside the complete-row top budget.
	for _, name := range []string{"a", "b"} {
		writeFile(t, filepath.Join(tmp, name, "target", "d0", "d1", "d2", "blob"), 4096)
	}
	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1",
		Top:               1,
		MaxDepth:          2, // sizes under target with small depth → incomplete
		IncludeHomeCaches: false,
		Now:               time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if report.Unverified.Count < 2 {
		t.Fatalf("want full-set count >=2, got %#v", report.Unverified)
	}
	if len(report.Unverified.Entries) != 2 {
		t.Fatalf("top must retain both incomplete entries, got %#v", report.Unverified.Entries)
	}
	if !report.Unverified.SizeIncomplete {
		t.Fatal("section size_incomplete must reflect full set, not display rows only")
	}
}

func TestUnverified_IncompleteBelowMinSizeStillVisible(t *testing.T) {
	tmp := t.TempDir()
	// Mass only deep under target; depth bound 2 under target marks incomplete lower bound.
	writeFile(t, filepath.Join(tmp, "proj", "target", "n0", "n1", "n2", "n3", "mass.bin"), 2*1024*1024)
	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "10M",
		MaxDepth:          2, // discovers proj/target; sizes under target with bound 2
		Top:               10,
		IncludeHomeCaches: false,
		Now:               time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if report.Unverified.Count < 1 {
		t.Fatalf("incomplete below min_size must remain visible, got %#v", report.Unverified)
	}
	e := report.Unverified.Entries[0]
	if e.State != StateUnverified || !e.SizeIncomplete {
		t.Fatalf("want unverified+incomplete, got %#v", e)
	}
	if e.SizeBytes >= 10*1024*1024 {
		t.Fatalf("expected lower bound below min_size, got %d", e.SizeBytes)
	}
}

func TestIncludeMeasuredSize(t *testing.T) {
	if !includeMeasuredSize(100, true, 1000) {
		t.Fatal("incomplete below min must include")
	}
	if includeMeasuredSize(100, false, 1000) {
		t.Fatal("complete below min must exclude")
	}
	if !includeMeasuredSize(2000, false, 1000) {
		t.Fatal("complete above min must include")
	}
	if !includeMeasuredSize(0, false, 0) {
		t.Fatal("no min_size must include")
	}
}

func TestAnalyze_EmitsMutationContractOpenAndReadOnly(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	for _, tc := range []struct {
		in, want string
	}{
		{"open", "open"},
		{"read_only", "read_only"},
	} {
		report, err := Analyze(context.Background(), Options{
			Path: tmp, MinSize: "1", MaxDepth: 6, Top: 5,
			IncludeHomeCaches: false, MutationContract: tc.in,
			Now: time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if report.MutationContract != tc.want {
			t.Fatalf("%s: got %q", tc.in, report.MutationContract)
		}
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
			t.Fatalf("%s schema: %v", tc.in, err)
		}
	}
	if _, err := Analyze(context.Background(), Options{
		Path: tmp, MutationContract: "write", IncludeHomeCaches: false,
	}); err == nil {
		t.Fatal("invalid mutation_contract must fail")
	}
}

func TestAnalyze_IncompleteNotesSuggestInventoryNotReclaim(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "rust-app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "rust-app", "target", "shallow.bin"), 1)
	writeFile(t, filepath.Join(tmp, "rust-app", "target", "debug", "a", "b", "c", "deep.bin"), 1024*1024)

	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1",
		MaxDepth:          2,
		Top:               10,
		IncludeHomeCaches: false,
		Now:               time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !report.Verified.SizeIncomplete {
		t.Fatal("fixture must produce size_incomplete")
	}
	joined := strings.Join(report.Notes, "\n")
	if !strings.Contains(joined, "focused complete inventory") {
		t.Fatalf("notes must suggest focused complete inventory, not inferred reclaim:\n%s", joined)
	}
	if strings.Contains(joined, "Use prune for full-depth reclaim totals") {
		t.Fatalf("must not push reclaim from incomplete lower bounds:\n%s", joined)
	}
}

// completion must be self-consistent: complete means zero warnings, partial
// means at least one.
func TestSpaceReportSchema_CompletionConsistency(t *testing.T) {
	report := validMinimalReport(t)
	for _, tc := range []struct {
		c  Completion
		ok bool
	}{
		{Completion{Lifecycle: CompletionComplete, WarningCount: 0}, true},
		{Completion{Lifecycle: CompletionPartial, WarningCount: 2}, true},
		{Completion{Lifecycle: CompletionComplete, WarningCount: 1}, false},
		{Completion{Lifecycle: CompletionPartial, WarningCount: 0}, false},
		{Completion{Lifecycle: "failed", WarningCount: 0}, false},
	} {
		c := tc.c
		report.Completion = &c
		err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, mustJSON(t, report))
		if (err == nil) != tc.ok {
			t.Errorf("completion %+v: schema ok=%v want %v (%v)", c, err == nil, tc.ok, err)
		}
	}
	if got := NewCompletion(nil); got.Lifecycle != CompletionComplete || got.WarningCount != 0 {
		t.Errorf("NewCompletion(nil)=%+v", got)
	}
	if got := NewCompletion([]string{"a", "b"}); got.Lifecycle != CompletionPartial || got.WarningCount != 2 {
		t.Errorf("NewCompletion(2)=%+v", got)
	}
}
