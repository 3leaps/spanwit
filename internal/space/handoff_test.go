package space

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/engine"
)

func TestBuildPruneHandoff_OnlyPrunable(t *testing.T) {
	verified := VerifiedSection{
		Candidates: []Entry{
			{Path: "/dev/a/target", SizeBytes: 100, SizeHuman: "100B", State: StatePrunable, Signature: "development.rust.cargo-target", EffectiveMinSize: "1K", EffectiveMinAge: "none", EffectiveMaxAge: "none"},
			{Path: "/dev/b/target", SizeBytes: 50, SizeHuman: "50B", State: StateWithheld, WithheldReason: "min_size", SizeIncomplete: true},
			{Path: "/dev/c/target", SizeBytes: 200, SizeHuman: "200B", State: StateDiagnosticOnly},
			{Path: "/dev/d/target", SizeBytes: 300, SizeHuman: "300B", State: StatePrunable, Signature: "development.rust.cargo-target", EffectiveMinSize: "1K", EffectiveMinAge: "none", EffectiveMaxAge: "none"},
		},
	}
	h := BuildPruneHandoff(HandoffInput{Verified: verified, AnalysisRoot: "/dev", MinSizeDisplay: "1K"})
	if !h.Present {
		t.Fatal("expected present handoff")
	}
	if h.PrunableCount != 2 {
		t.Fatalf("count=%d", h.PrunableCount)
	}
	if h.SizeIncomplete {
		t.Fatal("incomplete withheld must not mark handoff partial")
	}
	if h.ExactPlan == nil || len(h.ExactPlan.Candidates) != 2 {
		t.Fatalf("exact plan: %#v", h.ExactPlan)
	}
	if HandoffSuggestsExecute(h) {
		t.Fatal("must not suggest execute")
	}
	joined := ""
	for _, c := range h.SuggestedCommands {
		joined += c.Display + "\n"
		if strings.Contains(joined, "crisis-dev") {
			t.Fatal("must not suggest crisis-dev")
		}
	}
	if !strings.Contains(joined, "--from-space-report") {
		t.Fatalf("expected from-space-report: %s", joined)
	}
}

func TestBuildPruneHandoff_EmbedsCustomSignature(t *testing.T) {
	custom := config.SignatureCatalog{
		"development": {
			"custom": {
				"build": {
					CandidatePatterns:     []string{"**/build-out"},
					RequiredAncestorFiles: []string{"BUILD.marker"},
					AnyChildPaths:         []string{"obj"},
					Confidence:            "high",
					SafeToPrune:           true,
				},
			},
		},
	}
	verified := VerifiedSection{
		Candidates: []Entry{{
			Path: "/tmp/x/build-out", SizeBytes: 10, SizeHuman: "10B", State: StatePrunable,
			Signature:        "development.custom.build",
			EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none",
		}},
	}
	h := BuildPruneHandoff(HandoffInput{
		Verified: verified, AnalysisRoot: "/tmp", MinSizeDisplay: "none", AdditiveSigs: custom,
	})
	if h.ExactPlan == nil {
		t.Fatal("missing exact plan")
	}
	if _, ok := h.ExactPlan.Signatures.Lookup("development.custom.build"); !ok {
		t.Fatal("custom signature not embedded in exact plan")
	}
}

func TestExactPlanToEngine_PreservesFilters(t *testing.T) {
	p := &ExactPlan{
		AnalysisRoot: "/a",
		Candidates: []ExactCandidate{{
			Path: "/a/target", Signature: "development.rust.cargo-target",
			EffectiveMinSize: "100M", EffectiveMinAge: "7d", EffectiveMaxAge: "none",
		}},
	}
	in := ExactPlanToEngine(p)
	if len(in.Entries) != 1 || in.Entries[0].EffectiveMinSize != "100M" {
		t.Fatalf("%#v", in)
	}
	// The deletion authorization boundary must survive the conversion: a dropped
	// analysis_root would leave execute with no non-inferred boundary.
	if in.AnalysisRoot != "/a" {
		t.Fatalf("analysis_root not carried through ExactPlanToEngine: %q", in.AnalysisRoot)
	}
	_ = engine.FilterDisplayNone
}

func TestBuildPruneHandoff_MismatchedRootNotInCommands(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "crate", "target")
	h := BuildPruneHandoff(HandoffInput{
		Verified: VerifiedSection{Candidates: []Entry{{
			Path: target, SizeBytes: 10, SizeHuman: "10B", State: StatePrunable,
			Signature:        "development.rust.cargo-target",
			EffectiveMinSize: "1", EffectiveMinAge: "none", EffectiveMaxAge: "none",
		}}},
		AnalysisRoot: tmp, MinSizeDisplay: "1",
	})
	for _, c := range h.SuggestedCommands {
		if strings.Contains(c.Display, "crisis-dev") || strings.Contains(c.Display, "~/dev") {
			t.Fatalf("unrelated root: %s", c.Display)
		}
	}
	if h.ExactPlan == nil || h.ExactPlan.Candidates[0].Path != target {
		t.Fatal(h.ExactPlan)
	}
}
