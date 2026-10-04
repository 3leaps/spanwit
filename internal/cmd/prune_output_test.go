package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

// TestWritePlanText_RendersPrunableAndWithheld locks the shared read-only
// renderer used by both `prune` (dry-run listing) and the context-aware `scan`
// front end: prunable and withheld matches render in separate labelled tables,
// with the signature/pattern label and the withheld reason.
func TestWritePlanText_RendersPrunableAndWithheld(t *testing.T) {
	plan := PrunePlan{
		Candidates: []PruneCandidate{
			{
				Path:       "/x/target",
				Size:       2048,
				State:      candidateStatePrunable,
				Signature:  "development.rust.cargo-target",
				Confidence: "high",
			},
			{
				Path:           "/y/node_modules",
				Size:           1024,
				State:          candidateStateWithheld,
				WithheldReason: withheldReasonSafeToPrune,
				Pattern:        "**/node_modules",
			},
		},
	}

	var buf bytes.Buffer
	writePlanText(&buf, plan)
	out := buf.String()

	for _, want := range []string{
		"Effective filters: min_size=none  min_age=none  max_age=none",
		"Prune candidates:",
		"/x/target",
		"development.rust.cargo-target",
		"[filters min_size=none min_age=none max_age=none]",
		"Total reclaimable:",
		"Withheld (matched but not eligible for deletion):",
		"/y/node_modules",
		"[withheld: " + withheldReasonSafeToPrune + "]",
		"Withheld: 1 candidates (",
		") by " + withheldReasonSafeToPrune,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

// TestWritePlanText_EmptyPlan confirms the empty-plan message and that the
// read-only renderer never references deletion or --execute. The effective
// filters banner is always present so silent policy is impossible.
func TestWritePlanText_EmptyPlan(t *testing.T) {
	var buf bytes.Buffer
	writePlanText(&buf, PrunePlan{
		EffectiveFilters: EffectiveFilters{MinSize: "none", MinAge: "none", MaxAge: "none"},
	})
	out := buf.String()
	if !strings.Contains(out, "Effective filters: min_size=none  min_age=none  max_age=none") {
		t.Fatalf("expected filters banner, got:\n%s", out)
	}
	if !strings.Contains(out, "No prune candidates found.") {
		t.Fatalf("expected empty-plan message, got:\n%s", out)
	}
	if strings.Contains(out, "execute") || strings.Contains(strings.ToLower(out), "delete") {
		// "deletion" appears in the withheld header only when there are withheld
		// rows; empty plan must stay free of execute/delete language.
		t.Fatalf("read-only renderer must not mention execution/deletion, got:\n%s", out)
	}
}

func TestWritePlanText_WithheldByAgeAndMinSizeSummary(t *testing.T) {
	plan := PrunePlan{
		EffectiveFilters: EffectiveFilters{MinSize: "1K", MinAge: "24h", MaxAge: filterDisplayNone},
		Candidates: []PruneCandidate{
			{Path: "/a", Size: 2048, State: candidateStateWithheld, WithheldReason: withheldReasonAge, Pattern: "**/target", EffectiveMinSize: "1K", EffectiveMinAge: "24h", EffectiveMaxAge: filterDisplayNone},
			{Path: "/b", Size: 512, State: candidateStateWithheld, WithheldReason: withheldReasonMinSize, Pattern: "**/target", EffectiveMinSize: "1K", EffectiveMinAge: "24h", EffectiveMaxAge: filterDisplayNone},
			{Path: "/c", Size: 4096, State: candidateStatePrunable, Pattern: "**/target", EffectiveMinSize: "1K", EffectiveMinAge: "24h", EffectiveMaxAge: filterDisplayNone},
		},
	}
	var buf bytes.Buffer
	writePlanText(&buf, plan)
	out := buf.String()
	for _, want := range []string{
		"Effective filters: min_size=1K  min_age=24h  max_age=none",
		"Total reclaimable:",
		"[withheld: age]",
		"[withheld: min_size]",
		"[filters min_size=1K min_age=24h max_age=none]",
		"Withheld: 1 candidates (",
		") by age; 1 candidates (",
		") by min_size",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in:\n%s", want, out)
		}
	}
}

func TestPrunePlanJSON_WithheldReasonIndeterminate(t *testing.T) {
	plan := PrunePlan{
		SortMode:     "size",
		ReclaimScope: "whole",
		EffectiveFilters: EffectiveFilters{
			MinSize: "1G", MinAge: filterDisplayNone, MaxAge: filterDisplayNone,
		},
		Candidates: []PruneCandidate{{
			Path:             "/tmp/app/target",
			Size:             100,
			SizeIncomplete:   true,
			State:            candidateStateWithheld,
			WithheldReason:   withheldReasonIndeterminate,
			Signature:        "development.rust.cargo-target",
			Confidence:       "high",
			EffectiveMinSize: "1G",
			EffectiveMinAge:  filterDisplayNone,
			EffectiveMaxAge:  filterDisplayNone,
			ReclaimScope:     "whole",
		}},
	}
	out := newPrunePlanOutput(plan, "/tmp/config.yaml", false, time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	if len(out.Candidates) != 1 || out.Candidates[0].WithheldReason != withheldReasonIndeterminate {
		t.Fatalf("mapper must emit withheld_reason=indeterminate: %#v", out.Candidates)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitPrunePlanV1, raw); err != nil {
		t.Fatalf("schema must accept indeterminate: %v\n%s", err, raw)
	}
	// No second incompleteness truth field on prune-plan candidate output.
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	cands, _ := decoded["candidates"].([]any)
	if len(cands) != 1 {
		t.Fatalf("candidates: %#v", cands)
	}
	c0, _ := cands[0].(map[string]any)
	if c0["withheld_reason"] != "indeterminate" {
		t.Fatalf("json withheld_reason=%v", c0["withheld_reason"])
	}
	for _, forbidden := range []string{"size_incomplete", "size_partial", "bound_state", "threshold_outcome"} {
		if _, ok := c0[forbidden]; ok {
			t.Fatalf("must not introduce second incompleteness field %q", forbidden)
		}
	}
}

func TestWritePlanText_PathOverrideFiltersOnRow(t *testing.T) {
	plan := PrunePlan{
		EffectiveFilters: EffectiveFilters{MinSize: filterDisplayNone, MinAge: "90d", MaxAge: filterDisplayNone},
		Candidates: []PruneCandidate{
			{
				Path:             "/fresh/target",
				Size:             2048,
				State:            candidateStateWithheld,
				WithheldReason:   withheldReasonAge,
				Signature:        "development.rust.cargo-target",
				EffectiveMinSize: filterDisplayNone,
				EffectiveMinAge:  "90d",
				EffectiveMaxAge:  filterDisplayNone,
			},
		},
	}
	var buf bytes.Buffer
	writePlanText(&buf, plan)
	out := buf.String()
	for _, want := range []string{
		"Effective filters: min_size=none  min_age=90d  max_age=none",
		"[withheld: age]",
		"[filters min_size=none min_age=90d max_age=none]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in:\n%s", want, out)
		}
	}
}

// Idle evidence: each text row shows its activity, and anything short of a
// confidently known timestamp prints "activity unknown" — never "idle".
func TestWritePlanText_ActivityEvidenceNeverUpgradesUnknown(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	orig := planTextNow
	planTextNow = func() time.Time { return now }
	t.Cleanup(func() { planTextNow = orig })

	known := now.Add(-13 * 24 * time.Hour)
	plan := PrunePlan{
		SortMode: "idle",
		Candidates: []PruneCandidate{
			{Path: "/a/target", Size: 10, State: candidateStatePrunable, Signature: "development.rust.cargo-target",
				LastActivityAt: known, ActivityBasis: "descendant_mtime"},
			{Path: "/b/target", Size: 10, State: candidateStatePrunable, Signature: "development.rust.cargo-target",
				LastActivityAt: known, ActivityBasis: "descendant_mtime", ActivityIncomplete: true},
			{Path: "/c/target", Size: 10, State: candidateStatePrunable, Signature: "development.rust.cargo-target",
				ActivityBasis: "unknown"},
			{Path: "/d/target", Size: 10, State: candidateStateWithheld, WithheldReason: "age",
				Signature: "development.rust.cargo-target"},
		},
	}
	var buf bytes.Buffer
	writePlanText(&buf, plan)
	out := buf.String()

	if !strings.Contains(out, "Ranking: sort_mode=idle (idle = oldest known activity first; unknown activity last; advisory only)") {
		t.Fatalf("missing ranking header:\n%s", out)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		for _, p := range []string{"/a/target", "/b/target", "/c/target", "/d/target"} {
			if strings.Contains(line, p) {
				rows[p] = line
			}
		}
	}
	if !strings.Contains(rows["/a/target"], "last 2026-09-12 (13d)") {
		t.Errorf("known row: %q", rows["/a/target"])
	}
	for _, p := range []string{"/b/target", "/c/target", "/d/target"} {
		if !strings.Contains(rows[p], "activity unknown") {
			t.Errorf("%s must show activity unknown: %q", p, rows[p])
		}
	}
	for p, line := range rows {
		if strings.Contains(strings.ToLower(line), "idle") {
			t.Errorf("row %s labeled idle: %q", p, line)
		}
	}
}

// The JSON plan keeps the activity fields the text column is derived from.
func TestPlanJSON_KeepsActivityFields(t *testing.T) {
	ts := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	plan := PrunePlan{SortMode: "idle", Candidates: []PruneCandidate{{
		Path: "/a/target", Size: 10, State: candidateStatePrunable, Signature: "development.rust.cargo-target",
		Confidence: "high", LastActivityAt: ts, ActivityBasis: "descendant_mtime", ActivityIncomplete: true,
	}}}
	var buf bytes.Buffer
	if err := writePrunePlanJSON(&buf, plan, "/cfg/spanwit.yaml", false, time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Candidates []map[string]any `json:"candidates"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Candidates) != 1 {
		t.Fatalf("candidates=%v", doc.Candidates)
	}
	c := doc.Candidates[0]
	if c["last_activity_at"] == nil || c["activity_basis"] != "descendant_mtime" || c["activity_incomplete"] != true {
		t.Fatalf("activity fields not carried: %v", c)
	}
}
