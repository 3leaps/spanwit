package reportmodel

import "github.com/3leaps/spanwit/internal/planspec"

// ExactPlanSchemaID is the $schema for the embedded exact prune plan.
const ExactPlanSchemaID = "https://schemas.3leaps.dev/spanwit/exact-prune-plan/v1.json"

// ExactPlan is a policy-preserving revalidation plan for prune_handoff candidates.
// It carries signature + per-candidate filter provenance so prune --from-space-report
// revalidates the same identity the operator reviewed.
type ExactPlan struct {
	Schema       string `json:"$schema"`
	Version      int    `json:"version"`
	AnalysisRoot string `json:"analysis_root"`
	MinSize      string `json:"min_size"`
	// SortMode is the ranking policy used when this plan was produced (idle|size).
	SortMode string `json:"sort_mode"`
	// ReclaimScope is plan-wide (whole|incremental); one mode per plan.
	ReclaimScope string           `json:"reclaim_scope"`
	Candidates   []ExactCandidate `json:"candidates"`
	// Signatures holds additive (non-built-in) signature definitions required
	// to revalidate custom signature-backed candidates.
	Signatures planspec.SignatureCatalog `json:"signatures,omitempty"`
}

// ExactCandidate is one closed-set path with the policy that classified it.
type ExactCandidate struct {
	Path             string   `json:"path"`
	Signature        string   `json:"signature,omitempty"`
	Pattern          string   `json:"pattern,omitempty"`
	EffectiveMinSize string   `json:"effective_min_size"`
	EffectiveMinAge  string   `json:"effective_min_age"`
	EffectiveMaxAge  string   `json:"effective_max_age"`
	Evidence         []string `json:"evidence,omitempty"`
	SizeBytes        int64    `json:"size_bytes"`
	SizeHuman        string   `json:"size_human"`
	SizeIncomplete   bool     `json:"size_incomplete,omitempty"`
	// SizeBound restates SizeIncomplete as a byte bound; derived on marshal.
	SizeBound string `json:"size_bound,omitempty"`
	// ReclaimScope is always explicit (whole|incremental).
	ReclaimScope string `json:"reclaim_scope"`
	// ParentPath is required for incremental revalidation; empty for whole.
	ParentPath string `json:"parent_path,omitempty"`
	// LastActivityAt / activity fields are observations only; PlanFromExact remeasures.
	LastActivityAt     string `json:"last_activity_at,omitempty"`
	ActivityBasis      string `json:"activity_basis,omitempty"`
	ActivityIncomplete bool   `json:"activity_incomplete,omitempty"`
	RebuildExpectation string `json:"rebuild_expectation,omitempty"`
}

// PruneHandoff is a dry-run plan limited to already-classified prunable verified
// rows from this SpaceReport. Suggested revalidation uses the embedded ExactPlan
// via `spanwit prune --from-space-report <this-json>`. No --execute is suggested.
type PruneHandoff struct {
	Present        bool       `json:"present"`
	AnalysisRoot   string     `json:"analysis_root"`
	MinSize        string     `json:"min_size"`
	PrunableCount  int        `json:"prunable_count"`
	PrunableBytes  int64      `json:"prunable_bytes"`
	PrunableHuman  string     `json:"prunable_human"`
	SizeIncomplete bool       `json:"size_incomplete"`
	SizeBound      string     `json:"size_bound,omitempty"` // derived on marshal
	Candidates     []Entry    `json:"candidates"`
	ExactPlan      *ExactPlan `json:"exact_plan,omitempty"`
	// TempExactPlans holds one exact plan per admitted temp root, each with
	// that root as its analysis_root. prune re-admits each root at prune time.
	TempExactPlans    []ExactPlan        `json:"temp_exact_plans,omitempty"`
	SuggestedCommands []SuggestedCommand `json:"suggested_commands"`
	Notes             []string           `json:"notes"`
}

// NormalizeReclaimScope maps empty to whole (explicit carrier value).
func NormalizeReclaimScope(scope string) string {
	if scope == "" {
		return planspec.ReclaimScopeWhole
	}
	return scope
}
