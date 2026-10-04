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

func TestAnalyze_ClassFilterPrunable_NoDiagnosticsInReport(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 4096)

	var partialHotspots int
	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1K",
		Top:               1,
		IncludeHomeCaches: true,
		DisableRecipes:    false,
		MaxDepth:          6,
		ClassFilters:      []string{"prunable"},
		Now:               time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
		OnPartial: func(u PartialUpdate) {
			if u.Phase == PhaseHotspots {
				partialHotspots = len(u.Hotspots)
				if len(u.Recipes) != 0 {
					t.Error("early recipes must be filtered out for --class prunable")
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if partialHotspots != 0 {
		t.Fatalf("early hotspots must be empty under --class prunable, got %d", partialHotspots)
	}
	if len(report.Hotspots) != 0 {
		t.Fatalf("final hotspots must be empty, got %d", len(report.Hotspots))
	}
	if len(report.Recipes) != 0 {
		t.Fatalf("final recipes must be empty, got %d", len(report.Recipes))
	}
	if report.AppliedFilters == nil || len(report.AppliedFilters.Classes) != 1 {
		t.Fatalf("applied_filters: %#v", report.AppliedFilters)
	}
	if report.Verified.PrunableCandidates == 0 {
		t.Fatal("expected prunable candidates")
	}
	// Handoff contains all prunables despite --top 1 display truncation.
	if report.PruneHandoff == nil || !report.PruneHandoff.Present {
		t.Fatal("expected handoff present")
	}
	if report.PruneHandoff.PrunableCount != report.Verified.PrunableCandidates {
		t.Fatalf("handoff count %d must equal full filtered prunable total %d",
			report.PruneHandoff.PrunableCount, report.Verified.PrunableCandidates)
	}
	// Display top=1 applies per action section (prunable-only filter → ≤1 row).
	if len(report.Verified.Candidates) > 1 {
		t.Fatalf("top=1 should truncate display candidates, got %d", len(report.Verified.Candidates))
	}

	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
		t.Fatalf("schema: %v\n%s", err, raw)
	}
}

func TestAnalyze_ClassDiagnosticOnly_NoPrunableHandoff(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 4096)

	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1K",
		IncludeHomeCaches: true,
		DisableRecipes:    true,
		MaxDepth:          6,
		ClassFilters:      []string{"diagnostic-only"},
		Now:               time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verified.PrunableCandidates != 0 || len(report.Verified.Candidates) != 0 {
		t.Fatalf("verified should be empty under diagnostic-only filter: %#v", report.Verified)
	}
	if report.PruneHandoff != nil && report.PruneHandoff.Present {
		t.Fatal("handoff must not be present for diagnostic-only filter")
	}
	// Tampered load: inject handoff with diagnostic-only applied_filters.
	report.PruneHandoff = &PruneHandoff{
		Present:       true,
		AnalysisRoot:  tmp,
		MinSize:       "1K",
		PrunableCount: 1,
		PrunableBytes: 1,
		PrunableHuman: "1B",
		Candidates: []Entry{{
			Path: filepath.Join(tmp, "app", "target"), SizeBytes: 1, SizeHuman: "1B",
			State: StatePrunable, ReclaimScope: "whole", Signature: "development.rust.cargo-target",
			EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none",
		}},
		ExactPlan: &ExactPlan{
			Schema: ExactPlanSchemaID, Version: 1, AnalysisRoot: tmp, MinSize: "1K",
			SortMode: "idle", ReclaimScope: "whole",
			Candidates: []ExactCandidate{{
				Path: filepath.Join(tmp, "app", "target"), Signature: "development.rust.cargo-target",
				EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none",
				SizeBytes: 1, SizeHuman: "1B", ReclaimScope: "whole",
			}},
		},
		SuggestedCommands: []SuggestedCommand{},
		Notes:             []string{"test"},
	}
	raw, _ := json.Marshal(report)
	if _, err := LoadSpaceReportJSON(raw); err == nil {
		t.Fatal("loader must reject handoff when applied_filters exclude prunable")
	}
}

func TestAnalyze_DomainFilter_SegmentAndHandoff(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 4096)

	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1K",
		IncludeHomeCaches: false,
		MaxDepth:          6,
		DomainFilters:     []string{"development.rust"},
		Now:               time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.AppliedFilters == nil || len(report.AppliedFilters.Domains) != 1 {
		t.Fatalf("applied domains: %#v", report.AppliedFilters)
	}
	for _, c := range report.Verified.Candidates {
		if c.Domain != "development" || c.Ecosystem != "rust" {
			t.Fatalf("taxonomy annotate: %#v", c)
		}
		if !TaxonomyMatches(c.Signature, "development.rust") {
			t.Fatalf("candidate outside domain: %s", c.Signature)
		}
	}
	if report.PruneHandoff == nil || !report.PruneHandoff.Present {
		t.Fatal("expected domain-matched prunable handoff")
	}
	// Boundary negative: develop must match zero.
	empty, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
		DomainFilters: []string{"develop"},
		Now:           time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Verified.PrunableCandidates != 0 {
		t.Fatal("develop must not match development.*")
	}
	if empty.PruneHandoff != nil && empty.PruneHandoff.Present {
		t.Fatal("empty filtered handoff must not be present")
	}
	// Still schema-valid with applied_filters.
	raw, _ := json.Marshal(empty)
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
		t.Fatalf("empty filtered schema: %v", err)
	}
	if _, err := LoadSpaceReportJSON(raw); err != nil {
		t.Fatalf("load empty filtered: %v", err)
	}
}

func TestAnalyze_NoMatchFilter_SchemaValidEmpty(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 4096)

	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
		ClassFilters:  []string{"unknown"},
		DomainFilters: []string{"development.rust"},
		Now:           time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.AppliedFilters == nil {
		t.Fatal("applied_filters required")
	}
	if report.Verified.PrunableCandidates != 0 || report.Verified.WithheldCandidates != 0 {
		t.Fatal("want zero verified under unknown∩rust")
	}
	raw, _ := json.Marshal(report)
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
		t.Fatalf("schema: %v", err)
	}
	loaded, err := LoadSpaceReportJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SortMode == "" || loaded.ReclaimScope == "" {
		t.Fatal("carrier must preserve sort/scope")
	}
}

func TestHotspotCatalogTags(t *testing.T) {
	for _, spec := range knownHotspots() {
		if spec.CatalogID == "" {
			t.Fatalf("hotspot %s missing CatalogID", spec.ID)
		}
		if err := ValidateDomainSyntax(spec.CatalogID); err != nil {
			// catalog_id is multi-segment; ValidateDomainSyntax applies.
			t.Fatalf("catalog %s: %v", spec.CatalogID, err)
		}
		d, e := DomainEcosystemFromSpine(spec.CatalogID)
		if d == "" || e == "" {
			t.Fatalf("catalog %s needs domain.ecosystem", spec.CatalogID)
		}
	}
}

// Incomplete hotspot rows ride outside the top-N budget: a denied or bounded
// path stays explicit instead of reading as a clean bill of health.
func TestTruncateHotspotDisplay_KeepsPartialRows(t *testing.T) {
	entries := []Entry{
		{Path: "/c1", SizeBytes: 300},
		{Path: "/c2", SizeBytes: 200},
		{Path: "/c3", SizeBytes: 100},
		{Path: "/trash", SizeBytes: 0, SizeIncomplete: true},
	}
	got := truncateHotspotDisplay(entries, 2)
	if len(got) != 3 {
		t.Fatalf("want 2 complete + 1 partial, got %d: %#v", len(got), got)
	}
	if got[0].Path != "/c1" || got[1].Path != "/c2" || got[2].Path != "/trash" {
		t.Fatalf("positions not preserved: %#v", got)
	}
	// Incomplete row inside the budget consumes no complete slot: top=1
	// over [partial, complete] keeps both (the reported devrev regression).
	interleaved := []Entry{
		{Path: "/partial", SizeBytes: 0, SizeIncomplete: true},
		{Path: "/c1", SizeBytes: 300},
		{Path: "/c2", SizeBytes: 200},
	}
	kept := truncateHotspotDisplay(interleaved, 1)
	if len(kept) != 2 || kept[0].Path != "/partial" || kept[1].Path != "/c1" {
		t.Fatalf("interleaved top=1: got %#v, want partial + top complete", kept)
	}
	// Incomplete row between complete rows: budget still counts completes.
	between := []Entry{
		{Path: "/c1", SizeBytes: 300},
		{Path: "/partial", SizeBytes: 0, SizeIncomplete: true},
		{Path: "/c2", SizeBytes: 200},
	}
	kept = truncateHotspotDisplay(between, 1)
	if len(kept) != 2 || kept[0].Path != "/c1" || kept[1].Path != "/partial" {
		t.Fatalf("between top=1: got %#v, want c1 + partial", kept)
	}
	// Complete rows past the budget still drop.
	dropped := true
	for _, e := range got {
		if e.Path == "/c3" {
			dropped = false
		}
	}
	if !dropped {
		t.Fatalf("complete row past budget retained: %#v", got)
	}
	// No cap means everything passes through, nil stays empty.
	if all := truncateHotspotDisplay(entries, 0); len(all) != 4 {
		t.Fatalf("top=0: got %d, want 4", len(all))
	}
	if none := truncateHotspotDisplay(nil, 2); len(none) != 0 {
		t.Fatalf("nil: got %d, want 0", len(none))
	}
}

func TestTruncateVerifiedDisplay_PerStateTop(t *testing.T) {
	entries := []Entry{
		{Path: "/p1", State: StatePrunable, SizeBytes: 300},
		{Path: "/w1", State: StateWithheld, SizeBytes: 50},
		{Path: "/p2", State: StatePrunable, SizeBytes: 200},
		{Path: "/p3", State: StatePrunable, SizeBytes: 100},
		{Path: "/w2", State: StateWithheld, SizeBytes: 40},
	}
	got := truncateVerifiedDisplay(entries, 1)
	if len(got) != 2 {
		t.Fatalf("want 1 prunable + 1 withheld, got %d: %#v", len(got), got)
	}
	if got[0].Path != "/p1" || got[0].State != StatePrunable {
		t.Fatalf("first display row: %#v", got[0])
	}
	if got[1].Path != "/w1" || got[1].State != StateWithheld {
		t.Fatalf("second display row: %#v", got[1])
	}
	got2 := truncateVerifiedDisplay(entries, 2)
	if len(got2) != 4 { // 2 prunable + 2 withheld
		t.Fatalf("top=2: got %d %#v", len(got2), got2)
	}
}

func TestAnalyze_TopPerActionSection_WithheldVisible(t *testing.T) {
	tmp := t.TempDir()
	// Three prunable cargo targets above min_size.
	for _, name := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(tmp, name, "Cargo.toml"), 32)
		writeFile(t, filepath.Join(tmp, name, "target", "debug", "x"), 4096)
	}
	// One withheld under min_size.
	writeFile(t, filepath.Join(tmp, "tiny", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "tiny", "target", "debug", "x"), 100)

	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1K",
		Top:               1,
		SortMode:          "size",
		IncludeHomeCaches: false,
		MaxDepth:          6,
		Now:               time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verified.PrunableCandidates < 3 {
		t.Fatalf("want ≥3 prunable totals, got %d", report.Verified.PrunableCandidates)
	}
	if report.Verified.WithheldCandidates < 1 {
		t.Fatalf("want ≥1 withheld total, got %d", report.Verified.WithheldCandidates)
	}
	var dispP, dispW int
	for _, c := range report.Verified.Candidates {
		switch c.State {
		case StatePrunable:
			dispP++
		case StateWithheld:
			dispW++
		}
	}
	if dispP != 1 || dispW != 1 {
		t.Fatalf("display per section top=1: prunable=%d withheld=%d candidates=%#v",
			dispP, dispW, report.Verified.Candidates)
	}
	if report.PruneHandoff == nil || report.PruneHandoff.PrunableCount != report.Verified.PrunableCandidates {
		t.Fatalf("handoff %v must equal full prunable total %d",
			report.PruneHandoff, report.Verified.PrunableCandidates)
	}
}

func TestAnalyze_FilteredReportDoesNotClaimHiddenIndeterminate(t *testing.T) {
	// Incomplete-below-floor diagnostic fixture: name-shaped unverified mass deep
	// under a path, with --class prunable projecting diagnostics away.
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "proj", "target", "n0", "n1", "n2", "n3", "mass.bin"), 2*1024*1024)
	// Also a verified cargo target that is complete/prunable so class=prunable is valid.
	writeFile(t, filepath.Join(tmp, "rust", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "rust", "target", "debug", "a.o"), 2048)

	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "10M",
		MaxDepth:          2,
		Top:               10,
		IncludeHomeCaches: false,
		ClassFilters:      []string{StatePrunable},
		Now:               time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if report.Verified.SizeIncomplete {
		t.Fatalf("projected verified size_incomplete must be false when no incomplete verified rows visible: %#v", report.Verified)
	}
	if report.Unverified.Count != 0 || len(report.Unverified.Entries) != 0 {
		t.Fatalf("class=prunable must hide unverified diagnostics: %#v", report.Unverified)
	}
	joinedNotes := strings.Join(report.Notes, "\n")
	joinedWarn := strings.Join(report.Warnings, "\n")
	if strings.Contains(joinedNotes, "min_size threshold is indeterminate") ||
		strings.Contains(joinedWarn, "min_size indeterminate") {
		t.Fatalf("must not claim hidden indeterminate diagnostic rows stay visible:\nnotes=%s\nwarnings=%s", joinedNotes, joinedWarn)
	}
	if strings.Contains(joinedNotes, "depth-bounded partial measurements") && report.Verified.SizeIncomplete == false && report.Unverified.SizeIncomplete == false {
		// verified incomplete note only when projected incomplete
		if strings.Contains(joinedNotes, "Some verified sizes are depth-bounded") {
			t.Fatalf("must not claim verified incomplete when projected size_incomplete=false:\n%s", joinedNotes)
		}
	}
}

func TestAnnotateVerifiedBoundState_IndependentSubtotals(t *testing.T) {
	v := VerifiedSection{
		Candidates: []Entry{
			{Path: "/p", State: StatePrunable, SizeBytes: 100, SizeIncomplete: true},
			{Path: "/w", State: StateWithheld, SizeBytes: 50, SizeIncomplete: false},
		},
		PrunableCandidates: 1, PrunableBytes: 100,
		WithheldCandidates: 1, WithheldBytes: 50,
	}
	annotateVerifiedBoundState(&v)
	if !v.SizeIncomplete || !v.PrunableSizeIncomplete || v.WithheldSizeIncomplete {
		t.Fatalf("want section+prunable incomplete only: %#v", v)
	}
	v2 := VerifiedSection{
		Candidates: []Entry{
			{Path: "/p", State: StatePrunable, SizeBytes: 100, SizeIncomplete: false},
			{Path: "/w", State: StateWithheld, SizeBytes: 50, SizeIncomplete: true},
		},
	}
	annotateVerifiedBoundState(&v2)
	if !v2.SizeIncomplete || v2.PrunableSizeIncomplete || !v2.WithheldSizeIncomplete {
		t.Fatalf("want section+withheld incomplete only: %#v", v2)
	}
}

func TestLoadSpaceReportJSON_RejectsDomainTamperedHandoff(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 4096)

	report, err := Analyze(context.Background(), Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
		DomainFilters: []string{"development.rust"},
		Now:           time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.PruneHandoff == nil || !report.PruneHandoff.Present {
		t.Fatal("need handoff for tamper test")
	}
	// Inject nonmatching signature while keeping domain filter advertised.
	report.PruneHandoff.Candidates[0].Signature = "development.go.go-build"
	report.PruneHandoff.Candidates[0].Domain = "development"
	report.PruneHandoff.Candidates[0].Ecosystem = "go"
	if report.PruneHandoff.ExactPlan != nil && len(report.PruneHandoff.ExactPlan.Candidates) > 0 {
		report.PruneHandoff.ExactPlan.Candidates[0].Signature = "development.go.go-build"
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpaceReportJSON(raw); err == nil {
		t.Fatal("loader must reject handoff signature outside applied domain filters")
	}
}

func TestBuildPruneHandoff_SuggestedSpaceCarriesFilters(t *testing.T) {
	h := BuildPruneHandoff(HandoffInput{
		Verified: VerifiedSection{Candidates: []Entry{{
			Path: "/x/target", SizeBytes: 10, SizeHuman: "10B", State: StatePrunable,
			Signature:        "development.rust.cargo-target",
			EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none",
		}}},
		AnalysisRoot: "/x", MinSizeDisplay: "none", SortMode: "idle", ReclaimScope: "whole",
		ClassFilters: []string{"prunable"}, DomainFilters: []string{"development.rust"},
	})
	joined := ""
	for _, c := range h.SuggestedCommands {
		joined += c.Display + " "
	}
	for _, part := range []string{"--class", "prunable", "--domain", "development.rust", "--sort", "idle"} {
		if !strings.Contains(joined, part) {
			t.Fatalf("suggested space must carry %q: %s", part, joined)
		}
	}
}
