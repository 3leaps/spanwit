package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/engine"
)

const prunePlanSchemaID = "https://schemas.3leaps.dev/spanwit/prune-plan/v1.json"

type prunePlanOutput struct {
	Schema           string                 `json:"$schema"`
	Version          int                    `json:"version"`
	ConfigPath       string                 `json:"config_path"`
	Mode             string                 `json:"mode"`
	GeneratedAt      string                 `json:"generated_at"`
	SortMode         string                 `json:"sort_mode"`
	ReclaimScope     string                 `json:"reclaim_scope"`
	EffectiveFilters pruneEffectiveFilters  `json:"effective_filters"`
	Summary          prunePlanSummary       `json:"summary"`
	Execution        *pruneExecutionSummary `json:"execution,omitempty"`
	Candidates       []pruneCandidateOutput `json:"candidates"`
	Warnings         []string               `json:"warnings,omitempty"`
}

// pruneEffectiveFilters is the operator-visible policy banner for size/age
// gates. Plan-level values are a truthful aggregate over candidates ("mixed"
// when resolutions differ). Candidate rows carry their own resolved values.
// min_age = older-than floor; max_age = younger-than ceiling.
type pruneEffectiveFilters struct {
	MinSize string `json:"min_size"`
	MinAge  string `json:"min_age"`
	MaxAge  string `json:"max_age"`
}

// prunePlanSummary reports discovered totals alongside an explicit
// prunable/withheld split. Candidates/TotalBytes/TotalHuman count every
// discovered match (prunable + withheld); the reclaimable amount is
// PrunableBytes. This avoids labelling withheld bytes as "reclaimable".
type prunePlanSummary struct {
	Candidates         int    `json:"candidates"`
	TotalBytes         int64  `json:"total_bytes"`
	TotalHuman         string `json:"total_human"`
	PrunableCandidates int    `json:"prunable_candidates"`
	PrunableBytes      int64  `json:"prunable_bytes"`
	PrunableHuman      string `json:"prunable_human"`
	WithheldCandidates int    `json:"withheld_candidates"`
	WithheldBytes      int64  `json:"withheld_bytes"`
	WithheldHuman      string `json:"withheld_human"`
	Warnings           int    `json:"warnings"`
}

type pruneCandidateOutput struct {
	Path               string   `json:"path"`
	SizeBytes          int64    `json:"size_bytes"`
	SizeHuman          string   `json:"size_human"`
	State              string   `json:"state"`
	WithheldReason     string   `json:"withheld_reason,omitempty"`
	EffectiveMinSize   string   `json:"effective_min_size"`
	EffectiveMinAge    string   `json:"effective_min_age"`
	EffectiveMaxAge    string   `json:"effective_max_age"`
	Pattern            string   `json:"pattern,omitempty"`
	Signature          string   `json:"signature,omitempty"`
	Confidence         string   `json:"confidence"`
	Evidence           []string `json:"evidence,omitempty"`
	LastActivityAt     string   `json:"last_activity_at,omitempty"`
	ActivityBasis      string   `json:"activity_basis,omitempty"`
	ActivityIncomplete bool     `json:"activity_incomplete,omitempty"`
	RebuildExpectation string   `json:"rebuild_expectation,omitempty"`
	ReclaimScope       string   `json:"reclaim_scope"`
	ParentPath         string   `json:"parent_path,omitempty"`
	Deleted            *bool    `json:"deleted,omitempty"`
}

type pruneExecutionSummary struct {
	DeletedCandidates int    `json:"deleted_candidates"`
	DeletedBytes      int64  `json:"deleted_bytes"`
	DeletedHuman      string `json:"deleted_human"`
}

func newPrunePlanOutput(plan PrunePlan, loadedPath string, execute bool, now time.Time) prunePlanOutput {
	mode := "dry-run"
	if execute {
		mode = "execute"
	}

	sortMode := plan.SortMode
	if sortMode == "" {
		sortMode = "size"
	}
	reclaimScope := plan.ReclaimScope
	if reclaimScope == "" {
		reclaimScope = "whole"
	}
	// Effective filters must always be non-empty strings for schema validity
	// (including zero-candidate empty plans from space-report replay).
	effMinSize := displayFilter(plan.EffectiveFilters.MinSize)
	effMinAge := displayFilter(plan.EffectiveFilters.MinAge)
	effMaxAge := displayFilter(plan.EffectiveFilters.MaxAge)
	out := prunePlanOutput{
		Schema:       prunePlanSchemaID,
		Version:      1,
		ConfigPath:   loadedPath,
		Mode:         mode,
		GeneratedAt:  now.UTC().Format(time.RFC3339),
		SortMode:     sortMode,
		ReclaimScope: reclaimScope,
		EffectiveFilters: pruneEffectiveFilters{
			MinSize: effMinSize,
			MinAge:  effMinAge,
			MaxAge:  effMaxAge,
		},
		Summary: prunePlanSummary{
			Candidates: len(plan.Candidates),
			TotalBytes: plan.TotalSize,
			TotalHuman: humanSize(plan.TotalSize),
			Warnings:   len(plan.Warnings),
		},
		Candidates: make([]pruneCandidateOutput, 0, len(plan.Candidates)),
		Warnings:   make([]string, 0, len(plan.Warnings)),
	}
	var deletedCandidates int
	var deletedBytes int64
	for _, candidate := range plan.Candidates {
		var deleted *bool
		if execute {
			deleted = boolPtr(candidate.Deleted)
		}
		if candidate.Deleted {
			deletedCandidates++
			deletedBytes += candidate.Size
		}
		if candidate.State == candidateStateWithheld {
			out.Summary.WithheldCandidates++
			out.Summary.WithheldBytes += candidate.Size
		} else {
			out.Summary.PrunableCandidates++
			out.Summary.PrunableBytes += candidate.Size
		}
		po := pruneCandidateOutput{
			Path:               candidate.Path,
			SizeBytes:          candidate.Size,
			SizeHuman:          humanSize(candidate.Size),
			State:              candidate.State,
			WithheldReason:     candidate.WithheldReason,
			EffectiveMinSize:   displayFilter(candidate.EffectiveMinSize),
			EffectiveMinAge:    displayFilter(candidate.EffectiveMinAge),
			EffectiveMaxAge:    displayFilter(candidate.EffectiveMaxAge),
			Pattern:            candidate.Pattern,
			Signature:          candidate.Signature,
			Confidence:         candidate.Confidence,
			Evidence:           candidate.Evidence,
			ActivityBasis:      candidate.ActivityBasis,
			ActivityIncomplete: candidate.ActivityIncomplete,
			RebuildExpectation: candidate.RebuildExpectation,
			ReclaimScope:       candidate.ReclaimScope,
			ParentPath:         candidate.ParentPath,
			Deleted:            deleted,
		}
		if po.ReclaimScope == "" {
			po.ReclaimScope = "whole"
		}
		if po.ReclaimScope == "whole" {
			po.ParentPath = ""
		}
		if !candidate.LastActivityAt.IsZero() {
			po.LastActivityAt = candidate.LastActivityAt.UTC().Format(time.RFC3339)
		}
		out.Candidates = append(out.Candidates, po)
	}
	out.Summary.PrunableHuman = humanSize(out.Summary.PrunableBytes)
	out.Summary.WithheldHuman = humanSize(out.Summary.WithheldBytes)
	if execute {
		out.Execution = &pruneExecutionSummary{
			DeletedCandidates: deletedCandidates,
			DeletedBytes:      deletedBytes,
			DeletedHuman:      humanSize(deletedBytes),
		}
	}
	for _, warning := range plan.Warnings {
		out.Warnings = append(out.Warnings, warning.Error())
	}
	return out
}

// writePlanText renders a prune plan's candidates as read-only text: a "Prune
// candidates" table for prunable matches and a "Withheld" table for
// matched-but-not-eligible ones (filter or safety). It is the shared read-only
// view used by `prune` (dry-run listing + execute preamble) and by `scan` (the
// read-only front end of the same signature engine). It never deletes and never
// mentions --execute.
func writePlanText(w io.Writer, plan PrunePlan) {
	const rule = "------------------------------------------------------------"
	now := planTextNow()
	var prunable, withheld []PruneCandidate
	var prunableBytes, withheldBytes int64
	byReason := map[string]*reasonAgg{}
	for _, candidate := range plan.Candidates {
		if candidate.State == candidateStateWithheld {
			withheld = append(withheld, candidate)
			withheldBytes += candidate.Size
			reason := candidate.WithheldReason
			if reason == "" {
				reason = "unknown"
			}
			if byReason[reason] == nil {
				byReason[reason] = &reasonAgg{}
			}
			byReason[reason].n++
			byReason[reason].bytes += candidate.Size
		} else {
			prunable = append(prunable, candidate)
			prunableBytes += candidate.Size
		}
	}

	// Effective filters banner — aggregate over discovered rows (or defaults).
	// "mixed" means candidates used more than one resolved value; each row also
	// prints its own resolved gates so path/target overrides are never invisible.
	_, _ = fmt.Fprintf(w, "Effective filters: min_size=%s  min_age=%s  max_age=%s\n",
		displayFilter(plan.EffectiveFilters.MinSize),
		displayFilter(plan.EffectiveFilters.MinAge),
		displayFilter(plan.EffectiveFilters.MaxAge))
	_, _ = fmt.Fprintln(w, "(resolved per match: defaults → path → target; min_age=older-than floor, max_age=younger-than ceiling; mixed = more than one value among candidates)")
	_, _ = fmt.Fprintf(w, "Ranking: sort_mode=%s (idle = oldest known activity first; unknown activity last; advisory only)\n", displaySortMode(plan.SortMode))
	_, _ = fmt.Fprintln(w)

	if len(prunable) == 0 && len(withheld) == 0 {
		_, _ = fmt.Fprintln(w, "No prune candidates found.")
		return
	}
	if len(prunable) > 0 {
		_, _ = fmt.Fprintln(w, "Prune candidates:")
		_, _ = fmt.Fprintln(w, rule)
		for _, candidate := range prunable {
			_, _ = fmt.Fprintf(w, "%-12s  %-36s  %-24s  %s  %s\n",
				humanSize(candidate.Size), candidateLabel(candidate), formatActivity(candidate, now), candidate.Path, formatCandidateFilters(candidate))
		}
		_, _ = fmt.Fprintln(w, rule)
		_, _ = fmt.Fprintf(w, "Total reclaimable: %s\n", humanSize(prunableBytes))
	}
	if len(withheld) > 0 {
		if len(prunable) > 0 {
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintln(w, "Withheld (matched but not eligible for deletion):")
		_, _ = fmt.Fprintln(w, rule)
		for _, candidate := range withheld {
			_, _ = fmt.Fprintf(w, "%-12s  %-36s  %-24s  %s  [withheld: %s]  %s\n",
				humanSize(candidate.Size), candidateLabel(candidate), formatActivity(candidate, now), candidate.Path, candidate.WithheldReason, formatCandidateFilters(candidate))
		}
		_, _ = fmt.Fprintln(w, rule)
		_, _ = fmt.Fprintf(w, "Withheld total: %s\n", humanSize(withheldBytes))
		// Summary by reason, e.g. "Withheld: 6 candidates (11.4G) by age; 12 (80M) by min_size"
		if line := formatWithheldByReason(byReason); line != "" {
			_, _ = fmt.Fprintln(w, line)
		}
	}
}

// planTextNow is the clock for activity ages in text plans (overridable in tests).
var planTextNow = time.Now

// formatActivity renders a row's activity evidence. Anything short of a
// confidently established timestamp prints as "activity unknown"; a row is
// never labeled idle, because ranking is advisory and an incomplete
// measurement must not read as cold right before --execute.
func formatActivity(c PruneCandidate, now time.Time) string {
	if !engine.ActivityKnown(c) {
		return "activity unknown"
	}
	last := c.LastActivityAt.UTC()
	days := int(now.Sub(last).Hours() / 24)
	if days < 0 {
		days = 0
	}
	return fmt.Sprintf("last %s (%dd)", last.Format("2006-01-02"), days)
}

func displaySortMode(mode string) string {
	if mode == "" {
		return "size"
	}
	return mode
}

func formatCandidateFilters(c PruneCandidate) string {
	return fmt.Sprintf("[filters min_size=%s min_age=%s max_age=%s]",
		displayFilter(c.EffectiveMinSize), displayFilter(c.EffectiveMinAge), displayFilter(c.EffectiveMaxAge))
}

func displayFilter(v string) string {
	if v == "" {
		return "none"
	}
	return v
}

// reasonAgg aggregates withheld candidates by withheld_reason for the summary line.
type reasonAgg struct {
	n     int
	bytes int64
}

// formatWithheldByReason builds the operator summary. Stable reason order for tests.
func formatWithheldByReason(byReason map[string]*reasonAgg) string {
	if len(byReason) == 0 {
		return ""
	}
	// Prefer a stable, product-meaningful order.
	order := []string{withheldReasonAge, withheldReasonMinSize, withheldReasonSafeToPrune}
	seen := map[string]bool{}
	var parts []string
	appendPart := func(reason string, agg *reasonAgg) {
		if agg == nil || agg.n == 0 {
			return
		}
		parts = append(parts, fmt.Sprintf("%d candidates (%s) by %s", agg.n, humanSize(agg.bytes), reason))
	}
	for _, reason := range order {
		if agg, ok := byReason[reason]; ok {
			appendPart(reason, agg)
			seen[reason] = true
		}
	}
	// Any other reasons (defensive).
	var extras []string
	for reason := range byReason {
		if !seen[reason] {
			extras = append(extras, reason)
		}
	}
	sort.Strings(extras)
	for _, reason := range extras {
		appendPart(reason, byReason[reason])
	}
	if len(parts) == 0 {
		return ""
	}
	return "Withheld: " + strings.Join(parts, "; ")
}

func writePrunePlanJSON(w io.Writer, plan PrunePlan, loadedPath string, execute bool, now time.Time) error {
	payload, err := json.MarshalIndent(newPrunePlanOutput(plan, loadedPath, execute, now), "", "  ")
	if err != nil {
		return fmt.Errorf("encode prune plan json: %w", err)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitPrunePlanV1, payload); err != nil {
		return fmt.Errorf("prune plan json does not match spanwit schema: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", payload)
	return err
}

func boolPtr(v bool) *bool {
	return &v
}
