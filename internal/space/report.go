package space

// Trust vocabulary for space (extends prune prunable/withheld).
// See docs/decisions/ADR-0004-diagnostic-only-state.md.
const (
	StatePrunable       = "prunable"
	StateWithheld       = "withheld"
	StateDiagnosticOnly = "diagnostic-only"
	StateUnverified     = "unverified"
	StateUnknown        = "unknown"
)

// WithheldReasonDomainDisabled marks a context-verified candidate that would be
// prunable but sits in a use domain not enabled by domains.enabled. It stays
// visible (discover-broad) but is not authorized for reclaim (authorize-narrow)
// and is excluded from the prune handoff.
const WithheldReasonDomainDisabled = "domain_disabled"

// PressureLevel labels free-space pressure on a volume.
const (
	PressureOK       = "ok"
	PressureWarn     = "warn"
	PressureCritical = "critical"
)

// Pressure role labels distinguish primary write capacity from the analysis root.
const (
	PressureRolePrimaryWrite = "primary_write"
	PressureRoleAnalysisRoot = "analysis_root"
)

// RebuildExpectation describes how quickly a hotspot may refill after manual clean.
const (
	RebuildHigh   = "high"   // e.g. go-build, active cargo targets
	RebuildMedium = "medium" // e.g. uv, cargo registry
	RebuildLow    = "low"    // e.g. rustup toolchains (slow refill, high install cost)
)

// SchemaID is the JSON Schema $id for SpaceReport v1.
const SchemaID = "https://schemas.3leaps.dev/spanwit/space-report/v1.json"

// DefaultMaxDepth bounds unverified name-shaped discovery under the analysis root.
const DefaultMaxDepth = 12

// Report is the machine-readable space diagnostic (CLI JSON + future API body).
type Report struct {
	Schema            string `json:"$schema"`
	Version           int    `json:"version"`
	GeneratedAt       string `json:"generated_at"`
	Root              string `json:"root"`
	MinSize           string `json:"min_size"`
	IncludeHomeCaches bool   `json:"include_home_caches"`
	Top               int    `json:"top"`
	MaxDepth          int    `json:"max_depth"`
	// SortMode is the ranking policy used for verified candidates (idle|size).
	// Required on every report so zero-candidate diagnostics stay self-describing.
	SortMode string `json:"sort_mode"`
	// ReclaimScope is the plan-wide reclaim granularity (whole|incremental).
	ReclaimScope     string            `json:"reclaim_scope"`
	Pressure         Pressure          `json:"pressure"`
	AnalysisPressure *Pressure         `json:"analysis_pressure,omitempty"`
	Verified         VerifiedSection   `json:"verified_reclaimable"`
	Hotspots         []Entry           `json:"hotspots"`
	Unverified       UnverifiedSection `json:"unverified"`
	Unknown          []Entry           `json:"unknown"`
	// PruneHandoff lists already-prunable verified rows from this report (the
	// dry-run for those paths) plus allowlist revalidate commands. Never
	// includes diagnostic-only mass.
	PruneHandoff *PruneHandoff `json:"prune_handoff,omitempty"`
	// Recipes are guided external-command suggestions and multi-step structural
	// journeys for diagnostic-only capacity help (e.g. rustup, cargo placement).
	// Never executable via spanwit.
	Recipes []Recipe `json:"recipes,omitempty"`
	// AppliedFilters is set when opt-in --class/--domain projection was used.
	// Distinguishes an honestly empty filtered view from unfiltered empty.
	AppliedFilters *AppliedFilters `json:"applied_filters,omitempty"`
	// EnabledDomains echoes the resolved domains.enabled policy set when the
	// config supplied a domains block. It records authorize-narrow: which
	// domains may drive recipes/reclaim. A pointer so an explicit empty enable
	// set marshals as [] (distinct from the built-in default, which is absent).
	EnabledDomains *[]string `json:"enabled_domains,omitempty"`
	// MutationContract is the effective invocation no-mutation assertion for
	// this run (open | read_only). Present on full and capacity-only reports.
	MutationContract string `json:"mutation_contract,omitempty"`
	// Completion states whether the requested observation finished without
	// coverage warnings. It mirrors the CLI exit contract (complete = 0,
	// partial = 1) and is never deletion authority.
	Completion *Completion `json:"completion,omitempty"`
	// TempPlaneCoverage names OS temp roots not walked by this run, present
	// only under warn/critical primary pressure. Observation only.
	TempPlaneCoverage *TempPlaneCoverage `json:"temp_plane_coverage,omitempty"`
	Warnings          []string           `json:"warnings,omitempty"`
	Notes             []string           `json:"notes,omitempty"`
}

// Pressure describes filesystem capacity for one measured path/volume.
type Pressure struct {
	Path        string  `json:"path"`
	Mount       string  `json:"mount,omitempty"`
	VolumeID    string  `json:"volume_id,omitempty"` // opaque filesystem identity for same-volume compare
	Role        string  `json:"role,omitempty"`
	TotalBytes  int64   `json:"total_bytes"`
	TotalHuman  string  `json:"total_human"`
	UsedBytes   int64   `json:"used_bytes"`
	UsedHuman   string  `json:"used_human"`
	AvailBytes  int64   `json:"avail_bytes"`
	AvailHuman  string  `json:"avail_human"`
	UsedPercent float64 `json:"used_percent"`
	Level       string  `json:"level"`
}

// VerifiedSection is engine-backed reclaimable / withheld discovery.
type VerifiedSection struct {
	Candidates         []Entry `json:"candidates"`
	PrunableCandidates int     `json:"prunable_candidates"`
	PrunableBytes      int64   `json:"prunable_bytes"`
	PrunableHuman      string  `json:"prunable_human"`
	WithheldCandidates int     `json:"withheld_candidates"`
	WithheldBytes      int64   `json:"withheld_bytes"`
	WithheldHuman      string  `json:"withheld_human"`
	// SizeIncomplete is true when any candidate size is a depth-bounded partial
	// measurement (section-level machine signal; the single incompleteness field).
	SizeIncomplete bool `json:"size_incomplete"`
	// PrunableSizeIncomplete / WithheldSizeIncomplete are human-presentation
	// helpers derived from the same size_incomplete rows, split by state so
	// subtotals are not both marked lower-bound when only one side is incomplete.
	// Not marshaled — avoids a second machine incompleteness truth source.
	PrunableSizeIncomplete bool `json:"-"`
	WithheldSizeIncomplete bool `json:"-"`
}

// UnverifiedSection is name-shaped but not context-verified mass.
type UnverifiedSection struct {
	Count      int     `json:"count"`
	TotalBytes int64   `json:"total_bytes"`
	TotalHuman string  `json:"total_human"`
	Entries    []Entry `json:"entries"`
	// SizeIncomplete is true when any full-set entry was depth-bounded;
	// totals are lower bounds even if incomplete rows are top-truncated out.
	SizeIncomplete bool `json:"size_incomplete"`
}

// Entry is one reported path with classification state.
type Entry struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SizeHuman string `json:"size_human"`
	// SizeIncomplete means SizeBytes is an observed lower bound (depth cutoff or
	// coverage omission), not an exact full-tree total. Wholly unmeasured
	// observation candidates have no Entry; they appear in coverage warnings.
	SizeIncomplete     bool     `json:"size_incomplete,omitempty"`
	State              string   `json:"state"`
	WithheldReason     string   `json:"withheld_reason,omitempty"`
	Signature          string   `json:"signature,omitempty"`
	Pattern            string   `json:"pattern,omitempty"`
	Label              string   `json:"label,omitempty"`
	RebuildExpectation string   `json:"rebuild_expectation,omitempty"`
	Evidence           []string `json:"evidence,omitempty"`
	// Effective* carry resolved filter provenance (defaults→path→target) for
	// prune_handoff exact-plan revalidation.
	EffectiveMinSize string `json:"effective_min_size,omitempty"`
	EffectiveMinAge  string `json:"effective_min_age,omitempty"`
	EffectiveMaxAge  string `json:"effective_max_age,omitempty"`
	// LastActivityAt is RFC3339 newest observed descendant mtime (omit if unknown).
	LastActivityAt string `json:"last_activity_at,omitempty"`
	// ActivityBasis is descendant_mtime or unknown.
	ActivityBasis string `json:"activity_basis,omitempty"`
	// ActivityIncomplete means activity may be understated (depth bound / symlink).
	ActivityIncomplete bool `json:"activity_incomplete,omitempty"`
	// ReclaimScope is whole or incremental for verified/handoff rows (required
	// there). Omitted on diagnostic sections (hotspot/unverified/unknown).
	ReclaimScope string `json:"reclaim_scope,omitempty"`
	// ParentPath is required when ReclaimScope is incremental; empty for whole.
	ParentPath string `json:"parent_path,omitempty"`
	// Domain / Ecosystem are derived from signature or catalog_id (taxonomy spine).
	// Optional; omit when no spine is available.
	Domain    string `json:"domain,omitempty"`
	Ecosystem string `json:"ecosystem,omitempty"`
	// CatalogID is a full-spine id for non-signature catalog rows
	// (e.g. development.go.go-build). Verified rows keep Signature instead.
	CatalogID string `json:"catalog_id,omitempty"`
	// RecipeIDs optionally links entries to guided recipes (stable ids).
	RecipeIDs []string `json:"recipe_ids,omitempty"`
}

// PartialUpdate is emitted as early crisis-triage phases complete so callers
// can present pressure/hotspots before deep root walks finish.
type PartialUpdate struct {
	Phase    string
	Pressure *Pressure
	Hotspots []Entry
	Recipes  []Recipe
	// WorkBudgetNote is set when critical pressure reduced deep-walk budget.
	WorkBudgetNote string
	// TempPlanes is set under warn/critical pressure (see Report).
	TempPlanes *TempPlaneCoverage
}

// CriticalDeepMaxDepth caps analysis-root discovery depth when primary pressure
// is critical (crisis work budget). Operators can still pass an explicit lower
// --max-depth; a higher explicit depth is capped with a report note.
const CriticalDeepMaxDepth = 6

// Completion lifecycles for a finished space report.
const (
	CompletionComplete = "complete"
	CompletionPartial  = "partial"
)

// Completion summarizes a finished report: partial iff it carries warnings
// (coverage gaps). The report is still usable when partial.
type Completion struct {
	Lifecycle    string `json:"lifecycle"`
	WarningCount int    `json:"warning_count"`
}

// NewCompletion derives the completion record from a report's warnings.
func NewCompletion(warnings []string) *Completion {
	c := &Completion{Lifecycle: CompletionComplete, WarningCount: len(warnings)}
	if len(warnings) > 0 {
		c.Lifecycle = CompletionPartial
	}
	return c
}
