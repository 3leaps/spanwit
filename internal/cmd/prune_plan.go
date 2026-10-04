package cmd

import (
	"context"

	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/engine"
)

// Re-export engine exact input for prune CLI helpers.

// Compatibility aliases so existing cmd tests and renderers keep working while
// discovery lives in internal/engine (shared by prune, scan, and space).

const (
	candidateStatePrunable      = engine.CandidateStatePrunable
	candidateStateWithheld      = engine.CandidateStateWithheld
	withheldReasonSafeToPrune   = engine.WithheldReasonSafeToPrune
	withheldReasonAge           = engine.WithheldReasonAge
	withheldReasonMinSize       = engine.WithheldReasonMinSize
	withheldReasonIndeterminate = engine.WithheldReasonIndeterminate
	filterDisplayNone           = engine.FilterDisplayNone
	filterDisplayMixed          = engine.FilterDisplayMixed
)

type PrunePlan = engine.PrunePlan
type PruneCandidate = engine.PruneCandidate
type EffectiveFilters = engine.EffectiveFilters
type candidateJob = engine.CandidateJob

func buildPrunePlan(ctx context.Context, cfg *config.Config, opts ...engine.PlanOptions) (PrunePlan, error) {
	return engine.BuildPrunePlan(ctx, cfg, opts...)
}

func buildPrunePlanFromAllowlist(ctx context.Context, paths []string, opts ...engine.PlanOptions) (PrunePlan, error) {
	return engine.PlanFromAllowlist(ctx, paths, opts...)
}

func buildPrunePlanFromExact(ctx context.Context, in engine.ExactPlanInput, opts ...engine.PlanOptions) (PrunePlan, error) {
	return engine.PlanFromExact(ctx, in, opts...)
}

func cleanConfiguredPath(path string) (string, error) {
	return engine.CleanConfiguredPath(path)
}

func dirSize(path string) (int64, error) {
	return engine.DirSize(path)
}

func parseSize(s string) (int64, error) {
	return engine.ParseSize(s)
}

func humanSize(b int64) string {
	return engine.HumanSize(b)
}

func dedupeCandidateJobs(jobs []candidateJob) (unique []candidateJob, dropped int) {
	return engine.DedupeCandidateJobs(jobs)
}
