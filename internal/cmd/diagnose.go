package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/spf13/cobra"

	"github.com/3leaps/spanwit/internal/filepolicy"
	"github.com/3leaps/spanwit/internal/inventory"
)

func newDiagnoseCmd(identity *appidentity.Identity) *cobra.Command {
	var format string
	var quiet bool
	var includeRemote bool
	var stallTimeout time.Duration

	cmd := &cobra.Command{
		Use:   "diagnose ROOT [ROOT...]",
		Short: "Classify inventory files under diagnostic policies",
		Long: `Walk the given roots and classify regular files under file-level
diagnostic policies (downloaded installers, archives).

Classification is diagnostic-only: a match records what a file looks like
plus age, size, and redownload narrative. It never authorizes deletion,
never inspects file contents, and never enters the prune plane.
Directories cannot satisfy file policies.

Text output states found-only classification up front and always carries a
coverage banner; a partial walk reads as incomplete, never as a census.
JSONL emits one typed finding record per line plus a terminal report
record for review tooling. Withheld findings are included in JSONL with
their explicit class label and excluded from text display. Only relative
paths are emitted.

Network-filesystem and cloud-placeholder directories below a root are skipped
unless --include-remote is given; each directory open is bounded by
--stall-timeout, and a stalled directory makes coverage incomplete.

Exit status is 0 on complete coverage, 1 on incomplete coverage, 2 on
run error, 3 on usage error.`,
		// ArbitraryArgs deliberately: a missing root must reach runDiagnose
		// so it exits 3 (usage). MinimumNArgs would reject first and main
		// would map the cobra error to exit 1.
		Args: cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(runDiagnose(cmd.Context(), identity, os.Stdout, os.Stderr, diagnoseRunOpts{
				roots: args, format: format, quiet: quiet,
				includeRemote: includeRemote, stallTimeout: stallTimeout,
			}))
		},
	}

	cmd.Flags().StringVar(&format, "format", "text", "output format: text|jsonl")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress progress messages on stderr")
	cmd.Flags().BoolVar(&includeRemote, "include-remote", false, "also walk network-filesystem and cloud-placeholder (File Provider) directories below a root; may download files or block on the network")
	cmd.Flags().DurationVar(&stallTimeout, "stall-timeout", inventory.DefaultStallTimeout, "skip a directory whose open does not respond within this long, reporting a path-free stalled gap (0 = wait indefinitely)")
	return cmd
}

type diagnoseRunOpts struct {
	roots  []string
	format string
	quiet  bool
	// includeRemote and stallTimeout carry the flag values; a zero
	// stallTimeout means wait indefinitely, as on the command line.
	includeRemote bool
	stallTimeout  time.Duration
	now           time.Time
	runID         string
	policies      []filepolicy.Policy
}

// diagnoseCollector is the inventory sink that classifies entries, counts
// every evaluated file, and retains gaps plus the terminal lifecycle so the
// report bounds its own completeness.
type diagnoseCollector struct {
	policies  []filepolicy.Policy
	now       time.Time
	findings  []filepolicy.Finding
	gaps      []inventory.Gap
	evaluated int
	lifecycle string
}

func (c *diagnoseCollector) Header(inventory.Header) error { return nil }

func (c *diagnoseCollector) Entry(entry inventory.Entry) error {
	c.evaluated++
	if finding, ok := filepolicy.Classify(entry, c.policies, c.now); ok {
		c.findings = append(c.findings, finding)
	}
	return nil
}

func (c *diagnoseCollector) Gap(gap inventory.Gap) error {
	c.gaps = append(c.gaps, gap)
	return nil
}

func (c *diagnoseCollector) Summary(summary inventory.Summary) error {
	c.lifecycle = summary.Lifecycle
	return nil
}

func (c *diagnoseCollector) report() filepolicy.Report {
	return filepolicy.Summarize(c.findings, c.evaluated,
		filepolicy.CoverageFromGaps(c.lifecycle, c.gaps))
}

func runDiagnose(
	ctx context.Context,
	identity *appidentity.Identity,
	stdout, stderr io.Writer,
	opts diagnoseRunOpts,
) int {
	if identity == nil || identity.BinaryName == "" {
		return diagnoseUsage(stderr, "application identity is required")
	}
	if len(opts.roots) == 0 {
		return diagnoseUsage(stderr, "at least one explicit root is required")
	}
	switch opts.format {
	case "", "text":
		opts.format = "text"
	case "jsonl":
	default:
		return diagnoseUsage(stderr, "unsupported --format %q (use text|jsonl)", opts.format)
	}
	if opts.stallTimeout < 0 {
		return diagnoseUsage(stderr, "--stall-timeout cannot be negative (use 0 to wait indefinitely)")
	}
	policies := opts.policies
	if policies == nil {
		policies = filepolicy.DefaultPolicies()
	}
	for i := range policies {
		if err := policies[i].Validate(); err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: invalid diagnostic policy: %v\n", err)
			return 2
		}
	}
	now := opts.now
	if now.IsZero() {
		now = time.Now()
	}

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	collector := &diagnoseCollector{policies: policies, now: now}
	if !opts.quiet {
		_, _ = fmt.Fprintf(stderr, "Diagnose: roots=%d policies=%d\n", len(opts.roots), len(policies))
	}
	noticeRemoteRoots(stderr, opts.roots)
	var progress func(inventory.Progress)
	if !opts.quiet {
		progress = func(snapshot inventory.Progress) {
			_, _ = fmt.Fprintf(stderr,
				"Diagnose progress: elapsed=%s visited_entries=%d matched=%d affecting_gaps=%d completed_roots=%d/%d\n",
				snapshot.Elapsed.Round(time.Second), snapshot.VisitedEntries,
				snapshot.MatchedCount, snapshot.AffectingGapCount,
				snapshot.CompletedRoots, snapshot.TotalRoots)
		}
	}
	_, err := diagnoseInventoryRun(signalCtx, inventory.Options{
		Roots: opts.roots, EmissionMode: inventory.EmissionEntries,
		RunID: opts.runID, Now: now, Progress: progress,
		ProgressInterval: time.Second,
		MutationContract: ResolvedMutationContract(),
		IncludeRemote:    opts.includeRemote,
		StallTimeout:     walkStallTimeout(opts.stallTimeout),
		OnStall:          newStallAlerter(stderr),
	}, collector)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 2
	}
	report := collector.report()
	switch opts.format {
	case "jsonl":
		if err := filepolicy.EncodeJSONL(stdout, report); err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return 2
		}
	default:
		if _, err := io.WriteString(stdout, filepolicy.RenderText(report)); err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return 2
		}
	}
	coverage := filepolicy.Coverage{Lifecycle: report.Lifecycle, AffectingGapCount: report.AffectingGapCount}
	if !coverage.IsComplete() {
		return 1
	}
	return 0
}

// diagnoseInventoryRun is the walker entry point; tests replace it to observe
// the options diagnose passes.
var diagnoseInventoryRun = inventory.Run

func diagnoseUsage(stderr io.Writer, format string, args ...any) int {
	_, _ = fmt.Fprintf(stderr, "Error: "+format+"\n", args...)
	return 3
}
