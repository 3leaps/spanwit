package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/3leaps/spanwit/internal/config"
)

// PlanFromAllowlist revalidates bare absolute paths against the built-in
// signature catalog with no age/size provenance. Symlink path components are
// rejected. Sibling trees are never walked.
//
// Bare allowlist is a dry-run convenience surface. Execute with policy identity
// requires PlanFromExact (prune --from-space-report).
func PlanFromAllowlist(ctx context.Context, paths []string, opts ...PlanOptions) (PrunePlan, error) {
	var plan PrunePlan
	if len(paths) == 0 {
		return plan, fmt.Errorf("allowlist requires at least one path")
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
	// Allowlist is whole-path revalidation only; incremental expansion requires
	// config discovery or an exact-plan carrier.
	if merged.ReclaimScope != "" && merged.ReclaimScope != ReclaimScopeWhole {
		return plan, fmt.Errorf("allowlist does not support reclaim_scope=%s (use config discovery or --from-space-report)", merged.ReclaimScope)
	}

	signatures := config.MergeSignatureCatalogs(config.BuiltInSignatures(), nil)
	targets := builtInSignatureTargets(signatures)

	seen := make(map[string]bool)
	var jobs []CandidateJob
	for _, raw := range paths {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		if strings.TrimSpace(raw) == "" {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("empty allowlist path skipped"))
			continue
		}
		abs, err := CleanConfiguredPath(raw)
		if err != nil {
			return plan, fmt.Errorf("allowlist path %q: %w", raw, err)
		}
		key := candidatePathKey(abs)
		if seen[key] {
			continue
		}
		seen[key] = true

		if err := ValidatePathNoSymlinkComponents(abs); err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("allowlist path %s: %w", abs, err))
			continue
		}
		info, err := os.Lstat(abs)
		if err != nil {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("allowlist path %s: %w", abs, err))
			continue
		}
		if !info.IsDir() {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("allowlist path %s: not a directory", abs))
			continue
		}

		parent := filepath.Dir(abs)
		rel := filepath.ToSlash(filepath.Base(abs))
		target, signature, evidence, ok := matchingTarget(parent, abs, rel, targets, signatures)
		if !ok {
			plan.Warnings = append(plan.Warnings, fmt.Errorf(
				"allowlist path %s: no longer matches a reclaimable signature (not included)", abs))
			continue
		}

		state := CandidateStatePrunable
		withheldReason := ""
		confidence := "pattern"
		pattern := target.Pattern
		if target.Signature != "" {
			confidence = signature.Confidence
			pattern = strings.Join(signature.CandidatePatterns, ",")
			if !signature.SafeToPrune {
				state = CandidateStateWithheld
				withheldReason = WithheldReasonSafeToPrune
			}
		}

		jobs = append(jobs, CandidateJob{
			Path:             abs,
			State:            state,
			WithheldReason:   withheldReason,
			Pattern:          pattern,
			Signature:        target.Signature,
			Confidence:       confidence,
			Evidence:         evidence,
			MinSize:          0,
			EffectiveMinSize: FilterDisplayNone,
			EffectiveMinAge:  FilterDisplayNone,
			EffectiveMaxAge:  FilterDisplayNone,
			ReclaimScope:     ReclaimScopeWhole,
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
	plan.ReclaimScope = ReclaimScopeWhole
	return plan, nil
}

func builtInSignatureTargets(signatures config.SignatureCatalog) []config.Target {
	var ids []string
	for domain, ecosystems := range signatures {
		for ecosystem, sigs := range ecosystems {
			for name := range sigs {
				ids = append(ids, domain+"."+ecosystem+"."+name)
			}
		}
	}
	sort.Strings(ids)
	out := make([]config.Target, 0, len(ids))
	for _, id := range ids {
		out = append(out, config.Target{Signature: id})
	}
	return out
}
