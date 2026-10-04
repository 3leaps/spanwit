package cmd

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/3leaps/spanwit/internal/engine"
)

// executePrunePlan deletes every prunable candidate through the shared
// destructive guard. Each candidate must carry a non-inferred authorization
// boundary + provenance (config profile root or exact_plan analysis_root);
// missing/unknown provenance, a containment violation, or an identity change
// between authorization and deletion skips that candidate with a visible warning
// and makes the whole run return non-zero — a partial delete never reports as
// full success.
func executePrunePlan(ctx context.Context, plan *PrunePlan) error {
	if plan == nil {
		return fmt.Errorf("prune plan is required")
	}
	var failures int
	var prunable int
	for i := range plan.Candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		candidate := &plan.Candidates[i]
		// Only prunable candidates are eligible for deletion. Withheld matches
		// (e.g. signatures not marked safe_to_prune) must never enter the delete
		// loop, regardless of display state.
		if candidate.State != candidateStatePrunable {
			continue
		}
		prunable++
		tok, err := engine.AuthorizeDeletion(engine.DeletionRequest{
			Target:     candidate.Path,
			Boundary:   candidate.AuthorizationBoundary,
			Relation:   candidate.ContainmentRelation,
			Provenance: candidate.ProvenanceKind,
		})
		if err != nil {
			failures++
			plan.Warnings = append(plan.Warnings, fmt.Errorf("skipping %s: %w", candidate.Path, err))
			continue
		}

		loggerInstance.Info("deleting prune candidate",
			zap.String("path", candidate.Path),
			zap.Int64("size_bytes", candidate.Size),
			zap.String("signature", candidate.Signature),
			zap.String("pattern", candidate.Pattern),
			zap.String("authorization_boundary", candidate.AuthorizationBoundary),
			zap.String("provenance", candidate.ProvenanceKind))
		if err := engine.RemoveGuarded(tok); err != nil {
			failures++
			plan.Warnings = append(plan.Warnings, fmt.Errorf("delete %s: %w", candidate.Path, err))
			continue
		}
		candidate.Deleted = true
	}
	if failures > 0 {
		return fmt.Errorf("failed to delete %d of %d prune candidates", failures, prunable)
	}
	return nil
}
