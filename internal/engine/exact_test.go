package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/3leaps/spanwit/internal/config"
)

func TestPlanFromExact_CustomSignature(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "proj", "BUILD.marker"), 8)
	writeFile(t, filepath.Join(tmp, "proj", "build-out", "obj", "a"), 2048)

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
	path := filepath.Join(tmp, "proj", "build-out")
	plan, err := PlanFromExact(context.Background(), ExactPlanInput{
		AnalysisRoot: tmp,
		Entries: []ExactEntry{{
			Path: path, Signature: "development.custom.build",
			EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone,
		}},
		Signatures: custom,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].State != CandidateStatePrunable {
		t.Fatalf("plan=%+v warnings=%v", plan.Candidates, plan.Warnings)
	}
	if plan.Candidates[0].Signature != "development.custom.build" {
		t.Fatal(plan.Candidates[0].Signature)
	}
}

func TestPlanFromExact_BareAllowlistMissesCustom(t *testing.T) {
	// Documents why bare allowlist cannot execute custom signatures.
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "proj", "BUILD.marker"), 8)
	writeFile(t, filepath.Join(tmp, "proj", "build-out", "obj", "a"), 2048)
	path := filepath.Join(tmp, "proj", "build-out")
	plan, err := PlanFromAllowlist(context.Background(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("bare allowlist must not invent custom matches: %+v", plan.Candidates)
	}
}

func TestPlanFromExact_FilterMinSizeWithholds(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 100) // tiny
	path := filepath.Join(tmp, "app", "target")
	plan, err := PlanFromExact(context.Background(), ExactPlanInput{
		AnalysisRoot: tmp,
		Entries: []ExactEntry{{
			Path: path, Signature: "development.rust.cargo-target",
			EffectiveMinSize: "1G", // filter from report
			EffectiveMinAge:  FilterDisplayNone,
			EffectiveMaxAge:  FilterDisplayNone,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("want 1 candidate, got %d (%v)", len(plan.Candidates), plan.Warnings)
	}
	if plan.Candidates[0].State != CandidateStateWithheld || plan.Candidates[0].WithheldReason != WithheldReasonMinSize {
		t.Fatalf("want withheld min_size: %+v", plan.Candidates[0])
	}
}

func TestPlanFromExact_MissingAnalysisRootFailsClosed(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	// No AnalysisRoot: a non-empty exact plan without a carried boundary must
	// fail closed rather than infer one.
	_, err := PlanFromExact(context.Background(), ExactPlanInput{
		Entries: []ExactEntry{{
			Path: filepath.Join(tmp, "app", "target"), Signature: "development.rust.cargo-target",
			EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone,
		}},
	})
	if err == nil {
		t.Fatal("expected fail-closed error when analysis_root is missing")
	}
}

func TestPlanFromExact_TamperedCandidateOutsideAnalysisRoot(t *testing.T) {
	tmp := t.TempDir()
	authorized := filepath.Join(tmp, "authorized")
	if err := os.MkdirAll(authorized, 0o755); err != nil {
		t.Fatal(err)
	}
	// A real cargo target, but OUTSIDE the carried analysis_root.
	writeFile(t, filepath.Join(tmp, "elsewhere", "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "elsewhere", "app", "target", "debug", "a"), 2048)
	tampered := filepath.Join(tmp, "elsewhere", "app", "target")

	// A structural boundary violation must be a hard plan error (nothing deletes),
	// not a silent drop-and-continue.
	_, err := PlanFromExact(context.Background(), ExactPlanInput{
		AnalysisRoot: authorized,
		Entries: []ExactEntry{{
			Path: tampered, Signature: "development.rust.cargo-target",
			EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone,
		}},
	})
	if err == nil {
		t.Fatal("tampered candidate outside analysis_root must be a hard error")
	}
}

func TestPlanFromExact_RawCarrierAuthorityMustBeCleanAbs(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	target := filepath.Join(tmp, "app", "target")
	unclean := tmp + string(filepath.Separator) + "app" + string(filepath.Separator) + ".." + string(filepath.Separator) + "app" + string(filepath.Separator) + "target"

	// analysis_root position: relative and unclean must both be hard errors, and
	// must never be laundered into a clean-absolute boundary.
	for _, root := range []string{"relative/root", "~/root", unclean} {
		if _, err := PlanFromExact(context.Background(), ExactPlanInput{
			AnalysisRoot: root,
			Entries: []ExactEntry{{
				Path: target, Signature: "development.rust.cargo-target",
				EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone,
			}},
		}); err == nil {
			t.Fatalf("analysis_root %q must be rejected as not absolute+clean", root)
		}
	}

	// candidate Path position.
	for _, p := range []string{"relative/target", "~/target", unclean} {
		if _, err := PlanFromExact(context.Background(), ExactPlanInput{
			AnalysisRoot: tmp,
			Entries: []ExactEntry{{
				Path: p, Signature: "development.rust.cargo-target",
				EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone,
			}},
		}); err == nil {
			t.Fatalf("candidate path %q must be rejected as not absolute+clean", p)
		}
	}

	// incremental parent_path position (leaf must exist to reach the parent check).
	leaf := filepath.Join(tmp, "app", "target", "deadbeefdeadbeef", "incremental")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, pp := range []string{"relative/parent", "~/parent", unclean} {
		if _, err := PlanFromExact(context.Background(), ExactPlanInput{
			AnalysisRoot: tmp,
			Entries: []ExactEntry{{
				Path: leaf, Signature: "development.rust.cargo-target",
				EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone,
				ReclaimScope: ReclaimScopeIncremental, ParentPath: pp,
			}},
		}); err == nil {
			t.Fatalf("incremental parent_path %q must be rejected as not absolute+clean", pp)
		}
	}
}

func TestPlanFromExact_CarriesAnalysisRootProvenance(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	plan, err := PlanFromExact(context.Background(), ExactPlanInput{
		AnalysisRoot: tmp,
		Entries: []ExactEntry{{
			Path: filepath.Join(tmp, "app", "target"), Signature: "development.rust.cargo-target",
			EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("want 1 candidate, got %d (%v)", len(plan.Candidates), plan.Warnings)
	}
	c := plan.Candidates[0]
	if c.ProvenanceKind != ProvenanceExactPlanAnalysis {
		t.Fatalf("provenance=%q", c.ProvenanceKind)
	}
	if c.AuthorizationBoundary != filepath.Clean(tmp) {
		t.Fatalf("boundary=%q want %q", c.AuthorizationBoundary, filepath.Clean(tmp))
	}
}

func TestPlanFromExact_RejectsSymlinkAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlink fixture")
	}
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside", "app")
	writeFile(t, filepath.Join(outside, "Cargo.toml"), 32)
	writeFile(t, filepath.Join(outside, "target", "debug", "a"), 2048)
	link := filepath.Join(tmp, "root", "link")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	via := filepath.Join(link, "target")
	plan, err := PlanFromExact(context.Background(), ExactPlanInput{
		AnalysisRoot: tmp,
		Entries: []ExactEntry{{
			Path: via, Signature: "development.rust.cargo-target",
			EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: FilterDisplayNone, EffectiveMaxAge: FilterDisplayNone,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("symlink ancestor must not plan: %+v", plan.Candidates)
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("expected warning")
	}
}
