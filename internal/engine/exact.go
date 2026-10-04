package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3leaps/spanwit/internal/config"
)

// ExactEntry is one path from a space prune_handoff with full policy provenance.
type ExactEntry struct {
	Path             string
	Signature        string // preferred: signature-backed identity
	Pattern          string // pattern-only targets
	EffectiveMinSize string
	EffectiveMinAge  string
	EffectiveMaxAge  string
	Evidence         []string
	// ReclaimScope is whole (default/empty) or incremental.
	ReclaimScope string
	// ParentPath is required when ReclaimScope is incremental (verified cargo target).
	ParentPath string
}

// ExactPlanInput revalidates a closed set of handoff candidates under the same
// signature catalog and per-candidate filters that produced the SpaceReport.
type ExactPlanInput struct {
	Entries []ExactEntry
	// AnalysisRoot is the carried exact_plan.analysis_root: the non-inferred
	// authorization boundary every candidate must stay contained under. A
	// non-empty plan missing it fails closed (provenance loss is not authority).
	AnalysisRoot string
	Signatures   config.SignatureCatalog // additive overrides (custom signatures)
}

// PlanFromExact revalidates each entry in isolation. It never walks sibling
// trees, never invents new candidates, and applies the carried min_size / age
// window. Symlink path components are rejected. Ranking fields are remeasured
// at plan time (stale activity snapshots are never deletion authorization).
func PlanFromExact(ctx context.Context, in ExactPlanInput, opts ...PlanOptions) (PrunePlan, error) {
	var plan PrunePlan
	if len(in.Entries) == 0 {
		return plan, fmt.Errorf("exact plan requires at least one candidate")
	}
	merged := MergePlanOptions(opts...)
	sizeMaxDepth := -1
	if merged.sizeMaxDepthSet {
		sizeMaxDepth = merged.SizeMaxDepth
	}
	sortMode := merged.SortMode
	if sortMode == "" {
		sortMode = SortSize
	}

	signatures := config.MergeSignatureCatalogs(config.BuiltInSignatures(), in.Signatures)
	// Authorization boundary: the carried analysis_root. Validate the RAW carrier
	// value as already absolute and clean — never expand/normalize it first, or a
	// tampered relative/~/.. value would acquire a clean-absolute meaning derived
	// from the process working directory or home. A missing/relative/unclean
	// analysis_root has no valid deletion boundary — fail closed.
	analysisRoot, err := requireCleanAbs(in.AnalysisRoot)
	if err != nil {
		return plan, fmt.Errorf("exact plan: analysis_root authorization boundary must be absolute and clean: %w", err)
	}
	seen := make(map[string]bool)
	var jobs []CandidateJob

	for _, e := range in.Entries {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		if strings.TrimSpace(e.Path) == "" {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("empty exact-plan path skipped"))
			continue
		}
		// Validate the RAW candidate path as already absolute and clean before any
		// use — carrier authority is never reinterpreted against cwd/home or
		// rewritten through "..".
		abs, err := requireCleanAbs(e.Path)
		if err != nil {
			return plan, fmt.Errorf("exact-plan path %q must be absolute and clean: %w", e.Path, err)
		}
		key := candidatePathKey(abs)
		if seen[key] {
			continue
		}
		seen[key] = true

		if err := ValidatePathNoSymlinkComponents(abs); err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("exact-plan path %s: %w", abs, err))
			continue
		}
		info, err := os.Lstat(abs)
		if err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("exact-plan path %s: %w", abs, err))
			continue
		}
		if !info.IsDir() {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("exact-plan path %s: not a directory", abs))
			continue
		}
		// Containment at plan time: a candidate outside the carried analysis_root
		// is a structural boundary violation (a tampered or foreign entry), not
		// benign drift. Fail the whole plan before anything deletes — never a
		// silent drop-and-continue that could still delete other rows.
		if err := checkContainment(analysisRoot, abs, ContainmentDescendant); err != nil {
			return plan, fmt.Errorf("exact-plan path %s: %w", abs, err)
		}

		scope := e.ReclaimScope
		if scope == "" {
			scope = ReclaimScopeWhole
		}

		var (
			target    config.Target
			signature config.Signature
			evidence  []string
			ok        bool
			parentAbs string
		)

		if scope == ReclaimScopeIncremental {
			if e.Signature != "" && e.Signature != "development.rust.cargo-target" {
				plan.Warnings = append(plan.Warnings, fmt.Errorf(
					"exact-plan path %s: incremental reclaim only supports development.rust.cargo-target (got %q; not included)",
					abs, e.Signature))
				continue
			}
			if strings.TrimSpace(e.ParentPath) == "" {
				plan.Warnings = append(plan.Warnings, fmt.Errorf(
					"exact-plan path %s: incremental reclaim requires parent_path (not included)", abs))
				continue
			}
			// Validate the RAW incremental parent as already absolute and clean;
			// a relative/unclean parent is a structural boundary violation.
			parentAbs, err = requireCleanAbs(e.ParentPath)
			if err != nil {
				return plan, fmt.Errorf("exact-plan parent %q must be absolute and clean: %w", e.ParentPath, err)
			}
			if err := ValidatePathNoSymlinkComponents(parentAbs); err != nil {
				plan.Warnings = append(plan.Warnings, fmt.Errorf("exact-plan parent %s: %w", parentAbs, err))
				continue
			}
			if err := ValidateIncrementalUnderParent(parentAbs, abs); err != nil {
				plan.Warnings = append(plan.Warnings, fmt.Errorf(
					"exact-plan path %s: invalid incremental under parent: %w (not included)", abs, err))
				continue
			}
			// Revalidate parent as cargo-target.
			sigID := e.Signature
			if sigID == "" {
				sigID = "development.rust.cargo-target"
			}
			sig, found := signatures.Lookup(sigID)
			if !found {
				plan.Warnings = append(plan.Warnings, fmt.Errorf(
					"exact-plan path %s: signature %q not in catalog (not included)", abs, sigID))
				continue
			}
			parentParent := filepath.Dir(parentAbs)
			parentRel := filepath.ToSlash(filepath.Base(parentAbs))
			target = config.Target{Signature: sigID}
			target, signature, evidence, ok = matchingTarget(parentParent, parentAbs, parentRel, []config.Target{target}, signatures)
			if !ok {
				_ = sig
				plan.Warnings = append(plan.Warnings, fmt.Errorf(
					"exact-plan path %s: parent no longer matches signature %q (not included)", abs, sigID))
				continue
			}
			evidence = append(append([]string{}, evidence...), "reclaim_scope:incremental", "parent:"+parentAbs)
		} else if e.Signature != "" {
			// Revalidate against the *same* signature identity only.
			parent := filepath.Dir(abs)
			rel := filepath.ToSlash(filepath.Base(abs))
			sig, found := signatures.Lookup(e.Signature)
			if !found {
				plan.Warnings = append(plan.Warnings, fmt.Errorf(
					"exact-plan path %s: signature %q not in catalog (not included)", abs, e.Signature))
				continue
			}
			target = config.Target{Signature: e.Signature}
			target, signature, evidence, ok = matchingTarget(parent, abs, rel, []config.Target{target}, signatures)
			if !ok {
				_ = sig
				plan.Warnings = append(plan.Warnings, fmt.Errorf(
					"exact-plan path %s: no longer matches signature %q (not included)", abs, e.Signature))
				continue
			}
		} else if e.Pattern != "" {
			parent := filepath.Dir(abs)
			rel := filepath.ToSlash(filepath.Base(abs))
			target = config.Target{Pattern: e.Pattern}
			target, signature, evidence, ok = matchingTarget(parent, abs, rel, []config.Target{target}, signatures)
			if !ok {
				plan.Warnings = append(plan.Warnings, fmt.Errorf(
					"exact-plan path %s: no longer matches pattern %q (not included)", abs, e.Pattern))
				continue
			}
		} else {
			plan.Warnings = append(plan.Warnings, fmt.Errorf(
				"exact-plan path %s: missing signature and pattern provenance (not included)", abs))
			continue
		}

		// Fail-closed: filters must be carried explicitly (no silent "none" default).
		if e.EffectiveMinSize == "" || e.EffectiveMinAge == "" || e.EffectiveMaxAge == "" {
			return plan, fmt.Errorf("exact-plan path %s: missing effective filter provenance (min_size/min_age/max_age required)", abs)
		}
		effMinSize := e.EffectiveMinSize
		effMinAge := e.EffectiveMinAge
		effMaxAge := e.EffectiveMaxAge
		minSize, err := ParseSize(displayFilterRaw(effMinSize))
		if err != nil {
			return plan, fmt.Errorf("exact-plan path %s: invalid min_size %q: %w", abs, effMinSize, err)
		}

		state := CandidateStatePrunable
		withheldReason := ""
		if !meetsAgeWindow(effMinAge, effMaxAge, info.ModTime(), time.Now()) {
			state = CandidateStateWithheld
			withheldReason = WithheldReasonAge
		}
		confidence := "pattern"
		pattern := target.Pattern
		if target.Signature != "" {
			confidence = signature.Confidence
			pattern = strings.Join(signature.CandidatePatterns, ",")
			if !signature.SafeToPrune && state != CandidateStateWithheld {
				state = CandidateStateWithheld
				withheldReason = WithheldReasonSafeToPrune
			}
		}
		if evidence == nil {
			evidence = []string{}
		}

		jobs = append(jobs, CandidateJob{
			Path:             abs,
			State:            state,
			WithheldReason:   withheldReason,
			Pattern:          pattern,
			Signature:        target.Signature,
			Confidence:       confidence,
			Evidence:         evidence,
			MinSize:          minSize,
			EffectiveMinSize: effMinSize,
			EffectiveMinAge:  effMinAge,
			EffectiveMaxAge:  effMaxAge,
			ReclaimScope:     scope,
			ParentPath:       parentAbs,
			// Deletion boundary is the carried analysis_root (non-inferred).
			AuthorizationBoundary: analysisRoot,
			ContainmentRelation:   ContainmentDescendant,
			ProvenanceKind:        ProvenanceExactPlanAnalysis,
		})
	}

	if len(jobs) == 0 {
		plan.EffectiveFilters = EffectiveFilters{
			MinSize: FilterDisplayNone,
			MinAge:  FilterDisplayNone,
			MaxAge:  FilterDisplayNone,
		}
		return plan, nil
	}

	jobs, collapsed := CollapseContainment(jobs)
	if collapsed > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Errorf(
			"containment: dropped %d nested candidate path(s); plan keeps ancestors only",
			collapsed,
		))
	}

	plan.EffectiveFilters = aggregateEffectiveFilters(jobs, config.Defaults{})
	candidates, warnings := sizeCandidateJobs(ctx, jobs, sizeMaxDepth)
	plan.Candidates = candidates
	plan.Warnings = append(plan.Warnings, warnings...)
	for _, c := range plan.Candidates {
		plan.TotalSize += c.Size
		if c.SizeIncomplete {
			plan.SizeIncomplete = true
		}
	}
	SortCandidates(plan.Candidates, sortMode)
	plan.SortMode = sortMode
	// Plan-wide scope from entries (one mode per exact plan).
	plan.ReclaimScope = ReclaimScopeWhole
	if len(jobs) > 0 {
		scope := jobs[0].ReclaimScope
		if scope == "" {
			scope = ReclaimScopeWhole
		}
		plan.ReclaimScope = scope
	}
	return plan, nil
}
