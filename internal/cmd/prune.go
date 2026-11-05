package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/spf13/cobra"

	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/space"
)

// newPruneCmd creates the prune command.
func newPruneCmd(identity *appidentity.Identity) *cobra.Command {
	var (
		execute         bool
		outputFormat    string
		pruneConfigPath string
		allowlist       []string
		fromSpaceReport string
		fromExactPlan   string
		sortMode        string
		reclaimScope    string
		minAge          string
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete reclaimable directories (dry-run by default)",
		Long: `Delete directories identified as reclaimable according to the config.

This command is dry-run by default. You must explicitly pass --execute to perform deletion.

Modes:
  • config (default): discover via config file path profiles
  • --from-space-report FILE: revalidate prune_handoff.exact_plan from a space
    JSON report (preserves signature + filter provenance). Supports --execute
    only for candidates that remain prunable under that plan.
  • --from-exact-plan FILE: revalidate a standalone exact_plan JSON document
    (e.g. recipes[].exact_plan from a structural journey — Cargo-scoped purge).
    Supports --execute for candidates that remain prunable under that plan.
  • --allowlist PATH (repeatable): bare-path dry-run convenience against
    built-in signatures only. --execute is refused (no policy provenance).

Ranking: --sort size (default) or idle (cold complete activity first; advisory).
Granularity: --reclaim-scope whole (default) or incremental (Cargo
target/**/incremental only; mutually exclusive with whole-target for a tree).

Freshness (--from-space-report / --from-exact-plan):
  • Every candidate must be at least 1h idle at revalidation, even when the
    plan carries min_age none. --min-age DUR raises that floor (never lowers
    a carried, signature or temp floor). --min-age none drops only the 1h
    handoff floor for this run.
  • Each row is compared with the plan it came from. Activity newer than the
    plan's last_activity_at, or any change in a complete size, withholds the
    row (activity_after_plan / size_drift). Re-scan to review the new state.
  • Limits: plan baselines are not authenticated (an edited plan can hide
    drift, though never lower a floor). Newest mtime is an activity signal,
    not proof that nothing changed (the candidate directory's own mtime is
    not read), and revalidation is not a lock held through deletion. A tree
    deeper than the sizing walk has unknown drift: only the age floor guards
    it, so --min-age none leaves it unguarded.
  • --config / -c is refused in these modes: policy comes from the plan.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if outputFormat != "text" && outputFormat != "json" {
				return fmt.Errorf("unsupported output format %q (expected text or json)", outputFormat)
			}
			if sortMode != "size" && sortMode != "idle" {
				return fmt.Errorf("unsupported --sort %q (expected size or idle)", sortMode)
			}
			if reclaimScope != "whole" && reclaimScope != "incremental" {
				return fmt.Errorf("unsupported --reclaim-scope %q (expected whole or incremental)", reclaimScope)
			}
			modeCount := 0
			if fromSpaceReport != "" {
				modeCount++
			}
			if fromExactPlan != "" {
				modeCount++
			}
			if len(allowlist) > 0 {
				modeCount++
			}
			if modeCount > 1 {
				return fmt.Errorf("use only one of --from-space-report, --from-exact-plan, or --allowlist")
			}
			// Mode-specific flag contracts: never silently ignore requested granularity.
			if fromSpaceReport != "" && cmd.Flags().Changed("reclaim-scope") {
				return fmt.Errorf("--reclaim-scope is not valid with --from-space-report (reclaim_scope is carried by exact_plan)")
			}
			if fromExactPlan != "" && cmd.Flags().Changed("reclaim-scope") {
				return fmt.Errorf("--reclaim-scope is not valid with --from-exact-plan (reclaim_scope is carried by exact_plan)")
			}
			if len(allowlist) > 0 && cmd.Flags().Changed("reclaim-scope") {
				return fmt.Errorf("--reclaim-scope is not supported with --allowlist (use config discovery or --from-space-report)")
			}
			replay := fromSpaceReport != "" || fromExactPlan != ""
			if cmd.Flags().Changed("min-age") {
				if !replay {
					return fmt.Errorf("--min-age is only valid with --from-space-report or --from-exact-plan (config mode takes min_age from the config)")
				}
				if err := engine.ValidateRequestedMinAge(minAge); err != nil {
					return err
				}
			}
			if replay {
				// Replay policy is carried by the plan. An explicitly supplied
				// config would be silently ignored, so refuse it.
				if cmd.Flags().Changed("config") || cmd.InheritedFlags().Changed("config") {
					return fmt.Errorf("--config/-c is not valid with --from-space-report or --from-exact-plan (policy is carried by the plan; use --min-age to raise the age floor)")
				}
				if identity != nil {
					if env := identity.EnvVar("CONFIG_PATH"); os.Getenv(env) != "" {
						_, _ = fmt.Fprintf(os.Stderr, "Note: %s is set; config not used in replay mode (policy is carried by the plan).\n", env)
					}
				}
			}

			var (
				plan       PrunePlan
				loadedPath string
				err        error
			)

			planOpts := []engine.PlanOptions{
				engine.WithSortMode(sortMode),
				engine.WithReclaimScope(reclaimScope),
			}

			switch {
			case fromSpaceReport != "":
				loadedPath = "from-space-report:" + fromSpaceReport
				// Scope is carrier-owned. Sort uses carrier policy unless --sort was set.
				var sortOpts []engine.PlanOptions
				if cmd.Flags().Changed("sort") {
					sortOpts = append(sortOpts, engine.WithSortMode(sortMode))
				}
				plan, err = buildPrunePlanFromSpaceReport(context.Background(), fromSpaceReport, minAge, sortOpts...)
				if err != nil {
					return err
				}
			case fromExactPlan != "":
				loadedPath = "from-exact-plan:" + fromExactPlan
				var sortOpts []engine.PlanOptions
				if cmd.Flags().Changed("sort") {
					sortOpts = append(sortOpts, engine.WithSortMode(sortMode))
				}
				plan, err = buildPrunePlanFromExactPlanFile(context.Background(), fromExactPlan, minAge, sortOpts...)
				if err != nil {
					return err
				}
			case len(allowlist) > 0:
				if execute {
					return fmt.Errorf("bare --allowlist is dry-run only (no signature/filter provenance); use --from-space-report <space.json> for execute")
				}
				loadedPath = "allowlist:" + strings.Join(allowlist, ",")
				plan, err = buildPrunePlanFromAllowlist(context.Background(), allowlist, engine.WithSortMode(sortMode))
				if err != nil {
					return err
				}
			default:
				selectedConfig := configPath
				if pruneConfigPath != "" {
					selectedConfig = pruneConfigPath
				}
				var cfg *config.Config
				cfg, loadedPath, err = config.LoadConfig(context.Background(), identity, loggerInstance, selectedConfig)
				if err != nil {
					return err
				}
				plan, err = buildPrunePlan(context.Background(), cfg, planOpts...)
				if err != nil {
					return err
				}
			}

			_, _ = fmt.Fprintf(os.Stderr, "=== spanwit prune ===\n")
			_, _ = fmt.Fprintf(os.Stderr, "Config: %s\n", loadedPath)
			_, _ = fmt.Fprintf(os.Stderr, "Mode:   %s\n\n", map[bool]string{true: "EXECUTE (will delete)", false: "DRY RUN (no changes will be made)"}[execute])

			if outputFormat == "json" {
				var executeErr error
				if execute {
					executeErr = executePrunePlan(context.Background(), &plan)
				}
				if err := writePrunePlanJSON(os.Stdout, plan, loadedPath, execute, time.Now()); err != nil {
					return err
				}
				for _, warning := range plan.Warnings {
					_, _ = fmt.Fprintf(os.Stderr, "Warning: %v\n", warning)
				}
				return executeErr
			}

			writePlanText(os.Stdout, plan)

			for _, warning := range plan.Warnings {
				_, _ = fmt.Fprintf(os.Stderr, "Warning: %v\n", warning)
			}

			if !execute {
				_, _ = fmt.Fprintf(os.Stderr, "This is a DRY RUN. No files or directories will be deleted.\n")
				_, _ = fmt.Fprintf(os.Stderr, "Add --execute when you are ready to perform deletion.\n\n")
				return nil
			}

			if err := executePrunePlan(context.Background(), &plan); err != nil {
				for _, warning := range plan.Warnings {
					_, _ = fmt.Fprintf(os.Stderr, "Warning: %v\n", warning)
				}
				return err
			}
			var deletedBytes int64
			var deletedCandidates int
			for _, candidate := range plan.Candidates {
				if candidate.Deleted {
					deletedCandidates++
					deletedBytes += candidate.Size
				}
			}
			_, _ = fmt.Fprintf(os.Stderr, "Deleted %d candidates, reclaimed %s.\n", deletedCandidates, humanSize(deletedBytes))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&execute, "execute", "e", false, "actually perform deletion (default is dry-run)")
	cmd.Flags().StringVarP(&pruneConfigPath, "config", "c", "", "config file path override")
	cmd.Flags().StringVar(&outputFormat, "format", "text", "output format: text or json")
	cmd.Flags().StringArrayVar(&allowlist, "allowlist", nil, "exact path dry-run helper (repeatable; built-in signatures only; no --execute)")
	cmd.Flags().StringVar(&fromSpaceReport, "from-space-report", "", "revalidate prune_handoff.exact_plan from a space --format json report")
	cmd.Flags().StringVar(&fromExactPlan, "from-exact-plan", "", "revalidate a standalone exact_plan JSON (e.g. structural recipe recipes[].exact_plan)")
	cmd.Flags().StringVar(&sortMode, "sort", "size", "candidate ranking: size|idle (advisory; idle = cold complete activity first)")
	cmd.Flags().StringVar(&reclaimScope, "reclaim-scope", "whole", "reclaim granularity: whole|incremental")
	cmd.Flags().StringVar(&minAge, "min-age", "", "replay modes only: raise every candidate's min_age floor (e.g. 48h, 7d), or none to drop only the 1h handoff floor")

	return cmd
}

// buildPrunePlanFromExactPlanFile loads a standalone exact_plan JSON document
// (same shape as prune_handoff.exact_plan / recipes[].exact_plan) through the
// same fail-closed validator as report-carried plans.
func buildPrunePlanFromExactPlanFile(ctx context.Context, path, minAge string, opts ...engine.PlanOptions) (PrunePlan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PrunePlan{}, fmt.Errorf("read exact plan: %w", err)
	}
	ep, err := space.ParseAndValidateExactPlanJSON(raw)
	if err != nil {
		return PrunePlan{}, err
	}
	in := space.ExactPlanToEngine(&ep)
	in.RequestedMinAge = minAge
	// Sort is plan-carried; explicit CLI --sort may override via opts.
	merged := append([]engine.PlanOptions{engine.WithSortMode(ep.SortMode)}, opts...)
	return buildPrunePlanFromExact(ctx, in, merged...)
}

func candidateLabel(candidate PruneCandidate) string {
	if candidate.Signature != "" {
		return candidate.Signature
	}
	return candidate.Pattern
}

func buildPrunePlanFromSpaceReport(ctx context.Context, path, minAge string, opts ...engine.PlanOptions) (PrunePlan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PrunePlan{}, fmt.Errorf("read space report: %w", err)
	}
	report, err := space.LoadSpaceReportJSON(raw)
	if err != nil {
		return PrunePlan{}, err
	}
	if report.PruneHandoff == nil || !report.PruneHandoff.Present {
		// Zero-candidate carrier replay: still schema-valid and self-describing.
		return emptyPlanFromSpaceReport(report, minAge, opts...), nil
	}
	h := report.PruneHandoff
	if h.ExactPlan == nil && len(h.TempExactPlans) == 0 {
		return PrunePlan{}, fmt.Errorf("space report has no prune_handoff exact plan")
	}
	// Carrier sort (report-validated parity). Explicit CLI --sort (passed in
	// opts) overrides because ranking is advisory only.
	carrierSort := report.SortMode
	if h.ExactPlan != nil && h.ExactPlan.SortMode != "" {
		carrierSort = h.ExactPlan.SortMode
	}
	if carrierSort == "" {
		carrierSort = engine.SortIdle
	}
	merged := append([]engine.PlanOptions{engine.WithSortMode(carrierSort)}, opts...)

	var out PrunePlan
	have := false
	add := func(p PrunePlan) {
		if !have {
			out = p
			have = true
			return
		}
		out.Candidates = append(out.Candidates, p.Candidates...)
		out.TotalSize += p.TotalSize
		out.SizeIncomplete = out.SizeIncomplete || p.SizeIncomplete
		out.Warnings = append(out.Warnings, p.Warnings...)
		if out.Freshness == nil {
			out.Freshness = p.Freshness
		} else {
			out.Freshness.Merge(p.Freshness)
		}
	}
	if h.ExactPlan != nil {
		in := space.ExactPlanToEngine(h.ExactPlan)
		in.RequestedMinAge = minAge
		p, err := buildPrunePlanFromExact(ctx, in, merged...)
		if err != nil {
			return PrunePlan{}, err
		}
		add(p)
	}
	// Each temp plan is revalidated under its own root, and only after that
	// root is re-admitted now: ownership and mode may have changed since the
	// report was written, so admission is never taken from the report.
	for i := range h.TempExactPlans {
		tp := &h.TempExactPlans[i]
		adm := engine.ReadmitCarriedTempRoot(tp.AnalysisRoot, report.Root)
		if !adm.Admitted || adm.Root != tp.AnalysisRoot {
			reason := adm.Reason
			if reason == "" {
				reason = "root changed on canonicalization"
			}
			dropped := PrunePlan{Warnings: []error{fmt.Errorf(
				"temp root %s no longer admitted (%s); its %d candidate(s) are not included", tp.AnalysisRoot, reason, len(tp.Candidates))}}
			add(dropped)
			continue
		}
		in := space.ExactPlanToEngine(tp)
		in.TempRoot = true
		in.RequestedMinAge = minAge
		p, err := buildPrunePlanFromExact(ctx, in, merged...)
		if err != nil {
			return PrunePlan{}, err
		}
		add(p)
	}
	// A dropped temp root contributes no revalidation; still report policy.
	if out.Freshness == nil {
		out.Freshness = engine.NewFreshness(minAge)
	}
	return out, nil
}

// emptyPlanFromSpaceReport builds a schema-valid empty prune plan that retains
// the SpaceReport carrier policy (sort_mode / reclaim_scope) for zero-candidate
// replay. Explicit CLI sort in opts overrides carrier ranking.
func emptyPlanFromSpaceReport(report space.Report, minAge string, opts ...engine.PlanOptions) PrunePlan {
	sortMode := report.SortMode
	if sortMode == "" {
		sortMode = engine.SortIdle
	}
	scope := report.ReclaimScope
	if scope == "" {
		scope = engine.ReclaimScopeWhole
	}
	merged := engine.MergePlanOptions(append([]engine.PlanOptions{engine.WithSortMode(sortMode)}, opts...)...)
	if merged.SortMode != "" {
		sortMode = merged.SortMode
	}
	return PrunePlan{
		Candidates:   []PruneCandidate{},
		SortMode:     sortMode,
		ReclaimScope: scope,
		EffectiveFilters: EffectiveFilters{
			MinSize: filterDisplayNone,
			MinAge:  filterDisplayNone,
			MaxAge:  filterDisplayNone,
		},
		Freshness: engine.NewFreshness(minAge),
	}
}
