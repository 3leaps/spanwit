package space

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/engine"
)

var ErrCapacityOnlyNotPruneCarrier = errors.New("capacity-only space report is not a prune carrier")

type reportEnvelope struct {
	Schema      string `json:"$schema"`
	Version     int    `json:"version"`
	CaptureMode string `json:"capture_mode"`
}

type v2CapacityCarrier struct {
	GeneratedAt        string              `json:"generated_at"`
	Root               string              `json:"root"`
	CaptureMode        string              `json:"capture_mode"`
	Pressure           Pressure            `json:"pressure"`
	CapacityAccounting capacity.Accounting `json:"capacity_accounting"`
}

// LoadSpaceReportJSON validates raw bytes against the embedded SpaceReport
// schema, then unmarshals and cross-checks prune_handoff.exact_plan invariants.
// Fail-closed: missing/unknown policy fields and schema failures are errors.
func LoadSpaceReportJSON(raw []byte) (Report, error) {
	if len(raw) == 0 {
		return Report{}, fmt.Errorf("space report is empty")
	}
	var envelope reportEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Report{}, fmt.Errorf("parse space report envelope: %w", err)
	}

	var schema []byte
	switch {
	case envelope.Schema == SchemaID && envelope.Version == 1 && envelope.CaptureMode == "":
		schema = spanwitschema.SpanwitSpaceReportV1
	case envelope.Schema == SchemaV2ID && envelope.Version == 2 &&
		(envelope.CaptureMode == CaptureModeFull || envelope.CaptureMode == CaptureModeCapacityOnly):
		schema = spanwitschema.SpanwitSpaceReportV2
	default:
		return Report{}, fmt.Errorf(
			"space report schema validation failed: unsupported or mismatched identity: $schema=%q version=%d capture_mode=%q",
			envelope.Schema, envelope.Version, envelope.CaptureMode,
		)
	}
	if err := contract.ValidateJSON(schema, raw); err != nil {
		return Report{}, fmt.Errorf("space report schema validation failed: %w", err)
	}
	if envelope.Version == 2 {
		var carrier v2CapacityCarrier
		if err := json.Unmarshal(raw, &carrier); err != nil {
			return Report{}, fmt.Errorf("parse space report v2 capacity carrier: %w", err)
		}
		if err := validateCapacityCarrier(carrier.GeneratedAt, carrier.Pressure, carrier.CapacityAccounting); err != nil {
			return Report{}, fmt.Errorf("space report capacity invariant: %w", err)
		}
		if carrier.CaptureMode == CaptureModeCapacityOnly {
			return Report{}, ErrCapacityOnlyNotPruneCarrier
		}
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return Report{}, fmt.Errorf("parse space report: %w", err)
	}
	if err := validateReportPolicy(report); err != nil {
		return Report{}, err
	}
	if err := validateHandoffInvariants(report); err != nil {
		return Report{}, err
	}
	if err := validateRecipesInvariants(report); err != nil {
		return Report{}, err
	}
	if err := validateAppliedFiltersInvariants(report); err != nil {
		return Report{}, err
	}
	return report, nil
}

// validateRecipesInvariants locks structural vs argv recipe shape and command
// projection parity (016A). Historical argv reports omit kind and steps.
func validateRecipesInvariants(report Report) error {
	for i, r := range report.Recipes {
		kind := r.Kind
		if kind == "" {
			kind = RecipeKindArgv
		}
		switch kind {
		case RecipeKindArgv:
			if len(r.Steps) > 0 {
				return fmt.Errorf("recipe[%d] id=%q: argv recipes must not include steps", i, r.ID)
			}
			if r.ExactPlan != nil {
				return fmt.Errorf("recipe[%d] id=%q: argv recipes must not embed exact_plan", i, r.ID)
			}
		case RecipeKindStructural:
			if r.Kind != RecipeKindStructural {
				return fmt.Errorf("recipe[%d] id=%q: structural recipes must set kind=structural explicitly", i, r.ID)
			}
			if len(r.Steps) == 0 {
				return fmt.Errorf("recipe[%d] id=%q: structural recipes require non-empty steps", i, r.ID)
			}
			for j, s := range r.Steps {
				if s.ID == "" || s.Title == "" || s.Kind == "" {
					return fmt.Errorf("recipe[%d] step[%d]: id, title, and kind are required", i, j)
				}
				switch s.Kind {
				case StepConfigSnippet:
					if s.ConfigSnippet == nil || s.ConfigSnippet.Label == "" || s.ConfigSnippet.Content == "" {
						return fmt.Errorf("recipe[%d] step[%d] id=%q: config_snippet requires config_snippet payload", i, j, s.ID)
					}
					if s.Command != nil {
						return fmt.Errorf("recipe[%d] step[%d] id=%q: config_snippet must not carry command", i, j, s.ID)
					}
				case StepCommand, StepPurgeHint, StepVerify:
					if s.Command == nil {
						return fmt.Errorf("recipe[%d] step[%d] id=%q: kind %q requires command", i, j, s.ID, s.Kind)
					}
					if s.ConfigSnippet != nil {
						return fmt.Errorf("recipe[%d] step[%d] id=%q: kind %q must not carry config_snippet", i, j, s.ID, s.Kind)
					}
				case StepObserve:
					if s.Command != nil || s.ConfigSnippet != nil {
						return fmt.Errorf("recipe[%d] step[%d] id=%q: observe must not carry command or config_snippet", i, j, s.ID)
					}
				default:
					return fmt.Errorf("recipe[%d] step[%d] id=%q: unknown step kind %q", i, j, s.ID, s.Kind)
				}
			}
			// suggested_commands must match ordered projection of step commands.
			proj := projectCommandsFromSteps(r.Steps)
			if len(proj) != len(r.SuggestedCommands) {
				return fmt.Errorf("recipe[%d] id=%q: suggested_commands length %d != command-bearing steps %d", i, r.ID, len(r.SuggestedCommands), len(proj))
			}
			for k := range proj {
				if proj[k].Program != r.SuggestedCommands[k].Program ||
					proj[k].Display != r.SuggestedCommands[k].Display ||
					len(proj[k].Args) != len(r.SuggestedCommands[k].Args) {
					return fmt.Errorf("recipe[%d] id=%q: suggested_commands[%d] drifts from steps projection", i, r.ID, k)
				}
				for ai := range proj[k].Args {
					if proj[k].Args[ai] != r.SuggestedCommands[k].Args[ai] {
						return fmt.Errorf("recipe[%d] id=%q: suggested_commands[%d] arg drift", i, r.ID, k)
					}
				}
			}
			if r.ExactPlan != nil {
				if err := ValidateStandaloneExactPlan(r.ExactPlan); err != nil {
					return fmt.Errorf("recipe[%d] id=%q: exact_plan: %w", i, r.ID, err)
				}
			}
		default:
			return fmt.Errorf("recipe[%d] id=%q: unknown kind %q", i, r.ID, r.Kind)
		}
	}
	return nil
}

// validateAppliedFiltersInvariants enforces that a carrier advertising
// applied_filters is an honest filtered view (listing, aggregates, handoff).
// Absent applied_filters retains legacy broad semantics.
//
// Checks (when applied_filters is present):
//  1. every serialized listing row/recipe matches the projection
//  2. excluded-class section aggregates are zero/empty
//  3. verified prunable totals agree with handoff (absent/non-present = zero)
//  4. display cardinality: len(displayed rows for class) == min(full_count, top)
//  5. displayed prunables are a path/signature/scope/parent prefix/subset of handoff
//  6. handoff / exact-plan candidates match class+domain projection
//  7. optional domain/ecosystem fields do not contradict signature/catalog_id
func validateAppliedFiltersInvariants(report Report) error {
	af := report.AppliedFilters
	if af == nil {
		return nil
	}
	if af.Classes == nil || af.Domains == nil {
		return fmt.Errorf("applied_filters requires non-null classes and domains arrays")
	}
	f, err := NormalizeInventoryFilters(af.Classes, af.Domains)
	if err != nil {
		return fmt.Errorf("applied_filters: %w", err)
	}
	if !f.Active() {
		return fmt.Errorf("applied_filters present but both classes and domains are empty")
	}

	// --- Listing surfaces must match the advertised projection ---
	for _, c := range report.Verified.Candidates {
		if !EntryMatchesFilters(c, f) {
			return fmt.Errorf("verified candidate %s does not match applied_filters", c.Path)
		}
		if err := validateEntryTaxonomyConsistency("verified_reclaimable", c); err != nil {
			return err
		}
	}
	for _, e := range report.Hotspots {
		if !EntryMatchesFilters(e, f) {
			return fmt.Errorf("hotspot %s does not match applied_filters", e.Path)
		}
		if err := validateEntryTaxonomyConsistency("hotspots", e); err != nil {
			return err
		}
	}
	for _, e := range report.Unverified.Entries {
		if !EntryMatchesFilters(e, f) {
			return fmt.Errorf("unverified entry %s does not match applied_filters", e.Path)
		}
		if err := validateEntryTaxonomyConsistency("unverified", e); err != nil {
			return err
		}
	}
	for _, e := range report.Unknown {
		if !EntryMatchesFilters(e, f) {
			return fmt.Errorf("unknown entry %s does not match applied_filters", e.Path)
		}
		if err := validateEntryTaxonomyConsistency("unknown", e); err != nil {
			return err
		}
	}
	for _, r := range report.Recipes {
		if !RecipeMatchesFilters(r, f) {
			return fmt.Errorf("recipe %s does not match applied_filters", r.ID)
		}
		if err := validateRecipeTaxonomyConsistency(r); err != nil {
			return err
		}
	}

	// --- Excluded-class aggregates must be zero/empty ---
	if classFilterExcludes(f, StatePrunable) {
		if report.Verified.PrunableCandidates != 0 || report.Verified.PrunableBytes != 0 {
			return fmt.Errorf("applied_filters exclude prunable but verified_reclaimable reports prunable_candidates=%d prunable_bytes=%d",
				report.Verified.PrunableCandidates, report.Verified.PrunableBytes)
		}
		if report.PruneHandoff != nil && report.PruneHandoff.Present {
			return fmt.Errorf("applied_filters exclude prunable but prune_handoff.present=true")
		}
		if report.PruneHandoff != nil && len(report.PruneHandoff.Candidates) > 0 {
			return fmt.Errorf("applied_filters exclude prunable but prune_handoff has candidates")
		}
	}
	if classFilterExcludes(f, StateWithheld) {
		if report.Verified.WithheldCandidates != 0 || report.Verified.WithheldBytes != 0 {
			return fmt.Errorf("applied_filters exclude withheld but verified_reclaimable reports withheld_candidates=%d withheld_bytes=%d",
				report.Verified.WithheldCandidates, report.Verified.WithheldBytes)
		}
	}
	if classFilterExcludes(f, StateUnverified) {
		if report.Unverified.Count != 0 || report.Unverified.TotalBytes != 0 || len(report.Unverified.Entries) > 0 {
			return fmt.Errorf("applied_filters exclude unverified but unverified section is non-empty")
		}
	}
	if classFilterExcludes(f, StateDiagnosticOnly) {
		if len(report.Hotspots) > 0 {
			return fmt.Errorf("applied_filters exclude diagnostic-only but hotspots are present")
		}
		if len(report.Recipes) > 0 {
			return fmt.Errorf("applied_filters exclude diagnostic-only but recipes are present")
		}
	}
	if classFilterExcludes(f, StateUnknown) {
		if len(report.Unknown) > 0 {
			return fmt.Errorf("applied_filters exclude unknown but unknown entries are present")
		}
	}

	// --- Prunable totals must agree with handoff (both full filtered closed set) ---
	handoffCount := 0
	var handoffBytes int64
	var handoffCands []Entry
	if report.PruneHandoff != nil && report.PruneHandoff.Present {
		handoffCount = report.PruneHandoff.PrunableCount
		handoffBytes = report.PruneHandoff.PrunableBytes
		handoffCands = report.PruneHandoff.Candidates
	}
	if report.Verified.PrunableCandidates != handoffCount {
		return fmt.Errorf("verified prunable_candidates=%d disagrees with prune_handoff count=%d under applied_filters",
			report.Verified.PrunableCandidates, handoffCount)
	}
	if report.Verified.PrunableBytes != handoffBytes {
		return fmt.Errorf("verified prunable_bytes=%d disagrees with prune_handoff bytes=%d under applied_filters",
			report.Verified.PrunableBytes, handoffBytes)
	}

	// --- Display cardinality (producer: min(full_count, top) per action section) ---
	if err := validateFilteredDisplayCardinality(report, handoffCands); err != nil {
		return err
	}

	// --- Handoff / exact-plan candidates match projection ---
	if report.PruneHandoff == nil {
		return nil
	}
	for _, c := range report.PruneHandoff.Candidates {
		if !EntryMatchesFilters(c, f) {
			return fmt.Errorf("handoff candidate %s does not match applied_filters", c.Path)
		}
		if err := validateEntryTaxonomyConsistency("prune_handoff.candidates", c); err != nil {
			return err
		}
	}
	if report.PruneHandoff.ExactPlan != nil {
		for _, ec := range report.PruneHandoff.ExactPlan.Candidates {
			// Exact candidates lack state; check domain against signature spine only.
			if len(f.Domains) == 0 {
				continue
			}
			spine := ec.Signature
			if spine == "" {
				return fmt.Errorf("exact_plan candidate %s missing signature under domain applied_filters", ec.Path)
			}
			ok := false
			for _, d := range f.Domains {
				if TaxonomyMatches(spine, d) {
					ok = true
					break
				}
			}
			if !ok {
				return fmt.Errorf("exact_plan candidate %s signature %q does not match applied domain filters", ec.Path, spine)
			}
		}
	}
	return nil
}

// validateFilteredDisplayCardinality enforces that displayed inventory rows match
// the producer contract min(full_count, top) for verified states and unverified.
// Displayed prunables must be the path/signature/scope/parent prefix of handoff.
func validateFilteredDisplayCardinality(report Report, handoffCands []Entry) error {
	top := report.Top
	if top <= 0 {
		// Schema requires top >= 1; treat non-positive as 1 for fail-closed checks.
		top = 1
	}

	var dispPrunable, dispWithheld []Entry
	for _, c := range report.Verified.Candidates {
		switch c.State {
		case StatePrunable:
			dispPrunable = append(dispPrunable, c)
		case StateWithheld:
			dispWithheld = append(dispWithheld, c)
		}
	}

	wantP := report.Verified.PrunableCandidates
	if wantP > top {
		wantP = top
	}
	if len(dispPrunable) != wantP {
		return fmt.Errorf("applied_filters display cardinality: prunable rows=%d want min(prunable_candidates=%d, top=%d)=%d",
			len(dispPrunable), report.Verified.PrunableCandidates, top, wantP)
	}

	wantW := report.Verified.WithheldCandidates
	if wantW > top {
		wantW = top
	}
	if len(dispWithheld) != wantW {
		return fmt.Errorf("applied_filters display cardinality: withheld rows=%d want min(withheld_candidates=%d, top=%d)=%d",
			len(dispWithheld), report.Verified.WithheldCandidates, top, wantW)
	}

	wantU := report.Unverified.Count
	if wantU > top {
		wantU = top
	}
	if len(report.Unverified.Entries) != wantU {
		return fmt.Errorf("applied_filters display cardinality: unverified rows=%d want min(count=%d, top=%d)=%d",
			len(report.Unverified.Entries), report.Unverified.Count, top, wantU)
	}

	// Displayed prunables must match the corresponding handoff prefix (producer order).
	if wantP > 0 {
		if len(handoffCands) < wantP {
			return fmt.Errorf("applied_filters display cardinality: handoff has %d candidates but need prefix of %d for display",
				len(handoffCands), wantP)
		}
		for i := 0; i < wantP; i++ {
			d := dispPrunable[i]
			h := handoffCands[i]
			if d.Path != h.Path {
				return fmt.Errorf("applied_filters: displayed prunable[%d] path %q is not handoff prefix path %q",
					i, d.Path, h.Path)
			}
			if d.Signature != h.Signature {
				return fmt.Errorf("applied_filters: displayed prunable %s signature %q disagrees with handoff %q",
					d.Path, d.Signature, h.Signature)
			}
			if NormalizeReclaimScope(d.ReclaimScope) != NormalizeReclaimScope(h.ReclaimScope) {
				return fmt.Errorf("applied_filters: displayed prunable %s reclaim_scope disagrees with handoff", d.Path)
			}
			if normalizeParent(d.ParentPath) != normalizeParent(h.ParentPath) {
				return fmt.Errorf("applied_filters: displayed prunable %s parent_path disagrees with handoff", d.Path)
			}
		}
	}
	return nil
}

// classFilterExcludes reports whether class projection is active and omits state.
// Empty class list means no class projection (nothing excluded by class).
func classFilterExcludes(f InventoryFilters, state string) bool {
	if len(f.Classes) == 0 {
		return false
	}
	for _, c := range f.Classes {
		if c == state {
			return false
		}
	}
	return true
}

// validateEntryTaxonomyConsistency rejects domain/ecosystem that contradict the
// signature or catalog_id spine (fail-closed when both are present).
func validateEntryTaxonomyConsistency(section string, e Entry) error {
	spine := EntryTaxonomySpine(e)
	if spine == "" {
		return nil
	}
	d, eco := DomainEcosystemFromSpine(spine)
	if e.Domain != "" && e.Domain != d {
		return fmt.Errorf("%s entry %s domain %q contradicts spine %q", section, e.Path, e.Domain, spine)
	}
	if e.Ecosystem != "" && e.Ecosystem != eco {
		return fmt.Errorf("%s entry %s ecosystem %q contradicts spine %q", section, e.Path, e.Ecosystem, spine)
	}
	return nil
}

func validateRecipeTaxonomyConsistency(r Recipe) error {
	spine := r.CatalogID
	if spine == "" {
		return nil
	}
	d, eco := DomainEcosystemFromSpine(spine)
	if r.Domain != "" && r.Domain != d {
		return fmt.Errorf("recipe %s domain %q contradicts catalog_id %q", r.ID, r.Domain, spine)
	}
	if r.Ecosystem != "" && r.Ecosystem != eco {
		return fmt.Errorf("recipe %s ecosystem %q contradicts catalog_id %q", r.ID, r.Ecosystem, spine)
	}
	return nil
}

func validateReportPolicy(report Report) error {
	if err := requireSortMode("report", report.SortMode); err != nil {
		return err
	}
	if err := requireReclaimScope("report", report.ReclaimScope); err != nil {
		return err
	}
	for _, c := range report.Verified.Candidates {
		if err := validateEntryScope("verified_reclaimable", c); err != nil {
			return err
		}
	}
	return nil
}

func validateHandoffInvariants(report Report) error {
	if report.PruneHandoff == nil {
		return nil // no handoff is valid (nothing to revalidate)
	}
	h := report.PruneHandoff
	if !h.Present {
		if h.ExactPlan != nil && len(h.ExactPlan.Candidates) > 0 {
			return fmt.Errorf("prune_handoff.present=false but exact_plan has candidates")
		}
		return nil
	}
	if h.ExactPlan == nil {
		return fmt.Errorf("prune_handoff.present=true requires exact_plan")
	}
	ep := h.ExactPlan
	if ep.Schema != ExactPlanSchemaID {
		return fmt.Errorf("exact_plan.$schema must be %q (got %q)", ExactPlanSchemaID, ep.Schema)
	}
	if ep.Version != 1 {
		return fmt.Errorf("exact_plan.version must be 1 (got %d)", ep.Version)
	}
	if err := requireSortMode("exact_plan", ep.SortMode); err != nil {
		return err
	}
	if err := requireReclaimScope("exact_plan", ep.ReclaimScope); err != nil {
		return err
	}
	if ep.SortMode != report.SortMode {
		return fmt.Errorf("exact_plan.sort_mode %q disagrees with report.sort_mode %q", ep.SortMode, report.SortMode)
	}
	if ep.ReclaimScope != report.ReclaimScope {
		return fmt.Errorf("exact_plan.reclaim_scope %q disagrees with report.reclaim_scope %q", ep.ReclaimScope, report.ReclaimScope)
	}
	if len(ep.Candidates) == 0 {
		return fmt.Errorf("prune_handoff.present=true requires non-empty exact_plan.candidates")
	}
	if h.PrunableCount != len(h.Candidates) || h.PrunableCount != len(ep.Candidates) {
		return fmt.Errorf("prune_handoff candidate counts disagree: prunable_count=%d candidates=%d exact_plan=%d",
			h.PrunableCount, len(h.Candidates), len(ep.Candidates))
	}

	handoffPaths := make(map[string]Entry, len(h.Candidates))
	for _, c := range h.Candidates {
		if c.State != StatePrunable {
			return fmt.Errorf("handoff candidate %s has state %q (must be prunable)", c.Path, c.State)
		}
		if err := validateEntryScope("prune_handoff.candidates", c); err != nil {
			return err
		}
		if NormalizeReclaimScope(c.ReclaimScope) != ep.ReclaimScope {
			return fmt.Errorf("handoff candidate %s reclaim_scope %q disagrees with exact_plan.reclaim_scope %q",
				c.Path, c.ReclaimScope, ep.ReclaimScope)
		}
		handoffPaths[c.Path] = c
	}
	for _, ec := range ep.Candidates {
		if ec.Path == "" {
			return fmt.Errorf("exact_plan candidate missing path")
		}
		if ec.Signature == "" && ec.Pattern == "" {
			return fmt.Errorf("exact_plan candidate %s missing signature and pattern", ec.Path)
		}
		if err := requireFilterField(ec.Path, "effective_min_size", ec.EffectiveMinSize); err != nil {
			return err
		}
		if err := requireFilterField(ec.Path, "effective_min_age", ec.EffectiveMinAge); err != nil {
			return err
		}
		if err := requireFilterField(ec.Path, "effective_max_age", ec.EffectiveMaxAge); err != nil {
			return err
		}
		if err := validateExactCandidateScope(ec, ep.ReclaimScope); err != nil {
			return err
		}
		hc, ok := handoffPaths[ec.Path]
		if !ok {
			return fmt.Errorf("exact_plan path %s not present in prune_handoff.candidates", ec.Path)
		}
		if NormalizeReclaimScope(hc.ReclaimScope) != NormalizeReclaimScope(ec.ReclaimScope) {
			return fmt.Errorf("path %s reclaim_scope mismatch: handoff=%q exact=%q",
				ec.Path, hc.ReclaimScope, ec.ReclaimScope)
		}
		if normalizeParent(hc.ParentPath) != normalizeParent(ec.ParentPath) {
			return fmt.Errorf("path %s parent_path mismatch: handoff=%q exact=%q",
				ec.Path, hc.ParentPath, ec.ParentPath)
		}
		delete(handoffPaths, ec.Path)
	}
	if len(handoffPaths) > 0 {
		for p := range handoffPaths {
			return fmt.Errorf("prune_handoff.candidates path %s missing from exact_plan", p)
		}
	}
	return nil
}

func validateEntryScope(section string, c Entry) error {
	scope := NormalizeReclaimScope(c.ReclaimScope)
	if c.ReclaimScope == "" {
		return fmt.Errorf("%s candidate %s missing required reclaim_scope", section, c.Path)
	}
	if scope != engine.ReclaimScopeWhole && scope != engine.ReclaimScopeIncremental {
		return fmt.Errorf("%s candidate %s invalid reclaim_scope %q", section, c.Path, c.ReclaimScope)
	}
	if scope == engine.ReclaimScopeIncremental {
		if c.ParentPath == "" {
			return fmt.Errorf("%s candidate %s reclaim_scope=incremental requires parent_path", section, c.Path)
		}
	} else if c.ParentPath != "" {
		return fmt.Errorf("%s candidate %s reclaim_scope=whole must not carry parent_path", section, c.Path)
	}
	return nil
}

func validateExactCandidateScope(ec ExactCandidate, planScope string) error {
	scope := NormalizeReclaimScope(ec.ReclaimScope)
	if ec.ReclaimScope == "" {
		return fmt.Errorf("exact_plan candidate %s missing required reclaim_scope", ec.Path)
	}
	if scope != planScope {
		return fmt.Errorf("exact_plan candidate %s reclaim_scope %q disagrees with plan reclaim_scope %q",
			ec.Path, ec.ReclaimScope, planScope)
	}
	if scope == engine.ReclaimScopeIncremental {
		if ec.ParentPath == "" {
			return fmt.Errorf("exact_plan candidate %s reclaim_scope=incremental requires parent_path", ec.Path)
		}
	} else if ec.ParentPath != "" {
		return fmt.Errorf("exact_plan candidate %s reclaim_scope=whole must not carry parent_path", ec.Path)
	}
	return nil
}

func requireSortMode(where, mode string) error {
	if mode != engine.SortIdle && mode != engine.SortSize {
		return fmt.Errorf("%s.sort_mode must be idle|size (got %q)", where, mode)
	}
	return nil
}

func requireReclaimScope(where, scope string) error {
	if scope != engine.ReclaimScopeWhole && scope != engine.ReclaimScopeIncremental {
		return fmt.Errorf("%s.reclaim_scope must be whole|incremental (got %q)", where, scope)
	}
	return nil
}

func requireFilterField(path, name, value string) error {
	if value == "" {
		return fmt.Errorf("exact_plan candidate %s missing required %s (fail-closed; no silent default)", path, name)
	}
	// "none" is a valid explicit value; empty is not.
	_ = engine.FilterDisplayNone
	return nil
}

func normalizeParent(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}
