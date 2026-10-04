package space

import (
	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/engine"
)

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
	Signatures config.SignatureCatalog `json:"signatures,omitempty"`
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
	Present           bool               `json:"present"`
	AnalysisRoot      string             `json:"analysis_root"`
	MinSize           string             `json:"min_size"`
	PrunableCount     int                `json:"prunable_count"`
	PrunableBytes     int64              `json:"prunable_bytes"`
	PrunableHuman     string             `json:"prunable_human"`
	SizeIncomplete    bool               `json:"size_incomplete"`
	Candidates        []Entry            `json:"candidates"`
	ExactPlan         *ExactPlan         `json:"exact_plan,omitempty"`
	SuggestedCommands []SuggestedCommand `json:"suggested_commands"`
	Notes             []string           `json:"notes"`
}

// HandoffInput supplies verified candidates plus additive signatures for exact plan.
type HandoffInput struct {
	Verified       VerifiedSection
	AnalysisRoot   string
	MinSizeDisplay string
	SortMode       string
	ReclaimScope   string
	AdditiveSigs   config.SignatureCatalog
	// ClassFilters / DomainFilters are optional inventory projection used when
	// suggesting a fresh `space` rescan so the command does not silently broaden.
	ClassFilters  []string
	DomainFilters []string
}

// NormalizeReclaimScope maps empty to whole (explicit carrier value).
func NormalizeReclaimScope(scope string) string {
	if scope == "" {
		return engine.ReclaimScopeWhole
	}
	return scope
}

// NormalizeSortMode maps empty to idle for space-originated carriers.
func NormalizeSortMode(mode string) string {
	if mode == "" {
		return engine.SortIdle
	}
	return mode
}

// BuildPruneHandoff extracts prunable-only entries and builds a policy-preserving ExactPlan.
func BuildPruneHandoff(in HandoffInput) PruneHandoff {
	minSizeDisplay := in.MinSizeDisplay
	if minSizeDisplay == "" {
		minSizeDisplay = "none"
	}
	sortMode := NormalizeSortMode(in.SortMode)
	planScope := NormalizeReclaimScope(in.ReclaimScope)
	out := PruneHandoff{
		AnalysisRoot: in.AnalysisRoot,
		MinSize:      minSizeDisplay,
		Candidates:   make([]Entry, 0),
		Notes: []string{
			"space never deletes; this prune_handoff block is the dry-run for the listed paths only.",
			"candidates are limited to state=prunable rows from this report (withheld and diagnostic-only excluded).",
			"exact_plan carries signature, filter, sort_mode, and reclaim_scope provenance for revalidation.",
			"revalidate: spanwit prune --from-space-report <space-report.json> (dry-run by default; inherits carrier sort_mode unless --sort is set).",
			"no --execute command is suggested here; add --execute only after the exact-plan dry-run matches this handoff.",
			"bare --allowlist is dry-run convenience only and does not preserve custom signatures or filters.",
		},
		SuggestedCommands: []SuggestedCommand{},
	}

	var bytes int64
	var incomplete bool
	exactCands := make([]ExactCandidate, 0)
	neededSigs := config.SignatureCatalog{}

	for _, e := range in.Verified.Candidates {
		if e.State != StatePrunable && e.State != engine.CandidateStatePrunable {
			continue
		}
		if e.State == StateDiagnosticOnly || e.State == StateUnverified || e.State == StateUnknown || e.State == StateWithheld {
			continue
		}
		cp := e
		cp.State = StatePrunable
		if cp.EffectiveMinSize == "" {
			cp.EffectiveMinSize = engine.FilterDisplayNone
		}
		if cp.EffectiveMinAge == "" {
			cp.EffectiveMinAge = engine.FilterDisplayNone
		}
		if cp.EffectiveMaxAge == "" {
			cp.EffectiveMaxAge = engine.FilterDisplayNone
		}
		cp.ReclaimScope = NormalizeReclaimScope(cp.ReclaimScope)
		if cp.ReclaimScope == engine.ReclaimScopeWhole {
			cp.ParentPath = ""
		}
		out.Candidates = append(out.Candidates, cp)
		bytes += e.SizeBytes
		if e.SizeIncomplete {
			incomplete = true
		}
		exactCands = append(exactCands, ExactCandidate{
			Path:               e.Path,
			Signature:          e.Signature,
			Pattern:            e.Pattern,
			EffectiveMinSize:   cp.EffectiveMinSize,
			EffectiveMinAge:    cp.EffectiveMinAge,
			EffectiveMaxAge:    cp.EffectiveMaxAge,
			Evidence:           e.Evidence,
			SizeBytes:          e.SizeBytes,
			SizeHuman:          e.SizeHuman,
			SizeIncomplete:     e.SizeIncomplete,
			ReclaimScope:       cp.ReclaimScope,
			ParentPath:         cp.ParentPath,
			LastActivityAt:     e.LastActivityAt,
			ActivityBasis:      e.ActivityBasis,
			ActivityIncomplete: e.ActivityIncomplete,
			RebuildExpectation: e.RebuildExpectation,
		})
		// Capture additive custom signatures used by this candidate.
		if e.Signature != "" && in.AdditiveSigs != nil {
			if _, builtin := config.BuiltInSignatures().Lookup(e.Signature); !builtin {
				if sig, ok := in.AdditiveSigs.Lookup(e.Signature); ok {
					putSignature(neededSigs, e.Signature, sig)
				}
			}
		}
	}

	out.PrunableCount = len(out.Candidates)
	out.PrunableBytes = bytes
	out.PrunableHuman = engine.HumanSize(bytes)
	out.SizeIncomplete = incomplete
	out.Present = out.PrunableCount > 0

	if !out.Present {
		out.Notes = append(out.Notes, "no prunable verified candidates under this analysis — prune handoff is empty")
		return out
	}

	out.ExactPlan = &ExactPlan{
		Schema:       ExactPlanSchemaID,
		Version:      1,
		AnalysisRoot: in.AnalysisRoot,
		MinSize:      minSizeDisplay,
		SortMode:     sortMode,
		ReclaimScope: planScope,
		Candidates:   exactCands,
		Signatures:   neededSigs,
	}

	// Primary revalidate path: from-space-report (policy-preserving).
	out.SuggestedCommands = append(out.SuggestedCommands,
		FormatCommand("spanwit", "prune", "--from-space-report", "<space-report.json>"),
	)
	// Secondary: re-run space for a fresh report, carrying the same inventory
	// projection and ranking policy so the suggestion does not silently broaden.
	spaceArgs := []string{"space", in.AnalysisRoot, "--format", "json"}
	if minSizeDisplay != "" && minSizeDisplay != "none" {
		spaceArgs = append(spaceArgs, "--min-size", minSizeDisplay)
	}
	if sortMode != "" {
		spaceArgs = append(spaceArgs, "--sort", sortMode)
	}
	if planScope != "" && planScope != engine.ReclaimScopeWhole {
		spaceArgs = append(spaceArgs, "--reclaim-scope", planScope)
	}
	for _, c := range in.ClassFilters {
		spaceArgs = append(spaceArgs, "--class", c)
	}
	for _, d := range in.DomainFilters {
		spaceArgs = append(spaceArgs, "--domain", d)
	}
	out.SuggestedCommands = append(out.SuggestedCommands, FormatCommand("spanwit", spaceArgs...))

	if incomplete {
		out.Notes = append(out.Notes,
			"some handoff sizes are depth-bounded lower bounds (not exact totals); revalidate with full-depth prune sizing before treating byte totals as reclaim volume — prefer a focused complete inventory when triage depth hid mass")
	}
	if len(in.ClassFilters) > 0 || len(in.DomainFilters) > 0 {
		out.Notes = append(out.Notes,
			"suggested fresh space command carries the same --class/--domain projection as this report")
	}
	return out
}

func putSignature(cat config.SignatureCatalog, id string, sig config.Signature) {
	parts := splitSigID(id)
	if len(parts) != 3 {
		return
	}
	if cat[parts[0]] == nil {
		cat[parts[0]] = map[string]map[string]config.Signature{}
	}
	if cat[parts[0]][parts[1]] == nil {
		cat[parts[0]][parts[1]] = map[string]config.Signature{}
	}
	cat[parts[0]][parts[1]][parts[2]] = sig
}

func splitSigID(id string) []string {
	out := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(id); i++ {
		if id[i] == '.' {
			out = append(out, id[start:i])
			start = i + 1
		}
	}
	out = append(out, id[start:])
	return out
}

// HandoffSuggestsExecute reports whether any suggested command includes --execute.
func HandoffSuggestsExecute(h PruneHandoff) bool {
	for _, c := range h.SuggestedCommands {
		for _, a := range c.Args {
			if a == "--execute" || a == "-e" {
				return true
			}
		}
		if containsToken(c.Display, "--execute") || containsToken(c.Display, "-e") {
			return true
		}
	}
	return false
}

func containsToken(s, tok string) bool {
	for i := 0; i+len(tok) <= len(s); i++ {
		if s[i:i+len(tok)] != tok {
			continue
		}
		beforeOK := i == 0 || s[i-1] == ' '
		after := i + len(tok)
		afterOK := after == len(s) || s[after] == ' '
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

// ExactPlanToEngine converts the handoff exact plan into an engine input.
func ExactPlanToEngine(p *ExactPlan) engine.ExactPlanInput {
	if p == nil {
		return engine.ExactPlanInput{}
	}
	entries := make([]engine.ExactEntry, 0, len(p.Candidates))
	for _, c := range p.Candidates {
		entries = append(entries, engine.ExactEntry{
			Path:             c.Path,
			Signature:        c.Signature,
			Pattern:          c.Pattern,
			EffectiveMinSize: c.EffectiveMinSize,
			EffectiveMinAge:  c.EffectiveMinAge,
			EffectiveMaxAge:  c.EffectiveMaxAge,
			Evidence:         c.Evidence,
			ReclaimScope:     c.ReclaimScope,
			ParentPath:       c.ParentPath,
		})
	}
	return engine.ExactPlanInput{
		Entries: entries,
		// Carry the deletion authorization boundary through to the engine; without
		// it PlanFromExact fails closed (no inferred boundary).
		AnalysisRoot: p.AnalysisRoot,
		Signatures:   p.Signatures,
	}
}
