package space

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/inventory"
)

// Analysis phase names for ordered work and tests (crisis performance).
const (
	PhasePressure   = "pressure"
	PhaseHotspots   = "hotspots"
	PhaseRecipes    = "recipes"
	PhaseVerified   = "verified"
	PhaseUnverified = "unverified"
	PhaseUnknown    = "unknown"
	PhaseHandoff    = "handoff"
)

// Options configures a Space diagnostic run.
type Options struct {
	// Path is the root to analyze (default "."). Expanded with ~ support.
	Path string
	// MinSize is an opt-in size floor (empty = no floor). Applies to listing
	// filters; empty never injects a silent default.
	MinSize string
	// Top caps listed rows per multi-entry section (default 20).
	// Truncation is display-only; classification uses complete path sets.
	Top int
	// MaxDepth bounds name-shaped unverified discovery under Path (default 12).
	// Negative means unlimited (not recommended for large trees).
	MaxDepth int
	// Observation walker controls. They never configure the verified engine.
	Workers       int
	Backend       string
	IncludeRemote bool
	StallTimeout  time.Duration
	OnStall       func(inventory.StallAlert)
	// IncludeHomeCaches sizes known home cache/toolchain hotspots (default true).
	IncludeHomeCaches bool
	// DisableRecipes skips guided diagnostic recipes (rustup, …). Recipes run
	// by default whenever IncludeHomeCaches is true.
	DisableRecipes bool
	// SortMode is engine.SortIdle (default for space) or engine.SortSize.
	// Ranking is advisory display order only.
	SortMode string
	// ReclaimScope is whole (default) or incremental (Cargo incremental only).
	ReclaimScope string
	// ClassFilters are opt-in ADR-0004 state selectors (OR). Empty = broad.
	// Prefer NormalizeInventoryFilters before Analyze for validation.
	ClassFilters []string
	// DomainFilters are opt-in taxonomy selectors (OR). Empty = broad.
	// Segment-aware match on signature/catalog_id only.
	DomainFilters []string
	// MutationContract is the effective invocation no-mutation assertion
	// (open | read_only). Empty omits the field.
	MutationContract string
	// Config optional additive signatures (same posture as scan --config).
	Config *config.Config
	// Now overrides the report timestamp (tests).
	Now time.Time
	// OnPhase is invoked as each analysis phase begins (tests / observability).
	// Phases: pressure → hotspots → recipes → verified → unverified → unknown → handoff.
	OnPhase func(phase string)
	// OnPartial delivers crisis-triage data (pressure/hotspots/recipes) as soon
	// as those phases complete, before deep root walks. JSON still emits one
	// atomic report at the end. Hotspots/recipes are already class/domain projected.
	OnPartial func(PartialUpdate)
	// ForcePressureLevel overrides measured pressure.Level for tests (empty = use measured).
	ForcePressureLevel string
	// DisableTempPlanes skips the warn/critical temp-plane coverage probe.
	DisableTempPlanes bool
	// tempPlaneCandidates / tempPlaneDeadline override the platform defaults
	// in tests (nil / zero = defaults).
	tempPlaneCandidates []tempPlaneCandidate
	tempPlaneDeadline   time.Duration
}

func (o Options) phase(name string) {
	if o.OnPhase != nil {
		o.OnPhase(name)
	}
}

func (o Options) partial(u PartialUpdate) {
	if o.OnPartial != nil {
		o.OnPartial(u)
	}
}

// Analyze builds a read-only SpaceReport. It never deletes and has no execute path.
//
// Work order prioritizes crisis triage: pressure and home-cache hotspots are
// collected before deep walks of the analysis root (verified / unverified / unknown).
//
// Inventory projection pipeline (class/domain filters):
//  1. broad classification / measurement (unfiltered)
//  2. class/domain projection on full row sets
//  3. recompute visible section counts/bytes from filtered sets
//  4. apply --top only to displayed rows
//  5. build prune_handoff from the full filtered prunable closed set
func Analyze(ctx context.Context, opts Options) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if opts.Path == "" {
		opts.Path = "."
	}
	if opts.Top <= 0 {
		opts.Top = 20
	}
	if opts.MaxDepth == 0 {
		opts.MaxDepth = DefaultMaxDepth
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	invFilters, err := NormalizeInventoryFilters(opts.ClassFilters, opts.DomainFilters)
	if err != nil {
		return Report{}, err
	}

	// Resolve authorize-narrow policy and the domain catalog (built-in + user
	// overlay). Overlay/link errors are usage errors surfaced to the caller.
	enabled := config.ResolveEnabledDomains(opts.Config)
	cat, err := config.ResolveCatalog(opts.Config)
	if err != nil {
		return Report{}, err
	}

	root, err := engine.CleanConfiguredPath(opts.Path)
	if err != nil {
		return Report{}, err
	}

	var minSize int64
	minSizeDisplay := "none"
	if opts.MinSize != "" {
		minSize, err = engine.ParseSize(opts.MinSize)
		if err != nil {
			return Report{}, fmt.Errorf("invalid min_size %q: %w", opts.MinSize, err)
		}
		minSizeDisplay = opts.MinSize
	}

	sizeBound := opts.MaxDepth
	if sizeBound < 0 {
		sizeBound = -1
	}

	// --- Phase: pressure (cheap; first) ---
	opts.phase(PhasePressure)
	primaryPath := primaryWritePath(root)
	pressure, err := diskPressure(primaryPath)
	if err != nil {
		return Report{}, err
	}
	pressure.Role = PressureRolePrimaryWrite
	if opts.ForcePressureLevel != "" {
		pressure.Level = opts.ForcePressureLevel
	}

	var analysisPressure *Pressure
	rootPressure, rootErr := diskPressure(root)
	if rootErr == nil && !sameVolume(pressure, rootPressure) {
		rootPressure.Role = PressureRoleAnalysisRoot
		analysisPressure = &rootPressure
	}

	// --- Temp-plane coverage (warn/critical only; bounded, observation-only) ---
	var tempPlanes *TempPlaneCoverage
	if !opts.DisableTempPlanes && (pressure.Level == PressureWarn || pressure.Level == PressureCritical) {
		candidates := opts.tempPlaneCandidates
		if candidates == nil {
			candidates = defaultTempPlaneCandidates()
		}
		deadline := opts.tempPlaneDeadline
		if deadline <= 0 {
			deadline = tempPlaneProbeDeadline
		}
		tempPlanes = collectTempPlaneCoverage(ctx, root, pressure.Level, candidates, deadline)
	}

	budget := ApplyCriticalWorkBudget(pressure.Level, opts.MaxDepth)
	deepMaxDepth := budget.DeepMaxDepth
	skipUnknown := budget.SkipUnknown
	workBudgetNote := ""
	if len(budget.Notes) > 0 {
		workBudgetNote = budget.Notes[0]
		for i := 1; i < len(budget.Notes); i++ {
			workBudgetNote += "; " + budget.Notes[i]
		}
	}

	var warnings []string
	observation := newObservationWalker(opts)

	// --- Phase: hotspots (home caches; before deep root walk) ---
	hotspotsFull := make([]Entry, 0)
	if opts.IncludeHomeCaches {
		opts.phase(PhaseHotspots)
		var hw []string
		hotspotsFull, hw = collectHotspots(ctx, cat, minSize, sizeBound, observation)
		warnings = append(warnings, hw...)
	}

	// --- Phase: recipes (cheap listing; with hotspots posture) ---
	var recipes []Recipe
	if opts.IncludeHomeCaches && !opts.DisableRecipes {
		opts.phase(PhaseRecipes)
		recipes = BuildRecipes(ctx, RecipeOptions{IncludeRustup: true, Catalog: cat, Enabled: enabled})
	}
	if recipes == nil {
		recipes = []Recipe{}
	}
	// Annotate catalog taxonomy on hotspots/recipes before any projection.
	for i := range hotspotsFull {
		AnnotateEntryTaxonomy(&hotspotsFull[i])
	}
	for i := range recipes {
		AnnotateRecipeTaxonomy(&recipes[i])
	}

	// Deliver early triage (display-truncated, already class/domain projected)
	// before deep root walks. Pressure always streams; listing surfaces filter.
	pCopy := pressure
	earlyHotspots := ProjectEntries(hotspotsFull, invFilters)
	earlyRecipes := ProjectRecipes(recipes, invFilters)
	opts.partial(PartialUpdate{
		Phase:          PhaseHotspots,
		Pressure:       &pCopy,
		Hotspots:       truncateHotspotDisplay(earlyHotspots, opts.Top),
		Recipes:        append([]Recipe(nil), earlyRecipes...),
		WorkBudgetNote: workBudgetNote,
		TempPlanes:     tempPlanes,
	})

	// --- Phase: verified reclaimable (deep walk under analysis root) ---
	opts.phase(PhaseVerified)
	maxDepth := deepMaxDepth
	if maxDepth < 0 {
		maxDepth = -1
	}
	// Align sizing bound with discovery depth under critical budget.
	if pressure.Level == PressureCritical && sizeBound > deepMaxDepth && deepMaxDepth >= 0 {
		sizeBound = deepMaxDepth
	}
	cfg := &config.Config{
		Version: 1,
		Paths: []config.PathProfile{{
			Path:     root,
			MaxDepth: maxDepth,
			Targets:  builtInSignatureTargets(),
		}},
	}
	if opts.MinSize != "" {
		cfg.Defaults.MinSize = opts.MinSize
	}
	if opts.Config != nil {
		cfg.Signatures = opts.Config.Signatures
		// Carry the domain policy so the engine applies the same authorize-narrow
		// gate as direct prune (single mechanism; disabled-domain prunables are
		// withheld/domain_disabled here too, so handoff excludes them).
		cfg.Domains = opts.Config.Domains
		for _, p := range opts.Config.Paths {
			cfg.Paths[0].Targets = append(cfg.Paths[0].Targets, p.Targets...)
		}
	}

	sortMode := opts.SortMode
	if sortMode == "" {
		sortMode = engine.SortIdle // space defaults to idle-first for crisis triage
	}
	reclaimScope := opts.ReclaimScope
	if reclaimScope == "" {
		reclaimScope = engine.ReclaimScopeWhole
	}
	plan, err := engine.BuildPrunePlan(ctx, cfg,
		engine.WithSizeMaxDepth(sizeBound),
		engine.WithSortMode(sortMode),
		engine.WithReclaimScope(reclaimScope),
	)
	if err != nil {
		return Report{}, err
	}

	verifiedFull := VerifiedSection{
		Candidates:     make([]Entry, 0, len(plan.Candidates)),
		SizeIncomplete: plan.SizeIncomplete,
	}
	verifiedPaths := make(map[string]bool, len(plan.Candidates))
	domainDisabledCount := 0
	for _, c := range plan.Candidates {
		verifiedPaths[c.Path] = true
		// Also skip parent paths so unverified doesn't re-list partial parents.
		if c.ParentPath != "" {
			verifiedPaths[c.ParentPath] = true
		}
		entry := Entry{
			Path:               c.Path,
			SizeBytes:          c.Size,
			SizeHuman:          engine.HumanSize(c.Size),
			SizeIncomplete:     c.SizeIncomplete,
			State:              c.State,
			WithheldReason:     c.WithheldReason,
			Signature:          c.Signature,
			Pattern:            c.Pattern,
			Evidence:           c.Evidence,
			EffectiveMinSize:   c.EffectiveMinSize,
			EffectiveMinAge:    c.EffectiveMinAge,
			EffectiveMaxAge:    c.EffectiveMaxAge,
			ActivityBasis:      c.ActivityBasis,
			ActivityIncomplete: c.ActivityIncomplete,
			RebuildExpectation: c.RebuildExpectation,
			ReclaimScope:       c.ReclaimScope,
			ParentPath:         c.ParentPath,
		}
		if !c.LastActivityAt.IsZero() {
			entry.LastActivityAt = c.LastActivityAt.UTC().Format(time.RFC3339)
		}
		entry.ReclaimScope = NormalizeReclaimScope(c.ReclaimScope)
		if entry.ReclaimScope == engine.ReclaimScopeWhole {
			entry.ParentPath = ""
		}
		if entry.Evidence == nil {
			entry.Evidence = []string{}
		}
		AnnotateEntryTaxonomy(&entry)
		// The engine already applied the authorize-narrow gate (a disabled-domain
		// prunable arrives here as withheld/domain_disabled). Count it for the note;
		// the withheld state keeps it visible but out of the prune handoff.
		if entry.WithheldReason == WithheldReasonDomainDisabled {
			domainDisabledCount++
		}
		if c.SizeIncomplete {
			verifiedFull.SizeIncomplete = true
		}
		if entry.State == StateWithheld {
			verifiedFull.WithheldCandidates++
			verifiedFull.WithheldBytes += c.Size
		} else {
			verifiedFull.PrunableCandidates++
			verifiedFull.PrunableBytes += c.Size
		}
		verifiedFull.Candidates = append(verifiedFull.Candidates, entry)
	}
	verifiedFull.PrunableHuman = engine.HumanSize(verifiedFull.PrunableBytes)
	verifiedFull.WithheldHuman = engine.HumanSize(verifiedFull.WithheldBytes)

	for _, w := range plan.Warnings {
		warnings = append(warnings, w.Error())
	}

	// Size budgets for non-engine sections.
	unknownSizeDepth := sizeBound
	if deepMaxDepth >= 0 {
		unknownSizeDepth = deepMaxDepth - 1
		if unknownSizeDepth < 0 {
			unknownSizeDepth = 0
		}
		const unknownSizeDepthCap = 2
		if unknownSizeDepth > unknownSizeDepthCap {
			unknownSizeDepth = unknownSizeDepthCap
		}
	}

	// --- Phase: unverified (deep-ish under analysis root) ---
	opts.phase(PhaseUnverified)
	unverifiedFull, unverifiedWarnings := nameShapedUnverified(ctx, root, verifiedPaths, minSize, deepMaxDepth, observation)
	warnings = append(warnings, unverifiedWarnings...)
	if unverifiedFull.Entries == nil {
		unverifiedFull.Entries = make([]Entry, 0)
	}

	skip := make(map[string]bool, len(verifiedPaths)+len(hotspotsFull)+len(unverifiedFull.Entries))
	for p := range verifiedPaths {
		skip[p] = true
	}
	// Classified but wholly unmeasured candidates stay coverage-only; they must
	// not reappear as a fabricated unknown zero or be retried as explicit roots.
	for p := range observation.classified {
		skip[p] = true
	}
	for _, h := range hotspotsFull {
		skip[h.Path] = true
	}
	for _, u := range unverifiedFull.Entries {
		skip[u.Path] = true
	}

	// --- Phase: unknown (skipped under critical work budget) ---
	var unknownFull []Entry
	if skipUnknown {
		opts.phase(PhaseUnknown)
		unknownFull = make([]Entry, 0)
	} else {
		opts.phase(PhaseUnknown)
		var uw []string
		unknownFull, uw = topUnknown(ctx, root, skip, minSize, unknownSizeDepth, observation)
		if unknownFull == nil {
			unknownFull = make([]Entry, 0)
		}
		warnings = append(warnings, uw...)
	}
	if warnings == nil {
		warnings = make([]string, 0)
	}

	// --- Projection: class/domain on full row sets (never reclassify) ---
	// Skip maps above were built from unfiltered classification so hidden
	// verified rows cannot reappear as unverified/unknown.
	// Bound-state notes/warnings use these projected full sets (before --top),
	// never the unfiltered scan sets, so filtered output does not claim hidden rows.
	annotateVerifiedBoundState(&verifiedFull)
	verifiedProjected := ProjectVerified(verifiedFull, invFilters)
	verifiedProjected.PrunableHuman = engine.HumanSize(verifiedProjected.PrunableBytes)
	verifiedProjected.WithheldHuman = engine.HumanSize(verifiedProjected.WithheldBytes)
	hotspotsProjected := ProjectEntries(hotspotsFull, invFilters)
	unverifiedProjected := unverifiedFull
	unverifiedProjected.Entries = ProjectEntries(unverifiedFull.Entries, invFilters)
	// Recompute unverified aggregates from full filtered set.
	if invFilters.Active() {
		var ub int64
		var uinc bool
		for _, e := range unverifiedProjected.Entries {
			ub += e.SizeBytes
			if e.SizeIncomplete {
				uinc = true
			}
		}
		unverifiedProjected.Count = len(unverifiedProjected.Entries)
		unverifiedProjected.TotalBytes = ub
		unverifiedProjected.TotalHuman = engine.HumanSize(ub)
		unverifiedProjected.SizeIncomplete = uinc
	}
	unknownProjected := ProjectEntries(unknownFull, invFilters)
	recipesProjected := ProjectRecipes(recipes, invFilters)

	// Display-only truncation after projection.
	// Verified: --top applies per action section (prunable and withheld separately).
	// Bound helpers stay on the section from the full projected set (pre-top).
	verified := verifiedProjected
	verified.Candidates = truncateVerifiedDisplay(verifiedProjected.Candidates, opts.Top)
	// Preserve per-state incompleteness across truncation (derived pre-top).
	verified.SizeIncomplete = verifiedProjected.SizeIncomplete
	verified.PrunableSizeIncomplete = verifiedProjected.PrunableSizeIncomplete
	verified.WithheldSizeIncomplete = verifiedProjected.WithheldSizeIncomplete
	hotspots := truncateHotspotDisplay(hotspotsProjected, opts.Top)
	unverified := unverifiedProjected
	unverified.Entries = truncateHotspotDisplay(unverifiedProjected.Entries, opts.Top)
	// Section incompleteness reflects full projected set even if top drops rows.
	unverified.SizeIncomplete = unverifiedProjected.SizeIncomplete
	unknown := truncateHotspotDisplay(unknownProjected, opts.Top)
	recipes = recipesProjected

	// --- Phase: handoff from full filtered prunable set (independent of --top) ---
	opts.phase(PhaseHandoff)
	var additive config.SignatureCatalog
	if opts.Config != nil {
		additive = opts.Config.Signatures
	}
	handoffVerified := verifiedProjected
	if !ClassesIncludePrunable(invFilters) {
		// Class filter excludes prunable: never embed executable candidates.
		handoffVerified = VerifiedSection{Candidates: []Entry{}}
	}
	handoff := BuildPruneHandoff(HandoffInput{
		Verified:       handoffVerified,
		AnalysisRoot:   root,
		MinSizeDisplay: minSizeDisplay,
		SortMode:       sortMode,
		ReclaimScope:   reclaimScope,
		AdditiveSigs:   additive,
		ClassFilters:   invFilters.Classes,
		DomainFilters:  invFilters.Domains,
	})

	// --- Phase: structural journeys (evidence-driven; after handoff) ---
	// Cargo placement needs verified cargo-target rows + optional handoff argv.
	if !opts.DisableRecipes {
		var handoffForJourney *PruneHandoff
		if handoff.Present {
			h := handoff
			handoffForJourney = &h
		}
		if j, ok := BuildCargoPlacementJourney(CargoPlacementInput{
			Verified:         verifiedProjected,
			Pressure:         pressure,
			AnalysisPressure: analysisPressure,
			AnalysisRoot:     root,
			Enabled:          enabled,
			Handoff:          handoffForJourney,
		}); ok {
			if RecipeMatchesFilters(j, invFilters) {
				recipes = append(recipes, j)
			}
		}
	}

	notes := []string{
		"Observation sizes count regular-file apparent bytes with bounded inventory traversal; unmeasured candidates appear only in coverage warnings. Workers and remote/stall controls apply only to observation, not verified discovery or prune handoff.",
		"Remote exclusion applies only to recognized filesystems/placeholders on macOS/Linux; detection is not exhaustive. Windows and other platforms may walk network directories even without --include-remote. Stall timeout bounds directory opens, not all filesystem calls.",
		"space is read-only; it never deletes. Use prune with explicit execute only for prunable verified candidates.",
		"diagnostic-only hotspots are capacity loans: rebuild cost varies (see rebuild_expectation).",
		"Unverified mass is name-shaped only — not context-verified and not reclaimable via prune.",
		"pressure is the primary write/home volume; analysis_pressure is included when the analysis root is on a different volume.",
		"prune_handoff is the dry-run for prunable rows from this report; revalidate with prune --from-space-report <json>.",
		"recipes suggest external cleanup for diagnostic-only mass (never auto-executed); structural recipes (kind=structural) are multi-step print-only journeys.",
		"Verified candidates use sort_mode=" + sortMode + " (idle = complete cold activity first; incomplete/unknown last). Size ranking is bound-aware: complete measurements rank before incomplete lower bounds. Ranking is advisory only.",
		"last_activity_at is newest observed descendant mtime during sizing (symlink children not followed); incomplete activity ranks last, never falsely cold.",
		"reclaim_scope=" + reclaimScope + " (plan-wide; carried into exact_plan for replay).",
	}
	if reclaimScope == engine.ReclaimScopeIncremental {
		notes = append(notes, "reclaim_scope=incremental: only Cargo target/**/incremental caches are listed (not whole target/); mutually exclusive with whole-target mode.")
	}
	notes = append(notes, budget.Notes...)
	notes = append(notes, PlacementNotes(pressure, analysisPressure)...)
	if tempPlanes != nil {
		notes = append(notes, "temp_plane_coverage names OS temp roots this run did not walk (bounded size; observation only, no prune authority). Run the listed follow-up space command explicitly to inspect one.")
	}
	if pressure.Level == PressureCritical {
		notes = append(notes, "Primary write volume pressure is critical: prefer idle verified candidates first, then supervised cache cleanup outside spanwit for diagnostic-only rows.")
	}
	// Operator-facing reclaim lag note uses projected prunable (filter-visible) set.
	if verifiedProjected.PrunableBytes > 0 && pressure.AvailBytes > 0 {
		notes = append(notes, "After a prune execute, free space may lag plan totals (filesystem slack); re-check pressure.")
	}
	// Bound-state narratives from projected full sets only (before --top).
	if verifiedProjected.SizeIncomplete {
		notes = append(notes, "Some verified sizes are depth-bounded partial measurements (size_incomplete); reported byte totals are lower bounds, not exact reclaim amounts. Suggested follow-up: focused complete inventory or a deeper --max-depth sense pass — do not infer reclaim volume from incomplete lower bounds.")
		warnings = append(warnings, "verified reclaimable includes depth-bounded partial size measurements")
	}
	if unverifiedProjected.SizeIncomplete {
		notes = append(notes, "Unverified totals include partial measurements (size_incomplete); total_bytes is an observed lower bound. Partial diagnostic rows stay visible outside the complete-row --top budget. Suggested follow-up: focused complete inventory of those paths, not inferred reclaim.")
	}
	indeterminate := countMinSizeInconclusive(minSize, hotspotsProjected, unverifiedProjected.Entries, unknownProjected)
	if indeterminate > 0 {
		notes = append(notes, fmt.Sprintf("min_size threshold is indeterminate for %d partial diagnostic row(s) whose lower bound is below the floor; rows stay visible (not matched, not excluded as small).", indeterminate))
		warnings = append(warnings, fmt.Sprintf("min_size indeterminate for %d partial diagnostic rows", indeterminate))
	}

	var handoffPtr *PruneHandoff
	if handoff.Present || len(handoff.Notes) > 0 {
		h := handoff
		handoffPtr = &h
	}

	if invFilters.Active() {
		notes = append(notes, "Inventory filters are opt-in projection only (visibility + handoff membership); they never reclassify trust state.")
	}

	var enabledDomains *[]string
	if enabled.Configured {
		// Non-nil even when empty so explicit-empty marshals as [] not null.
		ld := append([]string{}, enabled.List...)
		enabledDomains = &ld
		if len(enabled.List) == 0 {
			notes = append(notes, "domains.enabled is empty: inventory is shown broadly, but no domain drives recipes or reclaim eligibility.")
		} else {
			notes = append(notes, "domains.enabled authorizes recipes/reclaim for: "+strings.Join(enabled.List, ", ")+" (discover-broad / authorize-narrow).")
		}
	}
	if domainDisabledCount > 0 {
		notes = append(notes, fmt.Sprintf("%d verified candidate(s) are withheld with reason domain_disabled: their use domain is not in domains.enabled. They stay visible but are excluded from the prune handoff; enable the domain to authorize reclaim.", domainDisabledCount))
	}

	mutationContract, err := normalizeMutationContract(opts.MutationContract)
	if err != nil {
		return Report{}, err
	}
	if mutationContract != "" {
		notes = append(notes, "mutation_contract="+mutationContract+" (invocation no-mutation assertion; does not replace dry-run/--execute semantics).")
	}
	return Report{
		Schema:            SchemaID,
		Version:           1,
		GeneratedAt:       now.UTC().Format(time.RFC3339),
		Root:              root,
		MinSize:           minSizeDisplay,
		IncludeHomeCaches: opts.IncludeHomeCaches,
		Top:               opts.Top,
		MaxDepth:          deepMaxDepth,
		SortMode:          sortMode,
		ReclaimScope:      reclaimScope,
		Pressure:          pressure,
		AnalysisPressure:  analysisPressure,
		Verified:          verified,
		Hotspots:          hotspots,
		Unverified:        unverified,
		Unknown:           unknown,
		PruneHandoff:      handoffPtr,
		Recipes:           recipes,
		AppliedFilters:    invFilters.ToApplied(),
		EnabledDomains:    enabledDomains,
		MutationContract:  mutationContract,
		TempPlaneCoverage: tempPlanes,
		Completion:        NewCompletion(warnings),
		Warnings:          warnings,
		Notes:             notes,
	}, nil
}

// normalizeMutationContract accepts empty (omit), open, or read_only.
func normalizeMutationContract(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case "", "open":
		if raw == "" {
			return "", nil
		}
		return "open", nil
	case "read_only":
		return "read_only", nil
	default:
		return "", fmt.Errorf("unsupported mutation_contract %q (use open|read_only)", raw)
	}
}

// MeasurePrimaryPressure returns primary write/home volume pressure for the
// analysis root without walking the tree.
func MeasurePrimaryPressure(analysisPath string) (Pressure, error) {
	if analysisPath == "" {
		analysisPath = "."
	}
	root, err := engine.CleanConfiguredPath(analysisPath)
	if err != nil {
		return Pressure{}, err
	}
	primaryPath := primaryWritePath(root)
	pressure, err := diskPressure(primaryPath)
	if err != nil {
		return Pressure{}, err
	}
	pressure.Role = PressureRolePrimaryWrite
	return pressure, nil
}

// countMinSizeInconclusive counts depth-bounded rows kept visible despite a
// measured lower bound below the opt-in min_size filter.
func countMinSizeInconclusive(minSize int64, sets ...[]Entry) int {
	if minSize <= 0 {
		return 0
	}
	n := 0
	for _, set := range sets {
		for _, e := range set {
			if e.SizeIncomplete && e.SizeBytes < minSize {
				n++
			}
		}
	}
	return n
}

func builtInSignatureTargets() []config.Target {
	var ids []string
	for domain, ecosystems := range config.BuiltInSignatures() {
		for ecosystem, signatures := range ecosystems {
			for name := range signatures {
				ids = append(ids, domain+"."+ecosystem+"."+name)
			}
		}
	}
	sort.Strings(ids)
	targets := make([]config.Target, 0, len(ids))
	for _, id := range ids {
		targets = append(targets, config.Target{Signature: id})
	}
	return targets
}
