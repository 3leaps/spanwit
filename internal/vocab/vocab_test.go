package vocab_test

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/cmd"
	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/envelope"
	"github.com/3leaps/spanwit/internal/inventory"
	"github.com/3leaps/spanwit/internal/observe"
	"github.com/3leaps/spanwit/internal/space"
	"github.com/3leaps/spanwit/internal/vocab"
)

// reportSchemas are the schemas whose string enums the site map must cover.
var reportSchemas = map[string][]byte{
	"space-report.v1":   spanwitschema.SpanwitSpaceReportV1,
	"space-report.v2":   spanwitschema.SpanwitSpaceReportV2,
	"space-compare.v1":  spanwitschema.SpanwitSpaceCompareV1,
	"prune-plan.v1":     spanwitschema.SpanwitPrunePlanV1,
	"observe-state.v1":  spanwitschema.SpanwitObserveStateV1,
	"observe-health.v1": spanwitschema.SpanwitObserveHealthV1,
}

func sitemap(t *testing.T) map[string]string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(spanwitschema.VocabularySitemap, &m); err != nil {
		t.Fatalf("parse site map: %v", err)
	}
	return m
}

// stringEnumSites walks a schema and returns JSON-pointer-ish paths of every
// enum whose values are all strings, matching the site map key format.
func stringEnumSites(node any, path string, out map[string][]string) {
	switch n := node.(type) {
	case map[string]any:
		if e, ok := n["enum"].([]any); ok && len(e) > 0 {
			vals := make([]string, 0, len(e))
			allStrings := true
			for _, v := range e {
				s, ok := v.(string)
				if !ok {
					allStrings = false
					break
				}
				vals = append(vals, s)
			}
			if allStrings {
				out[path] = vals
			}
		}
		for k, v := range n {
			stringEnumSites(v, path+"/"+k, out)
		}
	case []any:
		for i, v := range n {
			stringEnumSites(v, fmt.Sprintf("%s/%d", path, i), out)
		}
	}
}

func allSites(t *testing.T) map[string][]string {
	t.Helper()
	sites := map[string][]string{}
	for short, raw := range reportSchemas {
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", short, err)
		}
		found := map[string][]string{}
		stringEnumSites(doc, "", found)
		for p, vals := range found {
			sites[short+"#"+p] = vals
		}
	}
	return sites
}

// Every inline string enum in the report schemas is mapped by site to a
// registry vocabulary and is equal to it or a subset of it. Mapping is by
// location, never by value set, so equal sets in different roles stay apart.
func TestSitemapCoversEverySchemaEnum(t *testing.T) {
	sites := allSites(t)
	m := sitemap(t)
	for site, vals := range sites {
		name, ok := m[site]
		if !ok {
			t.Errorf("unmapped enum site %s %v: add it to spanwit-vocabulary-sitemap.v1.json", site, vals)
			continue
		}
		reg, err := vocab.Values(name)
		if err != nil {
			t.Errorf("%s maps to %v", site, err)
			continue
		}
		for _, v := range vals {
			if !contains(reg, v) {
				t.Errorf("%s: value %q is not in vocabulary %s %v", site, v, name, reg)
			}
		}
	}
	for site := range m {
		if _, ok := sites[site]; !ok {
			t.Errorf("site map entry %s matches no enum in the schemas (stale)", site)
		}
	}
}

// A site present in both SpaceReport v1 and v2 maps to the same vocabulary.
func TestSitemapV1V2Parity(t *testing.T) {
	m := sitemap(t)
	for site, name := range m {
		rest, ok := strings.CutPrefix(site, "space-report.v1#")
		if !ok {
			continue
		}
		if v2, ok := m["space-report.v2#"+rest]; ok && v2 != name {
			t.Errorf("%s maps to %s in v1 but %s in v2", rest, name, v2)
		}
	}
}

// Every registry vocabulary is used by at least one site, has no duplicates,
// and carries no x- extension values.
func TestRegistryHygiene(t *testing.T) {
	names, err := vocab.Names()
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	for _, n := range sitemap(t) {
		used[n] = true
	}
	for _, n := range names {
		if !used[n] {
			t.Errorf("vocabulary %s has no schema site", n)
		}
		vals, _ := vocab.Values(n)
		seen := map[string]bool{}
		for _, v := range vals {
			if seen[v] {
				t.Errorf("vocabulary %s repeats %q", n, v)
			}
			seen[v] = true
			if strings.HasPrefix(v, "x-") {
				t.Errorf("vocabulary %s carries extension value %q", n, v)
			}
		}
	}
}

// Go constant sets equal their registry vocabulary, both ways. Where a
// vocabulary is a union (byte_status), the Go set is a declared subset.
func TestGoConstantsMatchRegistry(t *testing.T) {
	cases := []struct {
		vocab  string
		subset bool
		values []string
	}{
		{"trust_state", false, []string{space.StatePrunable, space.StateWithheld, space.StateDiagnosticOnly, space.StateUnverified, space.StateUnknown}},
		{"pressure_level", false, []string{space.PressureOK, space.PressureWarn, space.PressureCritical}},
		{"pressure_level", false, []string{observe.LevelOK, observe.LevelWarn, observe.LevelCritical}},
		{"pressure_role", false, []string{space.PressureRolePrimaryWrite, space.PressureRoleAnalysisRoot}},
		{"rebuild_expectation", false, []string{space.RebuildHigh, space.RebuildMedium, space.RebuildLow}},
		{"rebuild_expectation", false, []string{engine.RebuildHigh, engine.RebuildMedium, engine.RebuildLow}},
		{"activity_basis", false, []string{engine.ActivityBasisDescendantMtime, engine.ActivityBasisUnknown}},
		{"sort_mode", false, []string{engine.SortIdle, engine.SortSize}},
		{"reclaim_scope", false, []string{engine.ReclaimScopeWhole, engine.ReclaimScopeIncremental}},
		{"mutation_contract", false, []string{inventory.MutationContractOpen, inventory.MutationContractReadOnly}},
		{"capture_mode", false, []string{space.CaptureModeFull, space.CaptureModeCapacityOnly}},
		{"temp_plane_kind", false, []string{space.TempPlaneKindShared, space.TempPlaneKindUser}},
		{"byte_status", true, []string{space.TempPlaneSizeMeasured, space.TempPlaneSizePartial, space.TempPlaneSizeUnavailable}},
		{"byte_status", true, []string{capacity.StatusMeasured, capacity.StatusPartial, capacity.StatusUnavailable, capacity.StatusUnsupported, capacity.StatusNotRequested}},
		{"byte_status", true, []string{space.ProbeMeasured, space.ProbeUnavailable, space.ProbeNotProbed}},
		{"byte_bound", true, []string{space.BoundExact, space.BoundLower, space.BoundUpper}},
		{"engine", false, []string{space.EngineColima, space.EngineDockerDesktop, space.EngineOrbStack, space.EnginePodman, space.EngineNative, space.EngineUnknown, space.EngineNone}},
		{"engine_detection", false, []string{space.DetectionIdentified, space.DetectionUnidentified, space.DetectionUnavailable}},
		{"engine_detection_basis", false, []string{space.DetectionBasisEndpointPath, space.DetectionBasisEngineInfo}},
		{"guest_basis", false, []string{space.GuestBasisGuestStatfs, space.GuestBasisHostStatfs, space.GuestBasisNone}},
		{"ref_disclosure", false, []string{space.RefDisclosureAggregate, space.RefDisclosureLocal}},
		{"runtime_bucket", false, []string{space.BucketImages, space.BucketBuildCache, space.BucketDanglingImages, space.BucketForeignPlatform, space.BucketSupersededTags, space.BucketVolumesUnreferenced}},
		{"cost_class", false, []string{space.CostNone, space.CostRebuild, space.CostRePull, space.CostDataLossRisk}},
		{"runtime_step", false, []string{space.StepDanglingImagePrune, space.StepBuildCachePrune, space.StepForeignPlatformRemove, space.StepSupersededTagRemove, space.StepImagePruneAll, space.StepVolumePrune, space.StepGuestTrim, space.StepDiskGrow}},
		{"runtime_reason", false, []string{space.ReasonEndpointUnresolved, space.ReasonEngineUnreachable, space.ReasonEngineDisagreement, space.ReasonProbeFailed, space.ReasonTimeout, space.ReasonParseFailed, space.ReasonUnsupportedEngine, space.ReasonProbeNotRequested}},
		{"engine_arch", false, []string{space.ArchAMD64, space.ArchARM64, space.ArchOther}},
		{"temp_family", false, []string{space.TempFamilyBunx, space.TempFamilyCodex, space.TempFamilyOpencode, space.TempFamilyTsx, space.TempFamilyOther}},
		{"completion_lifecycle", false, []string{space.CompletionComplete, space.CompletionPartial}},
		{"recipe_kind", false, []string{space.RecipeKindArgv, space.RecipeKindStructural}},
		{"recipe_step_kind", false, []string{space.StepCommand, space.StepConfigSnippet, space.StepObserve, space.StepPurgeHint, space.StepVerify}},
		{"size_basis", false, []string{inventory.SizeApparent}},
		{"withheld_reason", false, []string{engine.WithheldReasonAge, engine.WithheldReasonDomainDisabled, engine.WithheldReasonIndeterminate, engine.WithheldReasonMinSize, engine.WithheldReasonSafeToPrune}},
		{"coverage_gap_code", false, []string{capacity.GapPermissionDenied, capacity.GapCommandMissing, capacity.GapCommandFailed, capacity.GapParseFailed, capacity.GapTimeout, capacity.GapTruncated}},
		{"byte_bound", false, []string{capacity.BoundExact, capacity.BoundLower, capacity.BoundUpper, capacity.BoundUnknown}},
		{"privilege", false, []string{capacity.PrivilegeUnprivileged, capacity.PrivilegeElevated}},
		{"held_open_disclosure", false, []string{capacity.DisclosureNone, capacity.DisclosureFull}},
		{"held_open_root_class", false, []string{capacity.RootClassTargetTree, capacity.RootClassUserHome, capacity.RootClassSystemTemp, capacity.RootClassSystem, capacity.RootClassOther}},
		{"system_remediation", false, []string{capacity.RemediationNoneSupported, capacity.RemediationOSOrUpdateMechanism, capacity.RemediationUnknown}},
		{"delta_status", false, []string{capacity.DeltaExact, capacity.DeltaIndeterminate, capacity.DeltaIncomparable}},
		{"compare_compatibility", false, []string{capacity.CompatCompatible, capacity.CompatIncompatible, capacity.CompatPartial}},
		{"compare_plane", false, []string{capacity.PlaneFilesystem, capacity.PlaneContainer, capacity.PlaneVolumes, capacity.PlaneSnapshots, capacity.PlaneSystemManaged, capacity.PlaneHeldOpen, capacity.PlaneContainerRuntime}},
		{"compare_check", false, []string{capacity.CheckPlatform, capacity.CheckVolumeID, capacity.CheckContainerID, capacity.CheckMeasurementSemantics, capacity.CheckCaptureMode, capacity.CheckBootID, capacity.CheckReportVersion}},
		{"observe_coverage", false, []string{observe.CoverageComplete, observe.CoveragePartial, observe.CoverageDegraded, observe.CoverageIncomparable, observe.CoverageIndeterminate, observe.CoverageUnavailable}},
		{"observe_loss", false, []string{observe.LossAliasPathChange, observe.LossCorruptState, observe.LossDroppedWork, observe.LossIdentityShift, observe.LossIncompleteSample, observe.LossOverflow, observe.LossSampleError, observe.LossSequenceGap, observe.LossWatcherError}},
		{"observe_sample_source", false, []string{observe.SampleSourceFilesystem, observe.SampleSourceIdentityMismatch, observe.SampleSourceError}},
		{"proposal_kind", false, []string{observe.ProposalKindNote, observe.ProposalKindRecipe}},
		{"prune_mode", false, []string{cmd.PruneModeDryRun, cmd.PruneModeExecute}},
		{"disclosure_paths", false, []string{envelope.PathsNone, envelope.PathsAnalysis, envelope.PathsSystem, envelope.PathsFull}},
		{"disclosure_image_refs", false, []string{envelope.ImageRefsAggregate, envelope.ImageRefsLocal}},
		{"completion_lifecycle", false, []string{envelope.CompletionComplete, envelope.CompletionPartial}},
		{"temp_root_reason", false, []string{engine.TempRootNotOwned, engine.TempRootSharedPath, engine.TempRootGroupOtherWritable, engine.TempRootSticky, engine.TempRootSymlink, engine.TempRootNotDirectory, engine.TempRootOverlapsAnalysis, engine.TempRootUnsupported, engine.TempRootNotRequested, engine.TempRootMode, engine.TempRootEuidRoot}},
	}
	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.vocab] = true
		reg, err := vocab.Values(c.vocab)
		if err != nil {
			t.Fatal(err)
		}
		goSet := sortedUnique(c.values)
		if len(goSet) != len(c.values) {
			t.Errorf("%s: Go constants repeat a value: %v", c.vocab, c.values)
		}
		for _, v := range goSet {
			if !contains(reg, v) {
				t.Errorf("%s: Go constant %q is not in the registry %v", c.vocab, v, reg)
			}
		}
		if !c.subset {
			for _, v := range reg {
				if !contains(goSet, v) {
					t.Errorf("%s: registry value %q has no Go constant (Go set %v)", c.vocab, v, goSet)
				}
			}
		}
	}

	// Every vocabulary is either checked above or listed in noGoConstants with
	// a reason, so a new vocabulary cannot escape both.
	names, err := vocab.Names()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		_, exempt := noGoConstants[n]
		switch {
		case covered[n] && exempt:
			t.Errorf("vocabulary %s is both checked and exempt", n)
		case !covered[n] && !exempt:
			t.Errorf("vocabulary %s has no Go equality check and no noGoConstants reason", n)
		}
	}
	for n := range noGoConstants {
		if !contains(names, n) {
			t.Errorf("noGoConstants names unknown vocabulary %s", n)
		}
	}
}

// noGoConstants lists vocabularies whose values are not emitted from Go
// constants, with the reason. Each entry is a reviewed exception.
var noGoConstants = map[string]string{
	"session_boundary": "operator CLI flag value, validated in the space command and copied verbatim",
	"confidence":       "values come from signature configuration; engine.ConfidencePattern is the only Go-emitted value",
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func sortedUnique(xs []string) []string {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	out := make([]string, 0, len(m))
	for x := range m {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

// Each vocabulary equals the union of its mapped schema sites: a registry
// value that no schema enum uses would let an extra value land quietly.
func TestRegistryEqualsUnionOfSites(t *testing.T) {
	sites := allSites(t)
	union := map[string]map[string]bool{}
	for site, name := range sitemap(t) {
		if union[name] == nil {
			union[name] = map[string]bool{}
		}
		for _, v := range sites[site] {
			union[name][v] = true
		}
	}
	names, err := vocab.Names()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		vals, _ := vocab.Values(n)
		for _, v := range vals {
			if !union[n][v] {
				t.Errorf("vocabulary %s value %q appears at no schema site", n, v)
			}
		}
	}
}
