package space

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/3leaps/spanwit/internal/catalog"
	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/engine"
)

// Recipe kind: argv recipes are single-shot external command suggestions;
// structural recipes are multi-step guided journeys (print-only).
const (
	RecipeKindArgv       = "argv"
	RecipeKindStructural = "structural"
)

// Recipe step kinds for structural journeys. None are auto-executed.
const (
	StepObserve       = "observe"
	StepCommand       = "command"
	StepConfigSnippet = "config_snippet"
	StepPurgeHint     = "purge_hint"
	StepVerify        = "verify"
)

// RecipeStep is one ordered step in a structural guided recipe.
// Print-only: spanwit never writes config, never moves trees, never deletes.
type RecipeStep struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Kind  string   `json:"kind"`
	Body  []string `json:"body,omitempty"`
	Paths []string `json:"paths,omitempty"`
	// Command is set for kind=command (or purge_hint when a revalidate argv exists).
	Command *SuggestedCommand `json:"command,omitempty"`
	// ConfigSnippet is set for kind=config_snippet — copy-paste only.
	ConfigSnippet *ConfigSnippet `json:"config_snippet,omitempty"`
}

// ConfigSnippet is a copy-paste config fragment (never written by spanwit).
type ConfigSnippet struct {
	// Label is a human file/role hint (e.g. ".envrc", "shell profile").
	Label   string `json:"label"`
	Content string `json:"content"`
}

// CargoPlacementInput supplies evidence for the Cargo structural journey.
type CargoPlacementInput struct {
	// Verified is the full projected verified set (pre --top).
	Verified VerifiedSection
	// Pressure is primary write/home volume pressure.
	Pressure Pressure
	// AnalysisPressure is set when the analysis root is on a different volume.
	AnalysisPressure *Pressure
	// AnalysisRoot is the space analysis root.
	AnalysisRoot string
	// Enabled is the authorize-narrow domain policy.
	Enabled config.EnabledDomains
	// Handoff provides prune revalidate suggestions when present.
	Handoff *PruneHandoff
	// MaxProjects caps listed project roots (display bound).
	MaxProjects int
}

// cargoTargetSignature is the built-in whole-tree Cargo target signature id.
const cargoTargetSignature = "development.rust.cargo-target"

// BuildCargoPlacementJourney returns a multi-step structural recipe for
// relocating Cargo build output off a tight volume, or ok=false when
// preconditions are not met. Never mutates disk or env.
func BuildCargoPlacementJourney(in CargoPlacementInput) (Recipe, bool) {
	if !recipeDomainAllowed(in.Enabled, "development") {
		return Recipe{}, false
	}

	projects := cargoProjectsFromVerified(in.Verified)
	if len(projects) == 0 {
		return Recipe{}, false
	}

	// Prefer warn/critical pressure, but still offer the journey when cargo
	// targets are already verified prunable under the analysis root (operator
	// may be planning ahead). Gate on cargo-prunable mass, not global
	// PrunableBytes (other signatures must not unlock a Cargo journey alone).
	pressureTight := in.Pressure.Level == PressureWarn || in.Pressure.Level == PressureCritical
	cargoPrunableBytes := cargoPrunableBytes(projects)
	if !pressureTight && cargoPrunableBytes == 0 {
		return Recipe{}, false
	}

	maxProjects := in.MaxProjects
	if maxProjects <= 0 {
		maxProjects = 12
	}
	listed := projects
	truncated := false
	if len(listed) > maxProjects {
		listed = listed[:maxProjects]
		truncated = true
	}

	// 016A: never emit concrete CARGO_TARGET_DIR from analysis-vs-primary alone.
	// Targets live under analysis_root; "roomier analysis volume" is not
	// source→destination separation. Always choose-destination until a later
	// slice carries explicit destination root + per-candidate filesystem identity.
	destRationale := cargoDestinationRationale(in.Pressure, in.AnalysisPressure)
	projectPaths := make([]string, 0, len(listed))
	for _, p := range listed {
		projectPaths = append(projectPaths, p.Root)
	}

	steps := make([]RecipeStep, 0, 6)

	// 1. Observe — evidence-backed project roots
	observeBody := []string{
		"These Cargo project roots have context-verified target/ trees under the analysis root.",
		"Each project should use its own CARGO_TARGET_DIR under a parent on a roomier volume — never one shared target dir for all crates by default (contention and stale caches).",
	}
	if truncated {
		observeBody = append(observeBody, fmt.Sprintf("Showing the first %d of %d project roots (use a tighter analysis root or filters for a focused list).", maxProjects, len(projects)))
	}
	if pressureTight {
		observeBody = append(observeBody, "Primary write volume pressure is "+in.Pressure.Level+". Relocating build output can reduce refill on the primary volume only when destination is on a different filesystem than each source target/ — not proven by this report alone.")
	}
	steps = append(steps, RecipeStep{
		ID:    "observe-projects",
		Title: "List implicated Cargo project roots",
		Kind:  StepObserve,
		Body:  observeBody,
		Paths: projectPaths,
	})

	// 2. Purge-before-relocate — Cargo-scoped exact_plan only (never report-wide
	// prune_handoff; revalidate via prune --from-exact-plan).
	purgeBody := []string{
		"Purge regenerable Cargo target/ trees at the old location before you relocate.",
		"Do not move fat target/ directories by hand as the primary path — reclaim with signature-backed prune, then rebuild into the new location.",
		"space never deletes. Review the Cargo-only path list, dry-run revalidate, and add --execute only after dry-run matches.",
		"Do not use prune --from-space-report for this step: that replays the report-wide prune_handoff and may include non-Cargo prunables.",
	}
	cargoHandoff := BuildPruneHandoff(HandoffInput{
		Verified:       cargoPrunableVerified(in.Verified),
		AnalysisRoot:   in.AnalysisRoot,
		MinSizeDisplay: handoffMinSize(in.Handoff),
		SortMode:       handoffSortMode(in.Handoff),
		ReclaimScope:   engine.ReclaimScopeWhole,
	})
	var purgeCmd *SuggestedCommand
	var recipeExact *ExactPlan
	if cargoHandoff.Present && cargoHandoff.ExactPlan != nil && len(cargoHandoff.ExactPlan.Candidates) > 0 {
		// Deep-copy plan pointer for the recipe (handoff owns the value).
		ep := *cargoHandoff.ExactPlan
		ep.Candidates = append([]ExactCandidate(nil), cargoHandoff.ExactPlan.Candidates...)
		recipeExact = &ep
		c := FormatCommand("spanwit", "prune", "--from-exact-plan", "<cargo-exact-plan.json>")
		purgeCmd = &c
		cargoPaths := make([]string, 0, len(cargoHandoff.Candidates))
		for _, e := range cargoHandoff.Candidates {
			cargoPaths = append(cargoPaths, e.Path)
		}
		purgeBody = append(purgeBody,
			fmt.Sprintf("Cargo-only exact_plan: %d prunable target/ candidate(s), %s (lower bounds if size_incomplete).",
				cargoHandoff.PrunableCount, cargoHandoff.PrunableHuman),
			"Write recipes[].exact_plan for this journey to a file (e.g. jq), then revalidate with the command below (dry-run by default). The plan contains only the Cargo paths listed on this step.",
		)
		steps = append(steps, RecipeStep{
			ID:      "purge-before-relocate",
			Title:   "Dry-run purge of verified Cargo target/ trees",
			Kind:    StepPurgeHint,
			Body:    purgeBody,
			Paths:   cargoPaths,
			Command: purgeCmd,
		})
	} else {
		c := FormatCommand("spanwit", "prune", "--help")
		purgeCmd = &c
		purgeBody = append(purgeBody,
			"No Cargo-prunable target/ plan in this view (targets may be withheld, filtered, or absent). Do not run a report-wide prune as a substitute — re-run space without class filters that hide prunable, or review withheld reasons first.",
		)
		steps = append(steps, RecipeStep{
			ID:      "purge-before-relocate",
			Title:   "Dry-run purge of verified Cargo target/ trees",
			Kind:    StepPurgeHint,
			Body:    purgeBody,
			Command: purgeCmd,
		})
	}

	// 3. Destination precondition only (016A bounded surface).
	steps = append(steps, RecipeStep{
		ID:    "choose-destination",
		Title: "Choose a destination volume (precondition)",
		Kind:  StepObserve,
		Body: []string{
			destRationale,
			"Spanwit does not emit a concrete CARGO_TARGET_DIR export in this slice: Cargo targets were discovered under the analysis root, so analysis-vs-primary free space is not source→destination separation.",
			"Pick a parent directory on a different, roomier volume yourself; use unique per-project subdirs (leaf + short hash of the full project root — never one shared target for all crates).",
			"A later slice may emit copy-paste exports only when destination root is explicit and each candidate source filesystem identity differs from the destination.",
		},
		Paths: projectPaths,
	})

	// 5. Verify after cutover
	verifyBody := []string{
		"After purge + env cutover (when destination is set), rebuild once so the new target dir is created on the destination volume.",
		"Re-run space (and optionally space --compare with saved JSON captures). Destination growth under active build is expected; success is which volume grows.",
		"Success is not zero growth — active work refills; the goal is refill off the critical primary volume when destination separation is real.",
	}
	verifyCmd := FormatCommand("spanwit", "space", in.AnalysisRoot)
	steps = append(steps, RecipeStep{
		ID:      "verify-after",
		Title:   "Observe after cutover",
		Kind:    StepVerify,
		Body:    verifyBody,
		Command: &verifyCmd,
	})

	// suggested_commands = ordered projection of step commands (structural authority is steps).
	suggested := projectCommandsFromSteps(steps)
	if len(suggested) == 0 && purgeCmd != nil {
		// Fallback if steps somehow lack commands (should not happen).
		suggested = []SuggestedCommand{*purgeCmd, verifyCmd}
	}

	r := Recipe{
		ID:                 catalog.RecipeCargoTargetPlacement,
		Title:              "Relocate Cargo build output (guided structural journey)",
		Kind:               RecipeKindStructural,
		State:              StateDiagnosticOnly,
		RebuildExpectation: RebuildHigh,
		SuggestedCommands:  suggested,
		Steps:              steps,
		ExactPlan:          recipeExact,
		Rationale: []string{
			"Multi-step structural recipe (016A): diagnose → Cargo-only exact_plan purge → choose destination (no concrete export this slice) → observe after.",
			"Print-only: spanwit never writes env/config, never moves trees, and never auto-executes prune.",
			"Purge revalidate uses prune --from-exact-plan on this recipe's exact_plan (Cargo paths only; same fail-closed plan identity as report-carried exact plans) — not prune --from-space-report.",
			"Not a substitute for observe-first daemon (016C); this is the CLI journey operators can follow without prior env-var knowledge.",
		},
		Domain:    "development",
		Ecosystem: "rust",
		CatalogID: "development.rust.cargo-target",
	}
	AnnotateRecipeTaxonomy(&r)
	return r, true
}

// projectCommandsFromSteps returns argv suggestions in step order (structural projection).
func projectCommandsFromSteps(steps []RecipeStep) []SuggestedCommand {
	out := make([]SuggestedCommand, 0)
	for _, s := range steps {
		if s.Command != nil {
			out = append(out, *s.Command)
		}
	}
	return out
}

type cargoProject struct {
	Root       string
	TargetPath string
	SizeBytes  int64
	Prunable   bool
}

func cargoPrunableBytes(projects []cargoProject) int64 {
	var n int64
	for _, p := range projects {
		if p.Prunable {
			n += p.SizeBytes
		}
	}
	return n
}

// cargoPrunableVerified returns a VerifiedSection limited to prunable whole
// cargo-target rows so journey purge hints never ride a report-wide handoff.
func cargoPrunableVerified(v VerifiedSection) VerifiedSection {
	out := VerifiedSection{Candidates: make([]Entry, 0)}
	for _, e := range v.Candidates {
		if e.Signature != cargoTargetSignature {
			continue
		}
		if e.State != StatePrunable {
			continue
		}
		if e.ReclaimScope == engine.ReclaimScopeIncremental {
			continue
		}
		if !strings.EqualFold(filepath.Base(e.Path), "target") {
			continue
		}
		out.Candidates = append(out.Candidates, e)
		out.PrunableCandidates++
		out.PrunableBytes += e.SizeBytes
		if e.SizeIncomplete {
			out.SizeIncomplete = true
			out.PrunableSizeIncomplete = true
		}
	}
	return out
}

func handoffMinSize(h *PruneHandoff) string {
	if h != nil && h.MinSize != "" {
		return h.MinSize
	}
	return "none"
}

func handoffSortMode(h *PruneHandoff) string {
	if h != nil && h.ExactPlan != nil && h.ExactPlan.SortMode != "" {
		return h.ExactPlan.SortMode
	}
	return engine.SortIdle
}

func cargoProjectsFromVerified(v VerifiedSection) []cargoProject {
	// Prefer prunable whole-target rows; include withheld for observe/planning
	// when pressure is tight (purge may still be blocked until filters allow).
	byRoot := map[string]*cargoProject{}
	for _, e := range v.Candidates {
		if e.Signature != cargoTargetSignature {
			continue
		}
		if e.State != StatePrunable && e.State != StateWithheld {
			continue
		}
		// Whole-tree target/ only for project-root inference (incremental is a subpath).
		if e.ReclaimScope == engine.ReclaimScopeIncremental {
			continue
		}
		root := filepath.Dir(filepath.Clean(e.Path))
		if root == "" || root == "." || root == string(filepath.Separator) {
			continue
		}
		// Confirm leaf is target (signature path should be .../target).
		if !strings.EqualFold(filepath.Base(e.Path), "target") {
			continue
		}
		prunable := e.State == StatePrunable
		cur, ok := byRoot[root]
		if !ok {
			byRoot[root] = &cargoProject{Root: root, TargetPath: e.Path, SizeBytes: e.SizeBytes, Prunable: prunable}
			continue
		}
		if e.SizeBytes > cur.SizeBytes {
			cur.TargetPath = e.Path
			cur.SizeBytes = e.SizeBytes
		}
		// Any prunable observation for this root marks it purgable in the journey gate.
		if prunable {
			cur.Prunable = true
		}
	}
	out := make([]cargoProject, 0, len(byRoot))
	for _, p := range byRoot {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SizeBytes != out[j].SizeBytes {
			return out[i].SizeBytes > out[j].SizeBytes
		}
		return out[i].Root < out[j].Root
	})
	return out
}

// cargoDestinationRationale explains why 016A stops at choose-destination.
// Analysis-vs-primary volume comparison is never treated as candidate
// source→destination evidence (targets live under analysis_root).
func cargoDestinationRationale(pressure Pressure, analysis *Pressure) string {
	if analysis != nil && analysis.VolumeID != "" && pressure.VolumeID != "" &&
		analysis.VolumeID != pressure.VolumeID {
		return "Primary and analysis volumes differ, but discovered Cargo target/ trees sit under the analysis root — proposing a parent under that same root would not move mass off the source filesystem. Choose a destination on another volume yourself."
	}
	if pressure.Level == PressureWarn || pressure.Level == PressureCritical {
		return "Primary volume pressure is " + pressure.Level + ", but this report does not prove a roomier destination filesystem for the listed targets. Choose a parent on a different, roomier volume yourself."
	}
	return "No destination-volume claim is made from this report. Choose an explicit destination root on a roomier volume before setting per-project CARGO_TARGET_DIR."
}

func safeProjectLeaf(projectRoot string) string {
	base := filepath.Base(filepath.Clean(projectRoot))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "project"
	}
	// Keep path-safe single segment.
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, base)
	if base == "" || base == "." || base == ".." {
		return "project"
	}
	return base
}

// uniqueProjectDestName returns a path-safe subdirectory name that is unique
// for a project root even when basenames collide (e.g. work/a/app vs work/b/app).
// Format: <safe-leaf>-<8 hex chars of sha256(clean root)>.
func uniqueProjectDestName(projectRoot string) string {
	leaf := safeProjectLeaf(projectRoot)
	sum := sha256.Sum256([]byte(filepath.Clean(projectRoot)))
	return fmt.Sprintf("%s-%x", leaf, sum[:4])
}
