package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/foundry"
	"github.com/spf13/cobra"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/inventory"
	"github.com/3leaps/spanwit/internal/space"
)

var spaceAnalyze = space.Analyze

func newSpaceCmd(identity *appidentity.Identity) *cobra.Command {
	var (
		minSizeStr        string
		configOverride    string
		format            string
		top               int
		maxDepth          int
		includeHomeCaches bool
		includeRecipes    bool
		sortMode          string
		reclaimScope      string
		classFilters      []string
		domainFilters     []string
		capacityOnly      bool
		heldOpen          bool
		disclose          string
		sessionBoundary   string
		compare           bool
		workers           string
		backend           string
		includeRemote     bool
		stallTimeout      time.Duration
	)

	cmd := &cobra.Command{
		Use:   "space [path] | space --compare LEFT RIGHT",
		Short: "Read-only disk diagnostic: pressure, reclaimable, and hotspots",
		Long: `Answer "why is this disk full right now?" without deleting anything.

Default completed report order (actionability triage):
  1. Filesystem pressure on the primary write/home volume
  2. Can free now — verified prunable (idle-first by default) + prune handoff
  3. Withheld — visible filters / safe_to_prune trust model
  4. Named caches — diagnostic-only hotspots + guided recipes
  5. Name-shaped unverified mass
  6. Top unclassified directories
  7. Placement notes (when pressure is warn/critical)

Text mode streams a provisional early-triage block (pressure + hotspots) before
the deep verified walk; the completed report uses the actionability order above.

Opt-in inventory filters (omit = broad inventory; no silent narrowing):
  --class   repeatable; ADR-0004 states: prunable|withheld|diagnostic-only|unverified|unknown
  --domain  repeatable; segment-aware taxonomy match on signature/catalog_id
            (e.g. development, development.rust). No path/label guessing.
  OR within one flag type; AND across --class and --domain.

space is stronger than dry-run: there is no --execute path at all.
Prune handoff lists only prunable rows from this report (after filters).
Revalidate with ` + "`spanwit prune --from-space-report <json>`" + ` before any deletion.
Recipes never auto-delete.

--min-size is opt-in (no silent size/age defaults).
--max-depth bounds discovery under the analysis path (default 12).
Observation sizing and discovery use the bounded inventory walker (default 4
workers); --workers/--backend tune those phases only. Sizes retain depth bounds.
Recognized automatic remote/cloud roots are skipped unless --include-remote is set.
Remote detection is available on macOS/Linux only; Windows and other platforms
may walk network directories even without --include-remote. Detection is not
exhaustive, and the stall timeout bounds directory opens, not all filesystem calls.
--stall-timeout bounds observation directory opens (default 60s; 0 waits forever).
Unmeasured candidates appear as coverage warnings, never zero-sized rows.
These controls do not bound verified signature discovery or change prune handoff.
--sort idle|size ranks verified candidates (default idle; advisory only).
--reclaim-scope whole|incremental (default whole; incremental = Cargo **/incremental only).
--format json emits a schema-validated SpaceReport (future API body).
--capacity-only --held-open adds bounded Darwin evidence for unlinked files
that remain open; no held-open path component is emitted by default.
--compare LEFT RIGHT validates two explicit SpaceReport v2 captures and reports
only compatible right-minus-left plane deltas (one input may be - for stdin).

Capacity ritual:
  space ~/dev --class prunable --sort idle
  space ~/dev --class diagnostic-only --domain development
  space → review prune_handoff → prune --from-space-report dry-run → optional --execute

Placement: when pressure is warn/critical, notes suggest project-local
CARGO_TARGET_DIR on a roomier volume (advice only; never auto-relocates).
`,
		Args: func(cmd *cobra.Command, args []string) error {
			if compare {
				if len(args) != 2 {
					return fmt.Errorf("--compare requires exactly two SpaceReport v2 files (use - for stdin)")
				}
				return nil
			}
			return cobra.MaximumNArgs(1)(cmd, args)
		},
		Run: func(cmd *cobra.Command, args []string) {
			path := "."
			if len(args) > 0 && !compare {
				path = args[0]
			}
			var comparePaths []string
			if compare {
				comparePaths = append([]string(nil), args...)
			}
			os.Exit(runSpace(identity, os.Stdout, os.Stderr, spaceRunOpts{
				path:    path,
				workers: workers, backend: backend, includeRemote: includeRemote, stallTimeout: stallTimeout,
				minSize:           minSizeStr,
				configOverride:    configOverride,
				format:            format,
				top:               top,
				maxDepth:          maxDepth,
				includeHomeCaches: includeHomeCaches,
				includeRecipes:    includeRecipes,
				sortMode:          sortMode,
				reclaimScope:      reclaimScope,
				classFilters:      classFilters,
				domainFilters:     domainFilters,
				capacityOnly:      capacityOnly,
				heldOpen:          heldOpen,
				disclose:          disclose,
				disclosureChanged: cmd.Flags().Changed("disclose"),
				sessionBoundary:   sessionBoundary,
				comparePaths:      comparePaths,
				compareStdin:      os.Stdin,
				changedCompareFlags: changedFlags(cmd,
					"min-size", "config", "top", "max-depth", "include-home-caches",
					"recipes", "sort", "reclaim-scope", "class", "domain",
					"capacity-only", "held-open", "disclose", "session-boundary",
					"workers", "backend", "include-remote", "stall-timeout"),
				changedInventoryFlags: changedFlags(cmd,
					"min-size", "config", "top", "max-depth", "include-home-caches",
					"recipes", "sort", "reclaim-scope", "class", "domain",
					"workers", "backend", "include-remote", "stall-timeout"),
			}))
		},
	}

	cmd.Flags().StringVarP(&minSizeStr, "min-size", "m", "", "minimum size to list (default: no filter)")
	cmd.Flags().StringVarP(&configOverride, "config", "c", "", "add signatures/targets from this config on top of built-ins")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text|json")
	cmd.Flags().IntVar(&top, "top", 20, "max rows listed per multi-entry section (display only)")
	cmd.Flags().IntVar(&maxDepth, "max-depth", space.DefaultMaxDepth, "max directory depth for discovery under the analysis path")
	cmd.Flags().StringVar(&workers, "workers", "auto", "observation walker workers: auto (4) or a positive integer")
	cmd.Flags().StringVar(&backend, "backend", inventory.BackendAuto, "observation walker backend: auto|serial|parallel")
	cmd.Flags().BoolVar(&includeRemote, "include-remote", false, "include recognized remote/cloud directories in observation walks (detection: macOS/Linux only; may download files or block)")
	cmd.Flags().DurationVar(&stallTimeout, "stall-timeout", inventory.DefaultStallTimeout, "observation directory-open timeout (0 waits forever)")
	cmd.Flags().BoolVar(&includeHomeCaches, "include-home-caches", true, "include known home cache/toolchain hotspots")
	cmd.Flags().BoolVar(&includeRecipes, "recipes", true, "include guided recipes (home-cache argv recipes need --include-home-caches; structural journeys e.g. cargo-target-placement need verified evidence)")
	cmd.Flags().StringVar(&sortMode, "sort", "idle", "verified candidate ranking: idle|size (advisory; idle = cold complete activity first)")
	cmd.Flags().StringVar(&reclaimScope, "reclaim-scope", "whole", "verified reclaim granularity: whole|incremental")
	// StringArray (not StringSlice): repeatable flags without silent CSV packing.
	cmd.Flags().StringArrayVar(&classFilters, "class", nil, "opt-in filter by trust state (repeatable; OR): prunable|withheld|diagnostic-only|unverified|unknown")
	cmd.Flags().StringArrayVar(&domainFilters, "domain", nil, "opt-in filter by taxonomy segment prefix (repeatable; OR): e.g. development, development.rust")
	cmd.Flags().BoolVar(&capacityOnly, "capacity-only", false, "capture filesystem and platform capacity planes without deep inventory")
	cmd.Flags().BoolVar(&heldOpen, "held-open", false, "include slower held-open deleted-file evidence (read-only; never stops processes)")
	cmd.Flags().StringVar(&disclose, "disclose", "none", "held-open path disclosure: none|paths")
	cmd.Flags().StringVar(&sessionBoundary, "session-boundary", "", "capture label: before-running|before-quiesced|after-reboot|after-update")
	cmd.Flags().BoolVar(&compare, "compare", false, "compare two explicit SpaceReport v2 captures; use - for at most one stdin input")

	return cmd
}

type spaceRunOpts struct {
	workers               string
	backend               string
	includeRemote         bool
	stallTimeout          time.Duration
	path                  string
	minSize               string
	configOverride        string
	format                string
	top                   int
	maxDepth              int
	includeHomeCaches     bool
	includeRecipes        bool
	sortMode              string
	reclaimScope          string
	classFilters          []string
	domainFilters         []string
	capacityOnly          bool
	heldOpen              bool
	disclose              string
	disclosureChanged     bool
	sessionBoundary       string
	comparePaths          []string
	compareStdin          io.Reader
	compareNow            func() time.Time
	changedCompareFlags   []string
	changedInventoryFlags []string
	capacityCollector     *capacity.Collector
}

func runSpace(identity *appidentity.Identity, stdout, stderr io.Writer, opts spaceRunOpts) int {
	switch opts.format {
	case "text", "json":
	default:
		_, _ = fmt.Fprintf(stderr, "Error: unsupported --format %q (use text|json)\n", opts.format)
		return 3
	}

	if len(opts.comparePaths) > 0 {
		if len(opts.changedCompareFlags) > 0 {
			_, _ = fmt.Fprintf(stderr, "Error: --compare does not accept capture or inventory flags: %s\n",
				strings.Join(opts.changedCompareFlags, ", "))
			return 3
		}
		return runSpaceCompare(stdout, stderr, opts)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if opts.heldOpen && !opts.capacityOnly {
		_, _ = fmt.Fprintln(stderr, "Error: --held-open currently requires --capacity-only")
		return 3
	}
	if opts.disclosureChanged && !opts.heldOpen {
		_, _ = fmt.Fprintln(stderr, "Error: --disclose requires --held-open")
		return 3
	}
	disclosure := "none"
	switch opts.disclose {
	case "", "none":
	case "paths":
		disclosure = "full"
	default:
		_, _ = fmt.Fprintf(stderr, "Error: unsupported --disclose %q (use none|paths)\n", opts.disclose)
		return 3
	}
	if opts.sessionBoundary != "" && !opts.capacityOnly {
		_, _ = fmt.Fprintln(stderr, "Error: --session-boundary currently requires --capacity-only")
		return 3
	}
	switch opts.sessionBoundary {
	case "", "before-running", "before-quiesced", "after-reboot", "after-update":
	default:
		_, _ = fmt.Fprintf(stderr, "Error: unsupported --session-boundary %q\n", opts.sessionBoundary)
		return 3
	}

	if opts.capacityOnly {
		if len(opts.changedInventoryFlags) > 0 {
			_, _ = fmt.Fprintf(stderr, "Error: --capacity-only does not accept inventory flags: %s\n",
				strings.Join(opts.changedInventoryFlags, ", "))
			return 3
		}
		collector := capacity.Collector{}
		if opts.capacityCollector != nil {
			collector = *opts.capacityCollector
		}
		report, err := space.AnalyzeCapacityOnly(ctx, space.CapacityOptions{
			Path:      opts.path,
			Collector: collector,
			Tool: capacity.ToolIdentity{
				Name:    identity.BinaryName,
				Version: version,
				Commit:  knownCommit(commit),
			},
			HeldOpen:         opts.heldOpen,
			Disclosure:       disclosure,
			SessionBoundary:  opts.sessionBoundary,
			MutationContract: ResolvedMutationContract(),
		})
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return 2
		}
		if opts.format == "json" {
			if err := writeCapacityJSON(stdout, report); err != nil {
				_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
				return 2
			}
		} else {
			writeCapacityText(stdout, report)
		}
		return foundry.ExitSuccess
	}

	switch opts.sortMode {
	case "", "idle", "size":
	default:
		_, _ = fmt.Fprintf(stderr, "Error: unsupported --sort %q (use idle|size)\n", opts.sortMode)
		return 3
	}
	switch opts.reclaimScope {
	case "", "whole", "incremental":
	default:
		_, _ = fmt.Fprintf(stderr, "Error: unsupported --reclaim-scope %q (use whole|incremental)\n", opts.reclaimScope)
		return 3
	}

	// Validate inventory filters before filesystem analysis (usage errors → exit 3).
	if _, err := space.NormalizeInventoryFilters(opts.classFilters, opts.domainFilters); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 3
	}

	workerCount, err := parseInventoryWorkers(opts.workers)
	if err != nil {
		return inventoryUsage(stderr, "%v", err)
	}
	switch opts.backend {
	case "", inventory.BackendAuto, inventory.BackendParallel:
	case inventory.BackendSerial:
		if workerCount != 0 && workerCount != 1 {
			return inventoryUsage(stderr, "serial backend requires --workers 1 or auto")
		}
	default:
		return inventoryUsage(stderr, "unsupported --backend %q (use auto|serial|parallel)", opts.backend)
	}
	if opts.stallTimeout < 0 {
		return inventoryUsage(stderr, "--stall-timeout cannot be negative (use 0 to wait forever)")
	}
	analyzeOpts := space.Options{
		Workers: workerCount, Backend: opts.backend, IncludeRemote: opts.includeRemote,
		StallTimeout: walkStallTimeout(opts.stallTimeout), OnStall: newStallAlerter(stderr),
		Path:              opts.path,
		MinSize:           opts.minSize,
		Top:               opts.top,
		MaxDepth:          opts.maxDepth,
		IncludeHomeCaches: opts.includeHomeCaches,
		DisableRecipes:    !opts.includeRecipes,
		SortMode:          opts.sortMode,
		ReclaimScope:      opts.reclaimScope,
		ClassFilters:      opts.classFilters,
		DomainFilters:     opts.domainFilters,
		MutationContract:  ResolvedMutationContract(),
	}
	if opts.configOverride != "" {
		userCfg, _, err := config.LoadConfig(ctx, identity, loggerInstance, opts.configOverride)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return 2
		}
		analyzeOpts.Config = userCfg
	}

	_, _ = fmt.Fprintf(stderr, "Diagnosing space under: %s\n", opts.path)
	noticeRemoteRoots(stderr, []string{opts.path})
	resolvedWorkers := workerCount
	if resolvedWorkers == 0 {
		resolvedWorkers = 4
	}
	resolvedBackend := opts.backend
	if resolvedBackend == "" || resolvedBackend == inventory.BackendAuto {
		resolvedBackend = inventory.BackendParallel
		if resolvedWorkers == 1 {
			resolvedBackend = inventory.BackendSerial
		}
	}
	if resolvedBackend == inventory.BackendSerial {
		resolvedWorkers = 1
	}
	_, _ = fmt.Fprintf(stderr, "Observation walker: backend=%s workers=%d (depth-bounded; verified phase unchanged)\n",
		resolvedBackend, resolvedWorkers)

	earlyPrinted := false
	if opts.format == "text" {
		analyzeOpts.OnPhase = func(phase string) {
			if phase == space.PhaseVerified {
				_, _ = fmt.Fprintf(stderr, "Scanning analysis root for verified reclaimable…\n")
			}
		}
		// Stream pressure + projected hotspots as soon as OnPartial fires.
		analyzeOpts.OnPartial = func(u space.PartialUpdate) {
			if u.Phase != space.PhaseHotspots || u.Pressure == nil {
				return
			}
			writePressureBlock(stdout, "Primary write volume pressure (early triage)", *u.Pressure)
			writeTempPlanesText(stdout, u.TempPlanes)
			_, _ = fmt.Fprintln(stdout, "Known cache/toolchain hotspots (early — deep root scan continues):")
			_, _ = fmt.Fprintln(stdout, "------------------------------------------------------------")
			if len(u.Hotspots) == 0 {
				if len(opts.classFilters) > 0 || len(opts.domainFilters) > 0 {
					_, _ = fmt.Fprintln(stdout, "(none matching filters)")
				} else {
					_, _ = fmt.Fprintln(stdout, "(none listed)")
				}
			} else {
				for _, e := range u.Hotspots {
					_, _ = fmt.Fprintf(stdout, "%-12s  %-8s  %-36s  %s\n",
						boundAwareSizeHuman(e.SizeHuman, e.SizeIncomplete), e.RebuildExpectation, e.Label, e.Path)
				}
			}
			_, _ = fmt.Fprintln(stdout)
			if len(u.Recipes) > 0 {
				writeRecipesText(stdout, u.Recipes, false)
			}
			if u.WorkBudgetNote != "" {
				_, _ = fmt.Fprintf(stdout, "Work budget: %s\n\n", u.WorkBudgetNote)
			}
			_, _ = fmt.Fprintln(stdout, "--- early triage complete; deep root diagnostic continues below ---")
			_, _ = fmt.Fprintln(stdout)
			earlyPrinted = true
		}
	}

	report, err := spaceAnalyze(ctx, analyzeOpts)
	if err != nil {
		// Filter validation already handled; remaining errors are operational.
		msg := err.Error()
		if strings.Contains(msg, "unsupported --class") || strings.Contains(msg, "invalid --domain") ||
			strings.Contains(msg, "empty --class") || strings.Contains(msg, "empty --domain") {
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return 3
		}
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 2
	}

	if opts.format == "json" {
		if err := writeSpaceJSON(stdout, report); err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return 2
		}
	} else {
		writeSpaceText(stdout, report, earlyPrinted)
	}

	if len(report.Warnings) > 0 {
		for _, w := range report.Warnings {
			_, _ = fmt.Fprintf(stderr, "Warning: %s\n", w)
		}
		// Partial success (Exit-Codes standard: 1). Say so explicitly so an
		// agent does not read 1 as "tool failed" and retry more broadly.
		_, _ = fmt.Fprintf(stderr, "space completed with %d coverage warning(s): partial (exit 1). "+
			"The report is usable; exit status is not prune authorization.\n", len(report.Warnings))
		return 1
	}
	return foundry.ExitSuccess
}

func knownCommit(value string) string {
	if value == "" || value == "unknown" {
		return ""
	}
	return value
}

func changedFlags(cmd *cobra.Command, names ...string) []string {
	changed := make([]string, 0)
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			changed = append(changed, "--"+name)
		}
	}
	return changed
}

func writeCapacityJSON(w io.Writer, report space.CapacityOnlyReport) error {
	if err := space.ValidateCapacityInvariants(report); err != nil {
		return fmt.Errorf("space report invariant validation failed: %w", err)
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV2, payload); err != nil {
		return fmt.Errorf("space report schema validation failed: %w", err)
	}
	_, err = w.Write(append(payload, '\n'))
	return err
}

func writeCapacityText(w io.Writer, report space.CapacityOnlyReport) {
	_, _ = fmt.Fprintln(w, "=== spanwit space capacity ===")
	_, _ = fmt.Fprintf(w, "Analysis root: %s\n", sanitizeTerminalText(report.Root))
	if report.MutationContract != "" {
		_, _ = fmt.Fprintf(w, "mutation_contract: %s\n", report.MutationContract)
	}
	primaryPressure := report.Pressure
	primaryPressure.Path = sanitizeTerminalText(primaryPressure.Path)
	primaryPressure.Mount = sanitizeTerminalText(primaryPressure.Mount)
	writePressureBlock(w, "Primary write volume pressure", primaryPressure)
	if report.AnalysisPressure != nil {
		analysisPressure := *report.AnalysisPressure
		analysisPressure.Path = sanitizeTerminalText(analysisPressure.Path)
		analysisPressure.Mount = sanitizeTerminalText(analysisPressure.Mount)
		writePressureBlock(w, "Analysis root volume pressure", analysisPressure)
	}
	planes := report.CapacityAccounting.Planes
	_, _ = fmt.Fprintf(w, "Container:      %s\n", planes.Container.CollectionStatus)
	if planes.Container.Free != nil && planes.Container.Free.Bytes != nil {
		_, _ = fmt.Fprintf(w, "  free:         %s\n", planes.Container.Free.Human)
	}
	_, _ = fmt.Fprintf(w, "Volumes:       %s", planes.Volumes.CollectionStatus)
	if len(planes.Volumes.Entries) > 0 {
		_, _ = fmt.Fprintf(w, " (%d listed)", len(planes.Volumes.Entries))
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "Snapshots:     %s", planes.Snapshots.CollectionStatus)
	if planes.Snapshots.Count != nil {
		_, _ = fmt.Fprintf(w, " (%d found; bytes unsupported)", *planes.Snapshots.Count)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "System managed: %s", planes.SystemManaged.CollectionStatus)
	if len(planes.SystemManaged.Entries) > 0 {
		_, _ = fmt.Fprintf(w, " (%d diagnostic root(s))", len(planes.SystemManaged.Entries))
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "Held open:     %s\n", planes.HeldOpen.CollectionStatus)
	if planes.HeldOpen.LogicalBytes != nil && planes.HeldOpen.LogicalBytes.Bytes != nil {
		_, _ = fmt.Fprintf(w,
			"  logical lower bound: %s (%d unique object(s), %d holder process(es))\n",
			planes.HeldOpen.LogicalBytes.Human,
			optionalInt(planes.HeldOpen.UniqueObjects),
			optionalInt(planes.HeldOpen.HolderProcesses),
		)
	}
	writeCapacityNarrative(w, report.CapacityAccounting.Narrative)
}

func optionalInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func writeCapacityNarrative(w io.Writer, narrative *capacity.Narrative) {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Accounting story (planes are not summed):")
	if narrative == nil {
		_, _ = fmt.Fprintln(w, "  (narrative unavailable)")
		return
	}
	_, _ = fmt.Fprintf(w, "  %s\n", sanitizeTerminalText(narrative.Summary))
	writeCapacityStoryList(w, "Known", narrative.Known, "(none measured)")
	writeCapacityStoryList(w, "Unavailable", narrative.Unavailable, "(none)")
	writeCapacityStoryList(
		w,
		"Unexplained hints",
		narrative.UnexplainedHints,
		"(none; no numeric residual is computed)",
	)
}

func writeCapacityStoryList(w io.Writer, title string, entries []string, empty string) {
	_, _ = fmt.Fprintf(w, "%s:\n", title)
	if len(entries) == 0 {
		_, _ = fmt.Fprintf(w, "  - %s\n", empty)
		return
	}
	for _, entry := range entries {
		_, _ = fmt.Fprintf(w, "  - %s\n", sanitizeTerminalText(entry))
	}
}

func sanitizeTerminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '\uFFFD'
		}
		return r
	}, value)
}

func runSpaceCompare(stdout, stderr io.Writer, opts spaceRunOpts) int {
	if len(opts.comparePaths) != 2 {
		_, _ = fmt.Fprintln(stderr, "Error: --compare requires exactly two SpaceReport v2 inputs")
		return 3
	}
	if opts.comparePaths[0] == "-" && opts.comparePaths[1] == "-" {
		_, _ = fmt.Fprintln(stderr, "Error: --compare accepts stdin for at most one input")
		return 3
	}
	stdin := opts.compareStdin
	if stdin == nil {
		stdin = os.Stdin
	}
	inputs := make([]capacity.CompareInput, 0, 2)
	for index, path := range opts.comparePaths {
		raw, err := readCompareSource(path, stdin)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: compare input %d: %s\n", index+1, sanitizeTerminalText(err.Error()))
			return 2
		}
		input, err := space.LoadCompareInputJSON(raw, path)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: compare input %d: %s\n", index+1, sanitizeTerminalText(err.Error()))
			return 2
		}
		inputs = append(inputs, input)
	}
	now := time.Now
	if opts.compareNow != nil {
		now = opts.compareNow
	}
	report := capacity.Compare(now(), inputs[0], inputs[1])
	if opts.format == "json" {
		if err := writeCompareJSON(stdout, report); err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: %s\n", sanitizeTerminalText(err.Error()))
			return 2
		}
	} else {
		writeCompareText(stdout, report)
	}
	return foundry.ExitSuccess
}

func readCompareSource(path string, stdin io.Reader) ([]byte, error) {
	var (
		reader io.Reader
		file   *os.File
	)
	if path == "-" {
		reader = stdin
	} else {
		opened, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open explicit report: %w", err)
		}
		file = opened
		reader = opened
	}
	limited := io.LimitReader(reader, space.MaxCompareInputBytes+1)
	raw, err := io.ReadAll(limited)
	if file != nil {
		closeErr := file.Close()
		if err == nil && closeErr != nil {
			return nil, fmt.Errorf("close explicit report: %w", closeErr)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("read explicit report: %w", err)
	}
	if len(raw) > space.MaxCompareInputBytes {
		return nil, fmt.Errorf("report exceeds %d-byte limit", space.MaxCompareInputBytes)
	}
	return raw, nil
}

func writeCompareJSON(w io.Writer, report capacity.CompareReport) error {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceCompareV1, payload); err != nil {
		return fmt.Errorf("space compare schema validation failed: %w", err)
	}
	_, err = w.Write(append(payload, '\n'))
	return err
}

func writeCompareText(w io.Writer, report capacity.CompareReport) {
	_, _ = fmt.Fprintln(w, "=== spanwit space compare ===")
	_, _ = fmt.Fprintf(w, "Left:  %s (%s; %s)\n",
		sanitizeTerminalText(report.Left.Path),
		sanitizeTerminalText(report.Left.CapturedAt),
		sanitizeTerminalText(report.Left.CaptureMode),
	)
	_, _ = fmt.Fprintf(w, "Right: %s (%s; %s)\n",
		sanitizeTerminalText(report.Right.Path),
		sanitizeTerminalText(report.Right.CapturedAt),
		sanitizeTerminalText(report.Right.CaptureMode),
	)
	_, _ = fmt.Fprintf(w, "Compatibility: %s\n", report.Compatibility.Status)
	for _, check := range report.Compatibility.Checks {
		label := "gate"
		if check.Informational {
			label = "informational"
		}
		_, _ = fmt.Fprintf(w, "  - %s: ok=%v (%s; %s)\n",
			check.Name,
			check.OK,
			label,
			sanitizeTerminalText(check.Detail),
		)
	}
	_, _ = fmt.Fprintln(w, "Plane deltas (right minus left):")
	for _, delta := range report.PlaneDeltas {
		line := delta.DeltaStatus
		if delta.DeltaBytes != nil {
			line = fmt.Sprintf("%+d bytes", *delta.DeltaBytes)
		} else if delta.LeftBytes != nil || delta.RightBytes != nil {
			line += fmt.Sprintf(
				" (left=%s right=%s)",
				formatOptionalBytes(delta.LeftBytes),
				formatOptionalBytes(delta.RightBytes),
			)
		}
		if delta.Detail != nil && *delta.Detail != "" {
			line += "; " + sanitizeTerminalText(*delta.Detail)
		}
		_, _ = fmt.Fprintf(w, "  - %s.%s: %s\n", delta.Plane, delta.Field, line)
	}
	if len(report.Coverage.AsymmetricPlanes) > 0 {
		_, _ = fmt.Fprintf(w, "Asymmetric planes: %s\n",
			sanitizeTerminalText(strings.Join(report.Coverage.AsymmetricPlanes, ", ")))
	}
	writeCompareCoverageNotes(w, "Left coverage", report.Coverage.LeftPlaneNotes)
	writeCompareCoverageNotes(w, "Right coverage", report.Coverage.RightPlaneNotes)
	_, _ = fmt.Fprintln(w, "No reconciled or numeric unexplained total is computed.")
}

func writeCompareCoverageNotes(w io.Writer, title string, notes []string) {
	_, _ = fmt.Fprintf(w, "%s:\n", title)
	if len(notes) == 0 {
		_, _ = fmt.Fprintln(w, "  - (no reported gaps)")
		return
	}
	for _, note := range notes {
		_, _ = fmt.Fprintf(w, "  - %s\n", sanitizeTerminalText(note))
	}
}

func formatOptionalBytes(value *int64) string {
	if value == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%d", *value)
}

func writeSpaceJSON(w io.Writer, report space.Report) error {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, payload); err != nil {
		return fmt.Errorf("space report schema validation failed: %w", err)
	}
	_, err = w.Write(append(payload, '\n'))
	return err
}

func writeSpaceText(w io.Writer, report space.Report, earlyTriageAlreadyPrinted bool) {
	filtered := report.AppliedFilters != nil
	_, _ = fmt.Fprintf(w, "=== spanwit space ===\n")
	_, _ = fmt.Fprintf(w, "Root: %s\n", report.Root)
	_, _ = fmt.Fprintf(w, "min_size=%s  top=%d  max_depth=%d  home_caches=%v\n",
		report.MinSize, report.Top, report.MaxDepth, report.IncludeHomeCaches)
	if report.MutationContract != "" {
		_, _ = fmt.Fprintf(w, "mutation_contract=%s\n", report.MutationContract)
	}
	if report.AppliedFilters != nil {
		_, _ = fmt.Fprintf(w, "filters: class=%s  domain=%s\n",
			formatFilterList(report.AppliedFilters.Classes),
			formatFilterList(report.AppliedFilters.Domains))
	}
	if report.EnabledDomains != nil {
		_, _ = fmt.Fprintf(w, "domains.enabled: %s (policy — authorizes recipes/reclaim)\n",
			formatFilterList(*report.EnabledDomains))
	}
	_, _ = fmt.Fprintln(w)

	// Actionability layout. When early triage already streamed pressure/hotspots/
	// recipes, skip duplicating those; deep sections continue below.
	if !earlyTriageAlreadyPrinted {
		writePressureBlock(w, "Primary write volume pressure", report.Pressure)
		if report.AnalysisPressure != nil {
			writePressureBlock(w, "Analysis root volume pressure", *report.AnalysisPressure)
		}
		writeTempPlanesText(w, report.TempPlaneCoverage)
	} else if report.AnalysisPressure != nil {
		writePressureBlock(w, "Analysis root volume pressure", *report.AnalysisPressure)
	}

	// --- Can free now (prunable) + handoff ---
	prunable := filterEntriesByState(report.Verified.Candidates, space.StatePrunable)
	_, _ = fmt.Fprintln(w, "Can free now (verified prunable — prune can act after revalidate):")
	_, _ = fmt.Fprintln(w, "------------------------------------------------------------")
	if len(prunable) == 0 {
		_, _ = fmt.Fprintln(w, emptySectionMsg(filtered))
	} else {
		for _, e := range prunable {
			writeVerifiedLine(w, e)
		}
	}
	prunableIncomplete := report.Verified.PrunableSizeIncomplete
	partialNote := ""
	if prunableIncomplete {
		partialNote = "  (includes depth-bounded lower bounds — not exact totals)"
	}
	_, _ = fmt.Fprintf(w, "Prunable total: %s (%d)%s\n\n",
		boundAwareSizeHuman(report.Verified.PrunableHuman, prunableIncomplete),
		report.Verified.PrunableCandidates, partialNote)

	writePruneHandoffText(w, report.PruneHandoff)

	// --- Withheld ---
	withheld := filterEntriesByState(report.Verified.Candidates, space.StateWithheld)
	_, _ = fmt.Fprintln(w, "Withheld (context-verified but not prunable under current filters):")
	_, _ = fmt.Fprintln(w, "------------------------------------------------------------")
	if len(withheld) == 0 {
		_, _ = fmt.Fprintln(w, emptySectionMsg(filtered))
	} else {
		for _, e := range withheld {
			writeVerifiedLine(w, e)
		}
	}
	withheldIncomplete := report.Verified.WithheldSizeIncomplete
	_, _ = fmt.Fprintf(w, "Withheld total: %s (%d)\n\n",
		boundAwareSizeHuman(report.Verified.WithheldHuman, withheldIncomplete),
		report.Verified.WithheldCandidates)

	// --- Named caches + recipes (skip if early triage already printed) ---
	if !earlyTriageAlreadyPrinted {
		_, _ = fmt.Fprintln(w, "Named caches (diagnostic-only — not deleted by spanwit):")
		_, _ = fmt.Fprintln(w, "------------------------------------------------------------")
		if len(report.Hotspots) == 0 {
			_, _ = fmt.Fprintln(w, emptySectionMsg(filtered))
		} else {
			for _, e := range report.Hotspots {
				_, _ = fmt.Fprintf(w, "%-12s  %-8s  %-36s  %s\n",
					boundAwareSizeHuman(e.SizeHuman, e.SizeIncomplete), e.RebuildExpectation, e.Label, e.Path)
			}
		}
		_, _ = fmt.Fprintln(w)
		writeRecipesText(w, report.Recipes, filtered)
	}

	// --- Unverified ---
	u := report.Unverified
	uPartial := ""
	if u.SizeIncomplete {
		uPartial = "  (total is an observed partial lower bound — not an exact total)"
	}
	_, _ = fmt.Fprintf(w, "Name-shaped unverified: %d dir(s) (%s) — not context-verified, not reclaimable via prune%s\n",
		u.Count, boundAwareSizeHuman(u.TotalHuman, u.SizeIncomplete), uPartial)
	_, _ = fmt.Fprintln(w, "------------------------------------------------------------")
	if len(u.Entries) == 0 {
		if filtered {
			_, _ = fmt.Fprintln(w, "(none matching filters)")
		} else {
			_, _ = fmt.Fprintln(w, "(none above min_size)")
		}
	} else {
		for _, e := range u.Entries {
			_, _ = fmt.Fprintf(w, "%-12s  %s\n", boundAwareSizeHuman(e.SizeHuman, e.SizeIncomplete), e.Path)
		}
	}
	_, _ = fmt.Fprintln(w)

	// --- Unknown ---
	_, _ = fmt.Fprintln(w, "Top unknown directories under root:")
	_, _ = fmt.Fprintln(w, "------------------------------------------------------------")
	if len(report.Unknown) == 0 {
		if filtered {
			_, _ = fmt.Fprintln(w, "(none matching filters)")
		} else {
			_, _ = fmt.Fprintln(w, "(none above min_size)")
		}
	} else {
		for _, e := range report.Unknown {
			_, _ = fmt.Fprintf(w, "%-12s  %s\n", boundAwareSizeHuman(e.SizeHuman, e.SizeIncomplete), e.Path)
		}
	}

	// --- Placement / notes ---
	if len(report.Notes) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Notes (includes placement advice when pressure is warn/critical):")
		for _, n := range report.Notes {
			_, _ = fmt.Fprintf(w, "  - %s\n", n)
		}
	}

	// --- Result (mirrors the exit contract: complete = 0, partial = 1) ---
	if c := report.Completion; c != nil {
		_, _ = fmt.Fprintln(w)
		if c.Lifecycle == space.CompletionPartial {
			_, _ = fmt.Fprintf(w, "Result: partial — %d coverage warning(s) (exit 1; report is usable; see stderr)\n", c.WarningCount)
		} else {
			_, _ = fmt.Fprintln(w, "Result: complete")
		}
	}
}

func filterEntriesByState(entries []space.Entry, state string) []space.Entry {
	out := make([]space.Entry, 0)
	for _, e := range entries {
		if e.State == state {
			out = append(out, e)
		}
	}
	return out
}

func writeVerifiedLine(w io.Writer, e space.Entry) {
	extra := e.State
	if e.WithheldReason != "" {
		extra += ":" + e.WithheldReason
	}
	if e.Signature != "" {
		extra += "  " + e.Signature
	}
	if e.ReclaimScope == "incremental" {
		extra += "  incr"
	}
	if e.RebuildExpectation != "" {
		extra += "  rebuild=" + e.RebuildExpectation
	} else if e.ActivityIncomplete || e.ActivityBasis == "unknown" {
		extra += "  activity=unknown"
	}
	_, _ = fmt.Fprintf(w, "%-12s  %-40s  %s\n", boundAwareSizeHuman(e.SizeHuman, e.SizeIncomplete), extra, e.Path)
}

func emptySectionMsg(filtered bool) string {
	if filtered {
		return "(none matching filters)"
	}
	return "(none)"
}

func formatFilterList(vals []string) string {
	if len(vals) == 0 {
		return "(none)"
	}
	return strings.Join(vals, ",")
}

// boundAwareSizeHuman renders incomplete measurements as lower bounds (e.g. ≥1.2G)
// so human output never presents depth-bounded sizes as exact totals. Machine JSON
// keeps size_human numeric + size_incomplete as the single incompleteness signal.
// writeTempPlanesText names temp roots the run did not walk. Roots and a
// bounded size only; the follow-up is a root-level command, never a child path.
func writeTempPlanesText(w io.Writer, cov *space.TempPlaneCoverage) {
	if cov == nil || len(cov.Planes) == 0 {
		return
	}
	_, _ = fmt.Fprintln(w, "Temp planes (not walked by this run — observation only; run explicitly):")
	_, _ = fmt.Fprintln(w, "------------------------------------------------------------")
	for _, p := range cov.Planes {
		size := "?"
		if p.SizeBytes != nil {
			size = boundAwareSizeHuman(engine.HumanSize(*p.SizeBytes), p.SizeStatus == space.TempPlaneSizePartial)
		}
		// Root derives from TMPDIR (environment-controlled); sanitize it and
		// the follow-up for display. Shell quoting is not display safety.
		_, _ = fmt.Fprintf(w, "%-12s  %-11s  %-11s  %s\n    → %s\n",
			size, sanitizeTerminalText(p.SizeStatus), sanitizeTerminalText(p.Kind),
			sanitizeTerminalText(p.Root), sanitizeTerminalText(p.FollowUp))
	}
	_, _ = fmt.Fprintln(w)
}

func boundAwareSizeHuman(human string, incomplete bool) string {
	if incomplete {
		if human == "" {
			return "≥?"
		}
		if strings.HasPrefix(human, "≥") {
			return human
		}
		return "≥" + human
	}
	return human
}

func writePressureBlock(w io.Writer, title string, p space.Pressure) {
	role := p.Role
	if role == "" {
		role = "volume"
	}
	_, _ = fmt.Fprintf(w, "%s: %s (%s)\n", title, p.Level, role)
	if p.Mount != "" {
		_, _ = fmt.Fprintf(w, "  mount: %s\n", p.Mount)
	}
	_, _ = fmt.Fprintf(w, "  path:  %s\n", p.Path)
	_, _ = fmt.Fprintf(w, "  total=%s  used=%s  avail=%s  (%.1f%% used)\n\n",
		p.TotalHuman, p.UsedHuman, p.AvailHuman, p.UsedPercent)
}

func writePruneHandoffText(w io.Writer, h *space.PruneHandoff) {
	_, _ = fmt.Fprintln(w, "Prune handoff (dry-run for this report — space never deletes; prunable rows only):")
	_, _ = fmt.Fprintln(w, "------------------------------------------------------------")
	if h == nil || !h.Present {
		_, _ = fmt.Fprintln(w, "(no prunable candidates — nothing to hand off to prune)")
		_, _ = fmt.Fprintln(w)
		return
	}
	if h.AnalysisRoot != "" {
		_, _ = fmt.Fprintf(w, "analysis_root: %s  min_size: %s\n", h.AnalysisRoot, h.MinSize)
	}
	for _, e := range h.Candidates {
		sig := e.Signature
		if sig == "" {
			sig = e.Pattern
		}
		_, _ = fmt.Fprintf(w, "%-12s  prunable  %-36s  %s\n",
			boundAwareSizeHuman(e.SizeHuman, e.SizeIncomplete), sig, e.Path)
	}
	partial := ""
	if h.SizeIncomplete {
		partial = "  (includes depth-bounded lower bounds — not exact totals)"
	}
	_, _ = fmt.Fprintf(w, "Handoff total: %s (%d)%s\n",
		boundAwareSizeHuman(h.PrunableHuman, h.SizeIncomplete), h.PrunableCount, partial)
	if h.ExactPlan != nil {
		_, _ = fmt.Fprintf(w, "exact_plan: %d candidate(s) with signature/filter provenance\n", len(h.ExactPlan.Candidates))
	}
	if len(h.SuggestedCommands) > 0 {
		_, _ = fmt.Fprintln(w, "Suggested revalidate commands (dry-run only; no --execute):")
		for _, c := range h.SuggestedCommands {
			_, _ = fmt.Fprintf(w, "  %s\n", c.Display)
		}
	}
	_, _ = fmt.Fprintln(w)
}

func writeRecipesText(w io.Writer, recipes []space.Recipe, filtered bool) {
	_, _ = fmt.Fprintln(w, "Guided recipes (diagnostic-only — suggested external commands / print-only journeys, never auto-run):")
	_, _ = fmt.Fprintln(w, "------------------------------------------------------------")
	if len(recipes) == 0 {
		_, _ = fmt.Fprintln(w, emptySectionMsg(filtered))
		_, _ = fmt.Fprintln(w)
		return
	}
	for _, r := range recipes {
		kind := r.Kind
		if kind == "" {
			kind = space.RecipeKindArgv
		}
		_, _ = fmt.Fprintf(w, "[%s] %s  (state=%s, kind=%s", r.ID, r.Title, r.State, kind)
		if r.RebuildExpectation != "" {
			_, _ = fmt.Fprintf(w, ", rebuild=%s", r.RebuildExpectation)
		}
		_, _ = fmt.Fprintln(w, ")")
		if r.Path != "" {
			_, _ = fmt.Fprintf(w, "  path: %s\n", r.Path)
		}
		if len(r.Keep) > 0 {
			_, _ = fmt.Fprintf(w, "  keep: %s\n", strings.Join(r.Keep, ", "))
		}
		if len(r.Steps) > 0 {
			for i, s := range r.Steps {
				_, _ = fmt.Fprintf(w, "  step %d [%s] %s\n", i+1, s.Kind, s.Title)
				for _, line := range s.Body {
					_, _ = fmt.Fprintf(w, "    - %s\n", line)
				}
				for _, p := range s.Paths {
					_, _ = fmt.Fprintf(w, "    path: %s\n", p)
				}
				if s.Command != nil && s.Command.Display != "" {
					_, _ = fmt.Fprintf(w, "    $ %s\n", s.Command.Display)
				}
				if s.ConfigSnippet != nil {
					_, _ = fmt.Fprintf(w, "    --- %s (copy-paste; not written by spanwit) ---\n", s.ConfigSnippet.Label)
					for _, line := range strings.Split(strings.TrimRight(s.ConfigSnippet.Content, "\n"), "\n") {
						_, _ = fmt.Fprintf(w, "    %s\n", line)
					}
					_, _ = fmt.Fprintln(w, "    ---")
				}
			}
		} else {
			for _, c := range r.SuggestedCommands {
				_, _ = fmt.Fprintf(w, "  $ %s\n", c.Display)
			}
		}
		for _, note := range r.Rationale {
			_, _ = fmt.Fprintf(w, "  - %s\n", note)
		}
		_, _ = fmt.Fprintln(w)
	}
}
