package space

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/engine"
)

func validExactPlanFixture(t *testing.T, path string) ExactPlan {
	t.Helper()
	return ExactPlan{
		Schema:       ExactPlanSchemaID,
		Version:      1,
		AnalysisRoot: "/tmp/analysis",
		MinSize:      engine.FilterDisplayNone,
		SortMode:     engine.SortIdle,
		ReclaimScope: engine.ReclaimScopeWhole,
		Candidates: []ExactCandidate{{
			Path:             path,
			Signature:        cargoTargetSignature,
			EffectiveMinSize: engine.FilterDisplayNone,
			EffectiveMinAge:  engine.FilterDisplayNone,
			EffectiveMaxAge:  engine.FilterDisplayNone,
			SizeBytes:        1024,
			SizeHuman:        "1.0 KiB",
			ReclaimScope:     engine.ReclaimScopeWhole,
		}},
	}
}

func TestParseAndValidateExactPlanJSON_OK(t *testing.T) {
	ep := validExactPlanFixture(t, "/tmp/analysis/crate/target")
	raw, err := json.Marshal(ep)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseAndValidateExactPlanJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Candidates[0].Path != ep.Candidates[0].Path {
		t.Fatalf("path: %q", got.Candidates[0].Path)
	}
}

func TestParseAndValidateExactPlanJSON_Rejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*ExactPlan)
		want string
	}{
		{"bad_schema", func(ep *ExactPlan) { ep.Schema = "nope" }, "$schema"},
		{"version_0", func(ep *ExactPlan) { ep.Version = 0 }, "version"},
		{"version_99", func(ep *ExactPlan) { ep.Version = 99 }, "version"},
		{"empty_root", func(ep *ExactPlan) { ep.AnalysisRoot = "" }, "analysis_root"},
		{"empty_candidates", func(ep *ExactPlan) { ep.Candidates = nil }, "candidates"},
		{"missing_reclaim", func(ep *ExactPlan) { ep.Candidates[0].ReclaimScope = "" }, "reclaim_scope"},
		{"missing_filter", func(ep *ExactPlan) { ep.Candidates[0].EffectiveMinSize = "" }, "effective_min_size"},
		{"bad_sort", func(ep *ExactPlan) { ep.SortMode = "hot" }, "sort_mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep := validExactPlanFixture(t, "/tmp/x/target")
			tc.mut(&ep)
			raw, _ := json.Marshal(ep)
			_, err := ParseAndValidateExactPlanJSON(raw)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseAndValidateExactPlanJSON_UnknownField(t *testing.T) {
	ep := validExactPlanFixture(t, "/tmp/x/target")
	raw, _ := json.Marshal(ep)
	// Inject unknown field
	s := strings.TrimSuffix(string(raw), "}")
	s += `,"extra_evil":true}`
	_, err := ParseAndValidateExactPlanJSON([]byte(s))
	if err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestCargoPlacement_ExactPlanCargoOnlyVsMixedVerified(t *testing.T) {
	// Journey exact_plan must not include non-Cargo prunables from verified set.
	root := t.TempDir()
	cargoTarget := filepath.Join(root, "crate", "target")
	otherPath := filepath.Join(root, "other-cache")
	verified := VerifiedSection{
		Candidates: []Entry{
			{
				Path: cargoTarget, SizeBytes: 2 << 20, State: StatePrunable,
				Signature: cargoTargetSignature, ReclaimScope: engine.ReclaimScopeWhole,
				EffectiveMinSize: engine.FilterDisplayNone, EffectiveMinAge: engine.FilterDisplayNone, EffectiveMaxAge: engine.FilterDisplayNone,
			},
			{
				Path: otherPath, SizeBytes: 9 << 20, State: StatePrunable,
				Signature: "development.go.go-build", ReclaimScope: engine.ReclaimScopeWhole,
				EffectiveMinSize: engine.FilterDisplayNone, EffectiveMinAge: engine.FilterDisplayNone, EffectiveMaxAge: engine.FilterDisplayNone,
			},
		},
		PrunableBytes: 11 << 20,
	}
	r, ok := BuildCargoPlacementJourney(CargoPlacementInput{
		Verified:     verified,
		Pressure:     Pressure{Level: PressureCritical},
		AnalysisRoot: root,
	})
	if !ok {
		t.Fatal("expected journey")
	}
	if r.ExactPlan == nil {
		t.Fatal("missing exact_plan")
	}
	if err := ValidateStandaloneExactPlan(r.ExactPlan); err != nil {
		t.Fatalf("recipe exact_plan must pass standalone validator: %v", err)
	}
	if len(r.ExactPlan.Candidates) != 1 || r.ExactPlan.Candidates[0].Path != cargoTarget {
		t.Fatalf("exact_plan must be cargo-only: %#v", r.ExactPlan.Candidates)
	}
	// Round-trip JSON as operator would write to file
	raw, err := json.Marshal(r.ExactPlan)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAndValidateExactPlanJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Candidates) != 1 || parsed.Candidates[0].Path != cargoTarget {
		t.Fatalf("round-trip path set: %#v", parsed.Candidates)
	}
}
