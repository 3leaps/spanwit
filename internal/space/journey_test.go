package space

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/catalog"
	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/engine"
)

func TestBuildCargoPlacementJourney_EmitsStructuralSteps(t *testing.T) {
	root := t.TempDir()
	projA := filepath.Join(root, "crate-a")
	projB := filepath.Join(root, "crate-b")
	targetA := filepath.Join(projA, "target")
	targetB := filepath.Join(projB, "target")

	verified := VerifiedSection{
		Candidates: []Entry{
			{
				Path:         targetA,
				SizeBytes:    4 << 30,
				SizeHuman:    "4.0 GiB",
				State:        StatePrunable,
				Signature:    cargoTargetSignature,
				ReclaimScope: engine.ReclaimScopeWhole,
			},
			{
				Path:         targetB,
				SizeBytes:    1 << 30,
				SizeHuman:    "1.0 GiB",
				State:        StatePrunable,
				Signature:    cargoTargetSignature,
				ReclaimScope: engine.ReclaimScopeWhole,
			},
		},
		PrunableCandidates: 2,
		PrunableBytes:      5 << 30,
	}
	handoff := BuildPruneHandoff(HandoffInput{
		Verified:       verified,
		AnalysisRoot:   root,
		MinSizeDisplay: "none",
		SortMode:       engine.SortIdle,
		ReclaimScope:   engine.ReclaimScopeWhole,
	})

	r, ok := BuildCargoPlacementJourney(CargoPlacementInput{
		Verified: verified,
		Pressure: Pressure{
			Level:      PressureCritical,
			AvailBytes: 1 << 30,
			VolumeID:   "vol-primary",
		},
		AnalysisRoot: root,
		Handoff:      &handoff,
	})
	if !ok {
		t.Fatal("expected cargo placement journey")
	}
	if r.ID != catalog.RecipeCargoTargetPlacement {
		t.Fatalf("id: got %q", r.ID)
	}
	if r.Kind != RecipeKindStructural {
		t.Fatalf("kind: got %q", r.Kind)
	}
	if r.State != StateDiagnosticOnly {
		t.Fatalf("state must stay diagnostic-only: %q", r.State)
	}
	if len(r.Steps) < 4 {
		t.Fatalf("expected multi-step journey, got %d steps: %#v", len(r.Steps), r.Steps)
	}
	kinds := map[string]int{}
	for _, s := range r.Steps {
		kinds[s.Kind]++
		if s.ID == "" || s.Title == "" {
			t.Fatalf("step missing id/title: %#v", s)
		}
	}
	// Without proven destination separation: observe + purge + choose-destination + verify.
	for _, want := range []string{StepObserve, StepPurgeHint, StepVerify} {
		if kinds[want] == 0 {
			t.Fatalf("missing step kind %q in %#v", want, r.Steps)
		}
	}
	// Evidence paths must list both project roots (size-desc: crate-a first).
	observe := r.Steps[0]
	if len(observe.Paths) != 2 {
		t.Fatalf("observe paths: %#v", observe.Paths)
	}
	if observe.Paths[0] != projA {
		t.Fatalf("largest project first: got %q want %q", observe.Paths[0], projA)
	}
	// Purge step must use --from-exact-plan and never invent --execute.
	for _, s := range r.Steps {
		if s.Kind != StepPurgeHint || s.Command == nil {
			continue
		}
		joined := s.Command.Display + " " + strings.Join(s.Command.Args, " ")
		if strings.Contains(joined, "--execute") {
			t.Fatalf("purge hint must not suggest --execute: %s", joined)
		}
		if !strings.Contains(joined, "--from-exact-plan") {
			t.Fatalf("purge must use --from-exact-plan: %s", joined)
		}
	}
	if r.ExactPlan == nil {
		t.Fatal("expected embedded cargo exact_plan")
	}
	if len(r.SuggestedCommands) == 0 {
		t.Fatal("structural recipe must still carry suggested_commands for schema")
	}
	// Projection parity: suggested_commands == command-bearing steps.
	proj := projectCommandsFromSteps(r.Steps)
	if len(proj) != len(r.SuggestedCommands) {
		t.Fatalf("suggested_commands projection drift: %d vs %d", len(r.SuggestedCommands), len(proj))
	}
}

func TestBuildCargoPlacementJourney_NoCargoTargets(t *testing.T) {
	_, ok := BuildCargoPlacementJourney(CargoPlacementInput{
		Verified: VerifiedSection{Candidates: []Entry{{
			Path:      "/tmp/other",
			State:     StatePrunable,
			Signature: "development.go.something",
		}}},
		Pressure:     Pressure{Level: PressureCritical},
		AnalysisRoot: t.TempDir(),
	})
	if ok {
		t.Fatal("expected no journey without cargo-target rows")
	}
}

func TestBuildCargoPlacementJourney_DomainDisabled(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "app", "target")
	verified := VerifiedSection{
		Candidates: []Entry{{
			Path:         target,
			SizeBytes:    1 << 20,
			State:        StatePrunable,
			Signature:    cargoTargetSignature,
			ReclaimScope: engine.ReclaimScopeWhole,
		}},
		PrunableBytes: 1 << 20,
	}
	_, ok := BuildCargoPlacementJourney(CargoPlacementInput{
		Verified: verified,
		Pressure: Pressure{Level: PressureWarn},
		Enabled: config.EnabledDomains{
			Set:        map[string]bool{"media": true},
			List:       []string{"media"},
			Configured: true,
		},
		AnalysisRoot: root,
	})
	if ok {
		t.Fatal("development-disabled policy must suppress cargo placement journey")
	}
}

func TestBuildCargoPlacementJourney_SkipsIncrementalOnly(t *testing.T) {
	root := t.TempDir()
	// Incremental reclaim_scope is a subpath; project-root inference requires whole target/.
	verified := VerifiedSection{
		Candidates: []Entry{{
			Path:         filepath.Join(root, "app", "target", "debug", "incremental"),
			SizeBytes:    1 << 30,
			State:        StatePrunable,
			Signature:    cargoTargetSignature,
			ReclaimScope: engine.ReclaimScopeIncremental,
			ParentPath:   filepath.Join(root, "app", "target"),
		}},
		PrunableBytes: 1 << 30,
	}
	_, ok := BuildCargoPlacementJourney(CargoPlacementInput{
		Verified:     verified,
		Pressure:     Pressure{Level: PressureCritical},
		AnalysisRoot: root,
	})
	if ok {
		t.Fatal("incremental-only candidates must not drive whole-target placement journey")
	}
}

func TestSafeProjectLeaf(t *testing.T) {
	if got := safeProjectLeaf("/tmp/my crate!"); got == "" || strings.ContainsAny(got, " !") {
		t.Fatalf("unsafe leaf: %q", got)
	}
	if got := safeProjectLeaf("/"); got != "project" {
		t.Fatalf("root leaf: %q", got)
	}
}

func TestUniqueProjectDestName_CollidingBasenames(t *testing.T) {
	a := uniqueProjectDestName("/Users/me/work/a/app")
	b := uniqueProjectDestName("/Users/me/work/b/app")
	if a == b {
		t.Fatalf("same basename roots must not share dest name: %q", a)
	}
	if !strings.HasPrefix(a, "app-") || !strings.HasPrefix(b, "app-") {
		t.Fatalf("expected app-<hash> form: %q %q", a, b)
	}
	// Deterministic for a given root.
	if uniqueProjectDestName("/Users/me/work/a/app") != a {
		t.Fatal("dest name must be stable for the same root")
	}
}

func TestBuildCargoPlacementJourney_NeverEmitsConcreteDestExport(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "app")
	verified := VerifiedSection{
		Candidates: []Entry{{
			Path: filepath.Join(proj, "target"), SizeBytes: 1 << 20, State: StatePrunable,
			Signature: cargoTargetSignature, ReclaimScope: engine.ReclaimScopeWhole,
			EffectiveMinSize: engine.FilterDisplayNone, EffectiveMinAge: engine.FilterDisplayNone, EffectiveMaxAge: engine.FilterDisplayNone,
		}},
		PrunableBytes: 1 << 20,
	}
	// Even with distinct roomier analysis volume, targets under analysis_root
	// must not get concrete CARGO_TARGET_DIR exports (same filesystem).
	r, ok := BuildCargoPlacementJourney(CargoPlacementInput{
		Verified: verified,
		Pressure: Pressure{Level: PressureCritical, AvailBytes: 1 << 30, VolumeID: "vol-primary"},
		AnalysisPressure: &Pressure{
			Level: PressureOK, AvailBytes: 100 << 30, VolumeID: "vol-roomy",
		},
		AnalysisRoot: root,
	})
	if !ok {
		t.Fatal("expected journey")
	}
	for _, s := range r.Steps {
		if s.ID == "config-direnv" || s.ID == "propose-layout" || s.ConfigSnippet != nil {
			t.Fatalf("016A must not emit concrete dest export: %#v", s)
		}
	}
	var sawChoose bool
	for _, s := range r.Steps {
		if s.ID == "choose-destination" {
			sawChoose = true
		}
	}
	if !sawChoose {
		t.Fatal("expected choose-destination precondition step")
	}
}

func TestBuildCargoPlacementJourney_PurgeUsesFromExactPlan(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "crate")
	target := filepath.Join(proj, "target")
	verified := VerifiedSection{
		Candidates: []Entry{{
			Path: target, SizeBytes: 1 << 20, State: StatePrunable,
			Signature: cargoTargetSignature, ReclaimScope: engine.ReclaimScopeWhole,
			EffectiveMinSize: engine.FilterDisplayNone, EffectiveMinAge: engine.FilterDisplayNone, EffectiveMaxAge: engine.FilterDisplayNone,
		}},
		PrunableBytes: 1 << 20,
	}
	r, ok := BuildCargoPlacementJourney(CargoPlacementInput{
		Verified:     verified,
		Pressure:     Pressure{Level: PressureCritical},
		AnalysisRoot: root,
	})
	if !ok {
		t.Fatal("expected journey")
	}
	if r.ExactPlan == nil || len(r.ExactPlan.Candidates) != 1 {
		t.Fatalf("expected cargo-only exact_plan: %#v", r.ExactPlan)
	}
	if r.ExactPlan.Candidates[0].Path != target {
		t.Fatalf("exact_plan path: %q", r.ExactPlan.Candidates[0].Path)
	}
	var purge *RecipeStep
	for i := range r.Steps {
		if r.Steps[i].ID == "purge-before-relocate" {
			purge = &r.Steps[i]
			break
		}
	}
	if purge == nil || purge.Command == nil {
		t.Fatal("missing purge command")
	}
	joined := purge.Command.Display + " " + strings.Join(purge.Command.Args, " ")
	if !strings.Contains(joined, "--from-exact-plan") {
		t.Fatalf("purge must use --from-exact-plan: %s", joined)
	}
	if strings.Contains(joined, "--from-space-report") {
		t.Fatalf("purge must not use report-wide --from-space-report: %s", joined)
	}
	if strings.Contains(joined, "--execute") {
		t.Fatalf("must not suggest --execute: %s", joined)
	}
}

// End-to-end: Analyze over a cargo fixture emits a schema-valid structural recipe.
func TestAnalyze_EmitsCargoPlacementStructuralRecipe(t *testing.T) {
	// Force warn/critical-independent path: fixture has prunable cargo targets.
	// DisableRecipes would suppress the journey; leave recipes enabled.
	report := analyzeWithDomains(t, cargoFixture(t), nil)
	var found *Recipe
	for i := range report.Recipes {
		if report.Recipes[i].ID == catalog.RecipeCargoTargetPlacement {
			found = &report.Recipes[i]
			break
		}
	}
	if found == nil {
		// Pressure may be ok on a roomy CI/dev volume; journey still requires
		// prunable cargo mass OR tight pressure. Fixture has prunable mass.
		t.Fatalf("expected cargo-target-placement structural recipe; recipes=%#v verified=%#v pressure=%#v",
			report.Recipes, report.Verified, report.Pressure)
	}
	if found.Kind != RecipeKindStructural {
		t.Fatalf("kind: %q", found.Kind)
	}
	if len(found.Steps) == 0 {
		t.Fatal("structural recipe missing steps")
	}
}
