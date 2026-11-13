// Package profiles compiles reviewed persona bundles into existing policies.
// It has no activation, scheduling, command execution or deletion path.
package profiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/observe"
)

const MaxDocumentBytes = 1 << 20
const Schema = "https://schemas.3leaps.dev/spanwit/profile-pack/v0.json"

const (
	DefaultMaxSampleAge = 15 * time.Minute

	EvictionOrderIdleOldestFirst = "idle-oldest-first"

	AuthorityPropose = "propose"

	BudgetStatusWithin   = "within"
	BudgetStatusExceeded = "exceeded"
	BudgetStatusUnknown  = "unknown"

	ObjectiveOutcomeMet            = "met"
	ObjectiveOutcomeEvaluable      = "evaluable"
	ObjectiveOutcomeUnmetProtected = "unmet_protected"
	ObjectiveOutcomeUnknown        = "unknown"

	ApprovalStateNotNeeded           = "not_needed"
	ApprovalStateApprovalRequired    = "approval_required"
	ApprovalStateBlockedByProtection = "blocked_by_protection"
	ApprovalStateUnknownObservation  = "unknown_observation"
)

type RecipeBudget struct {
	MaxBytes      int64  `json:"max_bytes"`
	EvictionOrder string `json:"eviction_order,omitempty"`
	Authority     string `json:"authority,omitempty"`
}

type VolumeObjective struct {
	Path                string `json:"path,omitempty"`
	FloorBytes          int64  `json:"floor_bytes"`
	RecoveryTargetBytes int64  `json:"recovery_target_bytes"`
	MaxSampleAge        string `json:"max_sample_age,omitempty"`
}

type Profile struct {
	Label           string           `json:"label"`
	VolumeObjective *VolumeObjective `json:"volume_objective,omitempty"`
	Recipes         []string         `json:"recipes"`
}

type Recipe struct {
	Capability  string          `json:"capability"`
	Description string          `json:"description,omitempty"`
	Budget      *RecipeBudget   `json:"budget,omitempty"`
	Policy      json.RawMessage `json:"policy"`
}

type Document struct {
	Schema   string             `json:"$schema"`
	Version  int                `json:"version"`
	Recipes  map[string]Recipe  `json:"recipes"`
	Profiles map[string]Profile `json:"profiles"`
}

type Pack struct {
	raw      []byte
	document Document
	revision string
	policies map[string]*config.Config
}

// Load rejects duplicate object keys and excessive nesting before schema
// evaluation. The revision hashes the exact reviewed bytes, not a normalized
// semantic representation; even a whitespace edit creates a new revision.
func Load(r io.Reader) (*Pack, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxDocumentBytes {
		return nil, fmt.Errorf("profile document exceeds %d bytes", MaxDocumentBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := checkValue(dec, 0); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing profile data")
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitProfilePackV0, raw); err != nil {
		return nil, err
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	pack := &Pack{raw: append([]byte(nil), raw...), document: doc, revision: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), policies: map[string]*config.Config{}}
	for _, id := range sortedKeys(doc.Recipes) {
		recipe := doc.Recipes[id]
		if recipe.Budget != nil {
			if recipe.Budget.MaxBytes <= 0 {
				return nil, fmt.Errorf("recipe %s budget requires positive max_bytes", id)
			}
			if recipe.Budget.EvictionOrder != "" &&
				recipe.Budget.EvictionOrder != EvictionOrderIdleOldestFirst {
				return nil, fmt.Errorf("recipe %s budget unsupported eviction_order %q", id, recipe.Budget.EvictionOrder)
			}
			if recipe.Budget.Authority != "" && recipe.Budget.Authority != AuthorityPropose {
				return nil, fmt.Errorf("recipe %s budget unsupported authority %q", id, recipe.Budget.Authority)
			}
		}
		cfg, err := config.Parse(recipe.Policy)
		if err != nil {
			return nil, fmt.Errorf("recipe %s: %w", id, err)
		}
		// Pattern-only matching is intentionally not an authoring capability here.
		// Custom signatures remain supported through the existing evidence contract.
		signatures := config.MergeSignatureCatalogs(config.BuiltInSignatures(), cfg.Signatures)
		for _, path := range cfg.Paths {
			if !isAbsOrHomeRelative(path.Path) {
				return nil, fmt.Errorf("recipe %s requires absolute or home-relative roots", id)
			}
			for _, target := range path.Targets {
				if target.Signature == "" {
					return nil, fmt.Errorf("recipe %s requires signature-backed targets", id)
				}
				sig, _ := signatures.Lookup(target.Signature)
				if len(sig.RequiredAncestorFiles) == 0 && len(sig.AnyChildPaths) == 0 && len(sig.SiblingEvidence) == 0 && sig.ContentMarker == nil {
					return nil, fmt.Errorf("recipe %s requires context evidence beyond a candidate pattern", id)
				}
			}
		}
		pack.policies[id] = cfg
	}
	for _, id := range sortedKeys(doc.Profiles) {
		profile := doc.Profiles[id]
		if obj := profile.VolumeObjective; obj != nil {
			if obj.FloorBytes <= 0 || obj.RecoveryTargetBytes <= 0 {
				return nil, fmt.Errorf("profile %s volume_objective requires positive byte thresholds", id)
			}
			if obj.RecoveryTargetBytes < obj.FloorBytes {
				return nil, fmt.Errorf("profile %s volume_objective recovery_target_bytes (%d) must be >= floor_bytes (%d)", id, obj.RecoveryTargetBytes, obj.FloorBytes)
			}
			if obj.Path != "" && !isAbsOrHomeRelative(obj.Path) {
				return nil, fmt.Errorf("profile %s volume_objective requires absolute or home-relative path", id)
			}
			if obj.MaxSampleAge != "" {
				d, err := config.ParseAge(obj.MaxSampleAge)
				if err != nil || d <= 0 {
					return nil, fmt.Errorf("profile %s volume_objective invalid max_sample_age %q", id, obj.MaxSampleAge)
				}
			}
		}
		for _, recipe := range profile.Recipes {
			if _, ok := pack.policies[recipe]; !ok {
				return nil, fmt.Errorf("profile %s references unknown recipe %s", id, recipe)
			}
		}
	}
	return pack, nil
}

func isAbsOrHomeRelative(p string) bool {
	return filepath.IsAbs(p) || p == "~" || strings.HasPrefix(p, "~/")
}

func (p *Pack) Revision() string { return p.revision }
func (p *Pack) Bytes() []byte    { return append([]byte(nil), p.raw...) }
func (p *Pack) Names() []string  { return sortedKeys(p.document.Profiles) }

// Binding captures the execution-relevant resolved scope and compiled policy
// identity for a selected persona at activation time. Because HOME and built-in
// signature catalogs can change without altering the input-byte revision, the
// coordinator verifies both the exact input revision and this compiled binding
// before evaluating proposals.
type Binding struct {
	Profile              string   `json:"profile"`
	PackRevision         string   `json:"pack_revision"`
	ResolvedRoots        []string `json:"resolved_roots"`
	VolumeObjectivePath  string   `json:"volume_objective_path,omitempty"`
	CompiledPolicyDigest string   `json:"compiled_policy_digest"`
}

func (p *Pack) ResolveBinding(name string) (Binding, error) {
	profile, ok := p.document.Profiles[name]
	if !ok {
		return Binding{}, fmt.Errorf("unknown profile %q", name)
	}
	type compiledRecipe struct {
		ID            string         `json:"id"`
		Capability    string         `json:"capability"`
		Budget        *RecipeBudget  `json:"budget,omitempty"`
		Policy        config.Config  `json:"policy"`
		Signatures    map[string]any `json:"signatures"`
		ResolvedRoots []string       `json:"resolved_roots"`
	}
	var compiledRecipes []compiledRecipe
	seenRoots := map[string]bool{}
	resolvedRoots := []string{}
	var firstEnabledRoot string
	for _, id := range profile.Recipes {
		recipeSpec := p.document.Recipes[id]
		policy := *p.policies[id]
		policy.Paths = append([]config.PathProfile(nil), policy.Paths...)
		recipeRoots := []string{}
		for i, path := range policy.Paths {
			if path.IsEnabled() {
				root, err := engine.CleanConfiguredPath(path.Path)
				if err != nil {
					return Binding{}, err
				}
				if firstEnabledRoot == "" {
					firstEnabledRoot = root
				}
				policy.Paths[i].Path = root
				recipeRoots = append(recipeRoots, root)
				if !seenRoots[root] {
					seenRoots[root] = true
					resolvedRoots = append(resolvedRoots, root)
				}
			}
		}
		sigs := config.MergeSignatureCatalogs(config.BuiltInSignatures(), policy.Signatures)
		sigMap := map[string]any{}
		for _, path := range policy.Paths {
			for _, t := range path.Targets {
				if t.Signature != "" {
					if def, ok := sigs.Lookup(t.Signature); ok {
						sigMap[t.Signature] = def
					}
				}
			}
		}
		compiledRecipes = append(compiledRecipes, compiledRecipe{
			ID:            id,
			Capability:    recipeSpec.Capability,
			Budget:        recipeSpec.Budget,
			Policy:        policy,
			Signatures:    sigMap,
			ResolvedRoots: recipeRoots,
		})
	}
	sort.Strings(resolvedRoots)
	var objPath string
	if profile.VolumeObjective != nil {
		rawPath := profile.VolumeObjective.Path
		if rawPath == "" {
			rawPath = firstEnabledRoot
		}
		if rawPath == "" {
			rawPath = "~"
		}
		resolved, err := engine.CleanConfiguredPath(rawPath)
		if err != nil {
			return Binding{}, fmt.Errorf("volume_objective path: %w", err)
		}
		objPath = resolved
	}
	envelope := struct {
		Profile         string           `json:"profile"`
		VolumeObjective *VolumeObjective `json:"volume_objective,omitempty"`
		ObjectivePath   string           `json:"objective_path,omitempty"`
		Recipes         []compiledRecipe `json:"recipes"`
	}{
		Profile:         name,
		VolumeObjective: profile.VolumeObjective,
		ObjectivePath:   objPath,
		Recipes:         compiledRecipes,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return Binding{}, err
	}
	return Binding{
		Profile:              name,
		PackRevision:         p.revision,
		ResolvedRoots:        resolvedRoots,
		VolumeObjectivePath:  objPath,
		CompiledPolicyDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)),
	}, nil
}

type Candidate struct {
	Path             string `json:"path"`
	State            string `json:"state"`
	Signature        string `json:"signature"`
	SizeBytes        int64  `json:"size_bytes"`
	SizeBasis        string `json:"size_basis"`
	SizeIncomplete   bool   `json:"size_incomplete"`
	WithheldReason   string `json:"withheld_reason,omitempty"`
	LastActivityAt   string `json:"last_activity_at,omitempty"`
	ActivityBasis    string `json:"activity_basis,omitempty"`
	BudgetSelected   bool   `json:"budget_selected,omitempty"`
	BudgetRetained   bool   `json:"budget_retained,omitempty"`
	DedupeSuperseded string `json:"dedupe_superseded_by,omitempty"`
}

// BudgetEvaluation reports bounded per-recipe cache ceiling evaluation.
// Protected bytes (withheld by recency/age or unknown activity) are never
// selected for eviction even when they alone exceed MaxBytes.
type BudgetEvaluation struct {
	MaxBytes              int64  `json:"max_bytes"`
	EvictionOrder         string `json:"eviction_order"`
	Authority             string `json:"authority"`
	Status                string `json:"status"`
	SizeBasis             string `json:"size_basis,omitempty"`
	ObservedBytes         int64  `json:"observed_bytes"`
	ProtectedBytes        int64  `json:"protected_bytes"`
	EligibleBytes         int64  `json:"eligible_bytes"`
	OverageBytes          int64  `json:"overage_bytes"`
	SelectedBytes         int64  `json:"selected_bytes"`
	SelectedCount         int    `json:"selected_count"`
	RetainedBytes         int64  `json:"retained_bytes"`
	ProtectedOverageBytes int64  `json:"protected_overage_bytes"`
	ApprovalState         string `json:"approval_state"`
}

type RecipePreview struct {
	Roots                []string          `json:"roots"`
	ID                   string            `json:"id"`
	State                string            `json:"state"`
	ProposedItemsDigest  string            `json:"proposed_items_digest,omitempty"`
	ProposedItemsCount   int               `json:"proposed_items_count"`
	ProposedReclaimBytes int64             `json:"proposed_reclaim_bytes"`
	Budget               *BudgetEvaluation `json:"budget,omitempty"`
	Candidates           []Candidate       `json:"candidates"`
	Warnings             []string          `json:"warnings"`
}

// VolumeObjectiveEvaluation evaluates a profile's volume headroom floor and
// recovery target against a fresh capacity observation and deduplicated
// recipe candidates. Diagnostic pressure severity remains independent.
type VolumeObjectiveEvaluation struct {
	Path                       string   `json:"path"`
	VolumeID                   string   `json:"volume_id,omitempty"`
	CapturedAt                 string   `json:"captured_at,omitempty"`
	FloorBytes                 int64    `json:"floor_bytes"`
	RecoveryTargetBytes        int64    `json:"recovery_target_bytes"`
	AvailBytes                 *int64   `json:"avail_bytes,omitempty"`
	TotalBytes                 *int64   `json:"total_bytes,omitempty"`
	UsedPercent                *float64 `json:"used_percent,omitempty"`
	DiagnosticLevel            string   `json:"diagnostic_level,omitempty"`
	ObservationFresh           bool     `json:"observation_fresh"`
	ObservationComplete        bool     `json:"observation_complete"`
	Outcome                    string   `json:"outcome"`
	SizeBasis                  string   `json:"size_basis,omitempty"`
	DeduplicatedProtectedBytes int64    `json:"deduplicated_protected_bytes"`
	DeduplicatedEligibleBytes  int64    `json:"deduplicated_eligible_bytes"`
	DeduplicatedSelectedBytes  int64    `json:"deduplicated_selected_bytes"`
	DroppedOverlapCount        int      `json:"dropped_overlap_count"`
	FloorShortfallBytes        int64    `json:"floor_shortfall_bytes"`
	RecoveryShortfallBytes     int64    `json:"recovery_shortfall_bytes"`
	UncoveredShortfallBytes    int64    `json:"uncovered_shortfall_bytes"`
	ApprovalRequestState       string   `json:"approval_request_state"`
}

type Preview struct {
	Version             int                        `json:"version"`
	Revision            string                     `json:"revision"`
	Profile             string                     `json:"profile"`
	State               string                     `json:"state"`
	ExecutionAuthorized bool                       `json:"execution_authorized"`
	VolumeObjective     *VolumeObjectiveEvaluation `json:"volume_objective,omitempty"`
	Recipes             []RecipePreview            `json:"recipes"`
}

// PreviewOptions customizes capacity observation and freshness checks during
// profile preview/evaluation (primarily for deterministic testing or daemon
// sample injection).
type PreviewOptions struct {
	Sampler observe.Sampler
	Now     time.Time
}

// Preview uses the existing planner without exporting a deletion handoff. Rows
// from different recipes may overlap; per-recipe previews show each recipe's own
// view while VolumeObjective (when configured) deduplicates overlapping paths.
func (p *Pack) Preview(ctx context.Context, name string) (Preview, error) {
	return p.PreviewWithOptions(ctx, name, PreviewOptions{})
}

func (p *Pack) PreviewWithOptions(ctx context.Context, name string, opts PreviewOptions) (Preview, error) {
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	profile, ok := p.document.Profiles[name]
	if !ok {
		return Preview{}, fmt.Errorf("unknown profile %q", name)
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	sampler := opts.Sampler
	if sampler == nil {
		sampler = observe.CapacitySampler{}
	}
	out := Preview{Revision: p.revision, Profile: name, State: "complete", Recipes: []RecipePreview{}}
	var firstEnabledRoot string
	for _, id := range profile.Recipes {
		if err := ctx.Err(); err != nil {
			return Preview{}, err
		}
		recipeSpec := p.document.Recipes[id]
		// Resolve into a private copy: the planner and scope disclosure consume
		// the same absolute values without changing the reviewed document.
		policy := *p.policies[id]
		policy.Paths = append([]config.PathProfile(nil), policy.Paths...)
		row := RecipePreview{ID: id, State: "complete", Roots: []string{}, Candidates: []Candidate{}, Warnings: []string{}}
		for i, path := range policy.Paths {
			if path.IsEnabled() {
				root, err := engine.CleanConfiguredPath(path.Path)
				if err != nil {
					return Preview{}, err
				}
				if firstEnabledRoot == "" {
					firstEnabledRoot = root
				}
				row.Roots = append(row.Roots, root)
				policy.Paths[i].Path = root
			}
		}
		var planOpts []engine.PlanOptions
		if recipeSpec.Budget != nil {
			planOpts = append(planOpts, engine.WithSortMode(engine.SortIdle))
		}
		plan, err := engine.BuildPrunePlan(ctx, &policy, planOpts...)
		if err != nil {
			return Preview{}, fmt.Errorf("recipe %s: %w", id, err)
		}
		for _, warning := range plan.Warnings {
			row.Warnings = append(row.Warnings, warning.Error())
		}
		if len(plan.Warnings) > 0 || plan.SizeIncomplete {
			row.State = "partial"
			out.State = "partial"
		}
		for _, c := range plan.Candidates {
			cand := Candidate{
				Path:           c.Path,
				State:          c.State,
				Signature:      c.Signature,
				SizeBytes:      c.Size,
				SizeBasis:      c.SizeBasis,
				SizeIncomplete: c.SizeIncomplete,
				WithheldReason: c.WithheldReason,
				ActivityBasis:  c.ActivityBasis,
			}
			if !c.LastActivityAt.IsZero() && !c.ActivityIncomplete {
				cand.LastActivityAt = c.LastActivityAt.UTC().Format(time.RFC3339Nano)
			}
			row.Candidates = append(row.Candidates, cand)
		}
		if recipeSpec.Budget != nil {
			eval := evaluateRecipeBudget(*recipeSpec.Budget, row.State == "complete", plan.SizeBasis, row.Candidates)
			row.Budget = &eval
		}
		out.Recipes = append(out.Recipes, row)
	}
	droppedOverlap, dedupStats := deduplicateRecipes(out.Recipes)
	for i := range out.Recipes {
		computeProposedItems(&out.Recipes[i])
	}
	if profile.VolumeObjective != nil {
		eval, err := evaluateVolumeObjective(ctx, *profile.VolumeObjective, firstEnabledRoot, out.State == "complete", droppedOverlap, dedupStats, sampler, now)
		if err != nil {
			return Preview{}, err
		}
		out.VolumeObjective = &eval
	}
	return out, nil
}

func computeProposedItems(row *RecipePreview) {
	if row.State != "complete" {
		return
	}
	type item struct {
		Path      string `json:"path"`
		Signature string `json:"signature"`
		SizeBytes int64  `json:"size_bytes"`
		SizeBasis string `json:"size_basis"`
	}
	var items []item
	var reclaimBytes int64
	for _, c := range row.Candidates {
		if c.State != engine.CandidateStatePrunable || c.SizeIncomplete || c.DedupeSuperseded != "" {
			continue
		}
		if row.Budget != nil && !c.BudgetSelected {
			continue
		}
		items = append(items, item{
			Path:      c.Path,
			Signature: c.Signature,
			SizeBytes: c.SizeBytes,
			SizeBasis: c.SizeBasis,
		})
		reclaimBytes += c.SizeBytes
	}
	row.ProposedItemsCount = len(items)
	row.ProposedReclaimBytes = reclaimBytes
	if len(items) == 0 {
		return
	}
	raw, err := json.Marshal(struct {
		Recipe string   `json:"recipe"`
		Roots  []string `json:"roots"`
		Items  []item   `json:"items"`
	}{
		Recipe: row.ID,
		Roots:  row.Roots,
		Items:  items,
	})
	if err == nil {
		row.ProposedItemsDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	}
}

func evaluateRecipeBudget(spec RecipeBudget, recipeComplete bool, sizeBasis string, candidates []Candidate) BudgetEvaluation {
	order := spec.EvictionOrder
	if order == "" {
		order = EvictionOrderIdleOldestFirst
	}
	auth := spec.Authority
	if auth == "" {
		auth = AuthorityPropose
	}
	eval := BudgetEvaluation{
		MaxBytes:      spec.MaxBytes,
		EvictionOrder: order,
		Authority:     auth,
		SizeBasis:     sizeBasis,
	}
	incomplete := !recipeComplete
	for _, c := range candidates {
		if c.SizeIncomplete {
			incomplete = true
		}
		eval.ObservedBytes += c.SizeBytes
		// Unknown or incomplete activity (LastActivityAt == "") is never treated
		// as cold/eligible for oldest-first budget eviction; it is counted under
		// ProtectedBytes so it cannot be selected for deletion.
		if c.State == engine.CandidateStatePrunable && !c.SizeIncomplete && c.LastActivityAt != "" {
			eval.EligibleBytes += c.SizeBytes
		} else {
			eval.ProtectedBytes += c.SizeBytes
		}
	}
	if incomplete {
		eval.Status = BudgetStatusUnknown
		eval.ApprovalState = ApprovalStateUnknownObservation
		return eval
	}
	if eval.ObservedBytes <= spec.MaxBytes {
		eval.Status = BudgetStatusWithin
		eval.RetainedBytes = eval.ObservedBytes
		eval.ApprovalState = ApprovalStateNotNeeded
		for i := range candidates {
			if candidates[i].State == engine.CandidateStatePrunable && !candidates[i].SizeIncomplete && candidates[i].LastActivityAt != "" {
				candidates[i].BudgetRetained = true
			}
		}
		return eval
	}
	eval.Status = BudgetStatusExceeded
	eval.OverageBytes = eval.ObservedBytes - spec.MaxBytes
	if eval.ProtectedBytes > spec.MaxBytes {
		eval.ProtectedOverageBytes = eval.ProtectedBytes - spec.MaxBytes
	}
	// Candidates are ordered by engine.SortIdle (known oldest-activity first,
	// then size descending, then path; unknown activity last). Select only
	// prunable candidates with known activity in order until SelectedBytes >= OverageBytes.
	for i := range candidates {
		if candidates[i].State != engine.CandidateStatePrunable || candidates[i].SizeIncomplete || candidates[i].LastActivityAt == "" {
			continue
		}
		if eval.SelectedBytes < eval.OverageBytes {
			candidates[i].BudgetSelected = true
			eval.SelectedBytes += candidates[i].SizeBytes
			eval.SelectedCount++
		} else {
			candidates[i].BudgetRetained = true
		}
	}
	eval.RetainedBytes = eval.ObservedBytes - eval.SelectedBytes
	switch {
	case eval.SelectedCount > 0:
		eval.ApprovalState = ApprovalStateApprovalRequired
	case eval.ProtectedBytes > 0:
		eval.ApprovalState = ApprovalStateBlockedByProtection
	default:
		eval.ApprovalState = ApprovalStateNotNeeded
	}
	return eval
}

type deduplicatedCandidateStats struct {
	sizeBasis         string
	basisMixed        bool
	anyCandIncomplete bool
	protectedBytes    int64
	eligibleBytes     int64
	selectedBytes     int64
}

func isCandidateProposed(r RecipePreview, c Candidate) bool {
	if c.State != engine.CandidateStatePrunable || c.SizeIncomplete {
		return false
	}
	if r.Budget != nil {
		return c.BudgetSelected
	}
	return true
}

func isCandidateEligible(r RecipePreview, c Candidate) bool {
	if c.State != engine.CandidateStatePrunable || c.SizeIncomplete {
		return false
	}
	if r.Budget != nil {
		return c.LastActivityAt != ""
	}
	return true
}

// candidatePriority ranks an overlapping candidate across recipes so that a
// proposed (selected) candidate takes precedence over an eligible-but-retained
// candidate, which in turn takes precedence over a withheld/protected candidate.
func candidatePriority(r RecipePreview, c Candidate) int {
	if isCandidateProposed(r, c) {
		return 2
	}
	if isCandidateEligible(r, c) {
		return 1
	}
	return 0
}

// deduplicateRecipes marks overlapping candidates across recipes (exact path
// duplicates and ancestor/descendant containment) before computeProposedItems
// and volume_objective evaluation run.
func deduplicateRecipes(recipes []RecipePreview) (int, deduplicatedCandidateStats) {
	type flatCand struct {
		recipeIdx int
		candIdx   int
		path      string
		cand      Candidate
	}
	var all []flatCand
	var stats deduplicatedCandidateStats
	for ri := range recipes {
		for ci := range recipes[ri].Candidates {
			c := recipes[ri].Candidates[ci]
			if c.SizeIncomplete {
				stats.anyCandIncomplete = true
			}
			if c.SizeBasis != "" {
				if stats.sizeBasis == "" {
					stats.sizeBasis = c.SizeBasis
				} else if stats.sizeBasis != c.SizeBasis {
					stats.basisMixed = true
				}
			}
			clean := filepath.Clean(c.Path)
			all = append(all, flatCand{recipeIdx: ri, candIdx: ci, path: clean, cand: c})
		}
	}
	if stats.basisMixed {
		stats.sizeBasis = ""
	}

	droppedOverlap := 0
	var kept []flatCand
	for _, item := range all {
		handled := false
		for ki := range kept {
			k := kept[ki]
			if k.path == item.path {
				// Exact same path across two recipes: keep whichever has higher
				// selection/eligibility priority (tie-breaking to the earlier recipe).
				if candidatePriority(recipes[item.recipeIdx], item.cand) > candidatePriority(recipes[k.recipeIdx], k.cand) {
					recipes[k.recipeIdx].Candidates[k.candIdx].DedupeSuperseded = item.path
					kept[ki] = item
				} else {
					recipes[item.recipeIdx].Candidates[item.candIdx].DedupeSuperseded = k.path
				}
				droppedOverlap++
				handled = true
				break
			}
			if isSameOrDescendantPath(k.path, item.path) {
				recipes[item.recipeIdx].Candidates[item.candIdx].DedupeSuperseded = k.path
				droppedOverlap++
				handled = true
				break
			}
		}
		if handled {
			continue
		}
		// Check if this shorter path is a strict ancestor of an already-kept longer path.
		filtered := kept[:0]
		for _, k := range kept {
			if isSameOrDescendantPath(item.path, k.path) {
				droppedOverlap++
				recipes[k.recipeIdx].Candidates[k.candIdx].DedupeSuperseded = item.path
				continue
			}
			filtered = append(filtered, k)
		}
		kept = append(filtered, item)
	}

	for _, k := range kept {
		c := recipes[k.recipeIdx].Candidates[k.candIdx]
		if isCandidateEligible(recipes[k.recipeIdx], c) {
			stats.eligibleBytes += c.SizeBytes
			if isCandidateProposed(recipes[k.recipeIdx], c) {
				stats.selectedBytes += c.SizeBytes
			}
		} else {
			stats.protectedBytes += c.SizeBytes
		}
	}
	return droppedOverlap, stats
}

func evaluateVolumeObjective(
	ctx context.Context,
	spec VolumeObjective,
	defaultPath string,
	recipesComplete bool,
	droppedOverlap int,
	stats deduplicatedCandidateStats,
	sampler observe.Sampler,
	now time.Time,
) (VolumeObjectiveEvaluation, error) {
	rawPath := spec.Path
	if rawPath == "" {
		rawPath = defaultPath
	}
	if rawPath == "" {
		rawPath = "~"
	}
	resolvedPath, err := engine.CleanConfiguredPath(rawPath)
	if err != nil {
		return VolumeObjectiveEvaluation{}, fmt.Errorf("volume_objective path: %w", err)
	}
	maxAge := DefaultMaxSampleAge
	if spec.MaxSampleAge != "" {
		d, err := config.ParseAge(spec.MaxSampleAge)
		if err != nil || d <= 0 {
			return VolumeObjectiveEvaluation{}, fmt.Errorf("volume_objective invalid max_sample_age %q", spec.MaxSampleAge)
		}
		maxAge = d
	}
	eval := VolumeObjectiveEvaluation{
		Path:                       resolvedPath,
		FloorBytes:                 spec.FloorBytes,
		RecoveryTargetBytes:        spec.RecoveryTargetBytes,
		SizeBasis:                  stats.sizeBasis,
		DeduplicatedProtectedBytes: stats.protectedBytes,
		DeduplicatedEligibleBytes:  stats.eligibleBytes,
		DeduplicatedSelectedBytes:  stats.selectedBytes,
		DroppedOverlapCount:        droppedOverlap,
	}

	sample, sampleErr := sampler.Sample(ctx, resolvedPath)
	if sampleErr != nil {
		eval.Outcome = ObjectiveOutcomeUnknown
		eval.ApprovalRequestState = ApprovalStateUnknownObservation
		return eval, nil
	}
	if sample.CapturedAt.IsZero() {
		sample.CapturedAt = now.UTC()
	}
	eval.VolumeID = string(sample.VolumeID)
	eval.CapturedAt = sample.CapturedAt.UTC().Format(time.RFC3339Nano)
	eval.DiagnosticLevel = sample.Level
	avail := sample.AvailBytes
	total := sample.TotalBytes
	usedPct := sample.UsedPercent
	eval.AvailBytes = &avail
	eval.TotalBytes = &total
	eval.UsedPercent = &usedPct

	sampleAge := now.Sub(sample.CapturedAt)
	eval.ObservationFresh = sampleAge <= maxAge && sampleAge >= -time.Minute
	eval.ObservationComplete = sample.Coverage == observe.CoverageComplete &&
		sample.SizeComplete &&
		sample.Basis == observe.BasisStatfs &&
		recipesComplete &&
		!stats.anyCandIncomplete &&
		!stats.basisMixed

	if avail < spec.FloorBytes {
		eval.FloorShortfallBytes = spec.FloorBytes - avail
	}
	if avail < spec.RecoveryTargetBytes {
		eval.RecoveryShortfallBytes = spec.RecoveryTargetBytes - avail
	}

	if !eval.ObservationFresh || !eval.ObservationComplete {
		eval.Outcome = ObjectiveOutcomeUnknown
		eval.ApprovalRequestState = ApprovalStateUnknownObservation
		return eval, nil
	}

	if avail >= spec.FloorBytes {
		eval.Outcome = ObjectiveOutcomeMet
		if avail < spec.RecoveryTargetBytes && eval.DeduplicatedSelectedBytes > 0 {
			eval.ApprovalRequestState = ApprovalStateApprovalRequired
		} else {
			eval.ApprovalRequestState = ApprovalStateNotNeeded
		}
		if eval.RecoveryShortfallBytes > eval.DeduplicatedEligibleBytes {
			eval.UncoveredShortfallBytes = eval.RecoveryShortfallBytes - eval.DeduplicatedEligibleBytes
		}
		return eval, nil
	}

	// Below floor: check whether deduplicated eligible bytes cover the recovery
	// shortfall (or whether protected bytes block recovery).
	if eval.RecoveryShortfallBytes > eval.DeduplicatedEligibleBytes {
		eval.UncoveredShortfallBytes = eval.RecoveryShortfallBytes - eval.DeduplicatedEligibleBytes
	}
	if eval.DeduplicatedEligibleBytes >= eval.FloorShortfallBytes && eval.DeduplicatedSelectedBytes > 0 {
		eval.Outcome = ObjectiveOutcomeEvaluable
		eval.ApprovalRequestState = ApprovalStateApprovalRequired
		return eval, nil
	}
	eval.Outcome = ObjectiveOutcomeUnmetProtected
	if eval.DeduplicatedSelectedBytes > 0 {
		eval.ApprovalRequestState = ApprovalStateApprovalRequired
	} else {
		eval.ApprovalRequestState = ApprovalStateBlockedByProtection
	}
	return eval, nil
}

func isSameOrDescendantPath(ancestor, child string) bool {
	if ancestor == child {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(child, ancestor+sep)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func checkValue(d *json.Decoder, depth int) error {
	if depth > 32 {
		return fmt.Errorf("profile nesting exceeds 32")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return fmt.Errorf("duplicate or invalid profile key")
			}
			seen[s] = true
			if err := checkValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("invalid profile object")
		}
	case '[':
		for d.More() {
			if err := checkValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("invalid profile array")
		}
	default:
		return fmt.Errorf("invalid profile delimiter")
	}
	return nil
}

// Change exposes both sides of a reviewed edit, including dormant recipes.
type Change struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}
type Diff struct {
	Version      int      `json:"version"`
	FromRevision string   `json:"from_revision"`
	ToRevision   string   `json:"to_revision"`
	Recipes      []Change `json:"recipes"`
	Profiles     []Change `json:"profiles"`
}

func (p *Pack) Diff(next *Pack) Diff {
	return Diff{FromRevision: p.revision, ToRevision: next.revision, Recipes: changes(p.document.Recipes, next.document.Recipes), Profiles: changes(p.document.Profiles, next.document.Profiles)}
}
func changes[V any](before, after map[string]V) []Change {
	all := map[string]bool{}
	for id := range before {
		all[id] = true
	}
	for id := range after {
		all[id] = true
	}
	out := []Change{}
	for _, id := range sortedKeys(all) {
		a, aok := before[id]
		b, bok := after[id]
		switch {
		case !aok:
			out = append(out, Change{ID: id, Kind: "added", After: b})
		case !bok:
			out = append(out, Change{ID: id, Kind: "removed", Before: a})
		default:
			// All values come from strictly validated JSON. Marshaling sorts map keys
			// and compacts embedded policy JSON for a stable review comparison.
			ar, _ := json.Marshal(a)
			br, _ := json.Marshal(b)
			if !bytes.Equal(ar, br) {
				out = append(out, Change{ID: id, Kind: "changed", Before: a, After: b})
			}
		}
	}
	return out
}
