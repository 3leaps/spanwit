package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/spf13/cobra"

	"github.com/3leaps/spanwit/internal/coverageattestation"
	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/inventory"
)

func newInventoryCmd(identity *appidentity.Identity) *cobra.Command {
	var (
		entryType               string
		minSize                 string
		olderThan               string
		newerThan               string
		sizeBasis               string
		oneFilesystem           bool
		top                     int
		workers                 string
		maxOpenDirs             int
		maxPendingDirs          int
		backend                 string
		format                  string
		print0                  bool
		quiet                   bool
		summaryOnly             bool
		exclusions              []string
		coverageAttest          string
		directorySummary        bool
		directoryDepth          int
		directoryTop            int
		maxAggregateDirectories int
		directoryAccounting     bool
		includeRemote           bool
		stallTimeout            time.Duration
		directories             bool
		directoryMinSize        string
	)

	cmd := &cobra.Command{
		Use:   "inventory ROOT [ROOT...]",
		Short: "Stream neutral, read-only filesystem entry facts",
		Long: `Enumerate regular files without classifying them as reclaimable.

At least one root is required. Omitted filters mean broad enumeration; there is
no hidden size or age default. Traversal never follows directory symlinks and
does not promise lexical ordering.

Without --top, matches stream as they are observed. --top N still walks the
entire declared scope and emits the final bounded size ranking. JSONL includes
a typed header, entry/gap records, and a terminal completeness summary.

--summary-only walks and reconciles the full subject but suppresses entry
records. Repeatable --exclude rules define content outside that subject:
bare names match basenames at every depth, while ./path and paths containing
a slash are root-relative. There are no hidden exclusions.

--directories answers "which folders consume at least N?" in one walk:

  spanwit --read-only inventory ~/dev --directories --directory-min-size 5GiB

It prints a readable table (or the versioned v2 stream with --format jsonl)
over the full subject, then applies --directory-depth, the inclusive
--directory-min-size floor and --directory-top. It defaults to allocated size;
pass --size-basis apparent for logical sizes. Parent and child totals overlap.
Rows whose size cannot be decided against the floor are listed as
indeterminate, never as zero. Text output requires a floor or a positive
--directory-top; JSONL is unrestricted. File predicates (--min-size, --top,
--older-than, --newer-than, --type) and legacy directory flags are refused.

--directory-summary emits JSONL directory aggregates over every regular file in
the declared subject, independent of file size/age filters. It requires
--format jsonl. These are per-path-entry sums, not unique physical or
reclaimable bytes. Its bounds are independent:

  --directory-depth            emitted depth only; every descendant is still
                               walked and accounted
  --directory-top              final directory selection only
  --max-aggregate-directories  retained aggregate states; each admitted
                               directory consumes one (a count, not a byte
                               cap; raising it can raise memory use)
  --max-pending-dirs           pending traversal frontier; exhaustion is a
                               coverage gap, not a budget failure
  --max-open-dirs              concurrently open directory descriptors
  --workers                    execution workers; does not raise either
                               directory-count budget

Exhausting the aggregate budget fails the run (exit 2) and emits no directory
totals. Narrow the roots or raise the budget explicitly; reducing depth or
top does not make it fit.

--directory-accounting additionally selects the v1 accounting profile: every
directory record carries typed byte-plane claims (apparent and allocated sums
with explicit status/basis/bound; unique physical, shared/cloned, and expected
reclaim planes reported as unsupported with no number).

Progress is path-free, rate-limited, and written only to stderr; --quiet
suppresses it. --coverage-attest PATH writes a separate, validated
coverage-attestation/v0 document after the walk. It never overwrites, never
uses stdout, and does not authorize deletion. With --directories it attests
traversal of the full subject (floor, depth and top are output selection, not
gaps); a failed, canceled or incomplete audit withholds it, reported on stderr
only, never on the audit stream.

--print0 emits only NUL-delimited local paths. Piping those paths into a
destructive command forfeits Spanwit's identity revalidation and prune guards.

inventory has no execute or deletion path. Discovery is not deletion authority.`,
		Args: cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(runInventory(cmd.Context(), identity, os.Stdout, os.Stderr, inventoryRunOpts{
				roots: args, entryType: entryType, minSize: minSize,
				olderThan: olderThan, newerThan: newerThan, sizeBasis: sizeBasis,
				oneFilesystem: oneFilesystem, top: top, workers: workers,
				maxOpenDirs: maxOpenDirs, maxPendingDirs: maxPendingDirs,
				backend: backend, format: format,
				print0: print0, quiet: quiet, summaryOnly: summaryOnly,
				exclusions: exclusions, coverageAttest: coverageAttest,
				directorySummary: directorySummary, directoryDepth: directoryDepth,
				directoryTop: directoryTop, maxAggregateDirectories: maxAggregateDirectories,
				directoryAccounting: directoryAccounting,
				includeRemote:       includeRemote, stallTimeout: stallTimeout,
				directories: directories, directoryMinSize: directoryMinSize,
				explicitFlags: changedFlags(cmd, "size-basis", "min-size", "older-than",
					"newer-than", "top", "type", "print0", "summary-only",
					"directory-summary", "directory-accounting", "coverage-attest"),
			}))
		},
	}

	cmd.Flags().StringVar(&entryType, "type", "file", "entry type (v1: file)")
	cmd.Flags().StringVarP(&minSize, "min-size", "m", "", "inclusive size floor (e.g. 1G, 5GiB; default: none)")
	cmd.Flags().StringVar(&olderThan, "older-than", "", "include files at least this old (e.g. 30d)")
	cmd.Flags().StringVar(&newerThan, "newer-than", "", "include files at most this old (e.g. 7d)")
	cmd.Flags().StringVar(&sizeBasis, "size-basis", inventory.SizeApparent, "size comparison basis: apparent|allocated")
	cmd.Flags().BoolVar(&oneFilesystem, "one-filesystem", false, "do not descend across the admitted root device")
	cmd.Flags().IntVar(&top, "top", 0, "emit only the largest N matches after a complete scan (0 streams all)")
	cmd.Flags().StringVar(&workers, "workers", "auto", "worker count or auto (auto=4; 1 selects serial)")
	cmd.Flags().IntVar(&maxOpenDirs, "max-open-dirs", 0, "maximum concurrently open directories (0=worker count)")
	cmd.Flags().IntVar(&maxPendingDirs, "max-pending-dirs", inventory.DefaultMaxPendingDirs, "maximum queued directories before fail-loud subtree skips")
	cmd.Flags().StringVar(&backend, "backend", inventory.BackendAuto, "walker backend: auto|serial|parallel")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text|jsonl")
	cmd.Flags().BoolVar(&print0, "print0", false, "emit NUL-delimited paths only (incompatible with --format jsonl)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress progress messages on stderr")
	cmd.Flags().BoolVar(&summaryOnly, "summary-only", false, "walk the full scope but suppress entry records")
	cmd.Flags().StringArrayVar(&exclusions, "exclude", nil, "exclude a root-relative path or basename (repeatable; no hidden defaults)")
	cmd.Flags().StringVar(&coverageAttest, "coverage-attest", "", "write a separate validated coverage-attestation/v0 JSON document to PATH (no overwrite)")
	cmd.Flags().BoolVar(&directorySummary, "directory-summary", false, "emit full-subject directory aggregates (requires --format jsonl)")
	cmd.Flags().IntVar(&directoryDepth, "directory-depth", -1, "maximum emitted directory depth (-1=all, 0=root only); all descendants are still walked")
	cmd.Flags().IntVar(&directoryTop, "directory-top", 0, "emit only the largest N eligible directories (0=all)")
	cmd.Flags().IntVar(&maxAggregateDirectories, "max-aggregate-directories", inventory.DefaultMaxAggregateDirectories, "maximum retained directory aggregate states; each admitted directory uses one (not limited by --directory-depth/--directory-top)")
	cmd.Flags().BoolVar(&includeRemote, "include-remote", false, "also walk network-filesystem and cloud-placeholder (File Provider) directories below a root; may download files or block on the network")
	cmd.Flags().DurationVar(&stallTimeout, "stall-timeout", inventory.DefaultStallTimeout, "skip a directory whose open does not respond within this long, reporting a path-free stalled gap (0 = wait indefinitely)")
	cmd.Flags().BoolVar(&directories, "directories", false, "readable folder audit: directory totals over the full subject (text or --format jsonl; allocated basis by default)")
	cmd.Flags().StringVar(&directoryMinSize, "directory-min-size", "", "inclusive folder size floor for --directories (e.g. 5GiB; default: none)")
	cmd.Flags().BoolVar(&directoryAccounting, "directory-accounting", false, "emit typed v1 byte-plane accounting claims (requires --directory-summary --format jsonl)")
	return cmd
}

// aggregateBudgetGuidance follows the typed budget error on stderr. It names
// flags only; it never prints the rejected directory or a rerun budget, since
// the walk stopped without counting the remaining directories.
const aggregateBudgetGuidance = `No directory totals were emitted. Each admitted directory consumes one
aggregate state, so choose narrower roots or explicitly raise
--max-aggregate-directories after considering memory use. --directory-depth
and --directory-top limit emitted rows; they do not limit traversal or
retained aggregate states.
`

type inventoryRunOpts struct {
	roots          []string
	entryType      string
	minSize        string
	olderThan      string
	newerThan      string
	sizeBasis      string
	oneFilesystem  bool
	top            int
	workers        string
	maxOpenDirs    int
	maxPendingDirs int
	backend        string
	format         string
	print0         bool
	quiet          bool
	summaryOnly    bool
	exclusions     []string
	coverageAttest string
	// beforeAttestationPublishForTest runs after the audit and before
	// attestation publication; tests only.
	beforeAttestationPublishForTest func()
	directorySummary                bool
	directoryDepth                  int
	directoryTop                    int
	maxAggregateDirectories         int
	directoryAccounting             bool
	includeRemote                   bool
	stallTimeout                    time.Duration
	directories                     bool
	directoryMinSize                string
	// explicitFlags lists flags the operator set, so defaulted and explicit
	// values can be told apart (e.g. the --directories size basis).
	explicitFlags []string
	now           time.Time
	runID         string
}

func runInventory(
	ctx context.Context,
	identity *appidentity.Identity,
	stdout, stderr io.Writer,
	opts inventoryRunOpts,
) int {
	if identity == nil || identity.BinaryName == "" {
		return inventoryUsage(stderr, "application identity is required")
	}
	if len(opts.roots) == 0 {
		return inventoryUsage(stderr, "at least one explicit root is required")
	}
	if opts.entryType != "" && opts.entryType != "file" {
		return inventoryUsage(stderr, "unsupported --type %q (v1 supports file)", opts.entryType)
	}
	switch opts.format {
	case "", "text":
		opts.format = "text"
	case "jsonl":
	default:
		return inventoryUsage(stderr, "unsupported --format %q (use text|jsonl)", opts.format)
	}
	if opts.print0 && opts.format != "text" {
		return inventoryUsage(stderr, "--print0 is incompatible with --format %s", opts.format)
	}
	if opts.summaryOnly && opts.print0 {
		return inventoryUsage(stderr, "--summary-only is incompatible with --print0")
	}
	if opts.directoryMinSize != "" && !opts.directories {
		return inventoryUsage(stderr, "--directory-min-size requires --directories")
	}
	var directoryFloor *int64
	if opts.directories {
		if code, ok := validateDirectoriesMode(stderr, &opts); !ok {
			return code
		}
		if opts.directoryMinSize != "" {
			floor, err := inventory.ParseSize(opts.directoryMinSize)
			if err != nil {
				return inventoryUsage(stderr, "invalid --directory-min-size: %v", err)
			}
			directoryFloor = &floor
		}
		if opts.format == "text" && directoryFloor == nil && opts.directoryTop == 0 {
			return inventoryUsage(stderr,
				"--directories text output needs --directory-min-size or a positive --directory-top (or use --format jsonl for every directory)")
		}
		if opts.format == "text" && !allocatedSizesSupported &&
			opts.sizeBasis == inventory.SizeAllocated && opts.directoryTop == 0 {
			return inventoryUsage(stderr,
				"allocated sizes are unavailable on this platform, so a floor cannot bound text output; add a positive --directory-top, use --format jsonl, or use --size-basis apparent")
		}
	}
	if opts.directoryAccounting && !opts.directorySummary {
		return inventoryUsage(stderr, "--directory-accounting requires --directory-summary")
	}
	if opts.directorySummary {
		if opts.format != "jsonl" {
			return inventoryUsage(stderr, "--directory-summary requires --format jsonl")
		}
		if opts.summaryOnly || opts.print0 || opts.top != 0 || opts.coverageAttest != "" {
			return inventoryUsage(stderr, "--directory-summary is incompatible with --summary-only, --print0, --top, and --coverage-attest")
		}
		if opts.directoryDepth < -1 {
			return inventoryUsage(stderr, "--directory-depth cannot be less than -1")
		}
		if opts.directoryTop < 0 {
			return inventoryUsage(stderr, "--directory-top cannot be negative")
		}
		if opts.maxAggregateDirectories <= 0 {
			return inventoryUsage(stderr, "--max-aggregate-directories must be positive")
		}
	}
	if opts.stallTimeout < 0 {
		return inventoryUsage(stderr, "--stall-timeout cannot be negative (use 0 to wait indefinitely)")
	}

	minSize, err := inventory.ParseSize(opts.minSize)
	if err != nil {
		return inventoryUsage(stderr, "invalid --min-size: %v", err)
	}
	older, err := inventory.ParseAge(opts.olderThan)
	if err != nil {
		return inventoryUsage(stderr, "invalid --older-than: %v", err)
	}
	newer, err := inventory.ParseAge(opts.newerThan)
	if err != nil {
		return inventoryUsage(stderr, "invalid --newer-than: %v", err)
	}
	if older > 0 && newer > 0 && older > newer {
		return inventoryUsage(stderr,
			"--older-than %s exceeds --newer-than %s; the age window is empty",
			opts.olderThan, opts.newerThan)
	}
	switch opts.sizeBasis {
	case "", inventory.SizeApparent, inventory.SizeAllocated:
	default:
		return inventoryUsage(stderr,
			"unsupported --size-basis %q (use apparent|allocated)", opts.sizeBasis)
	}
	switch opts.backend {
	case "", inventory.BackendAuto, inventory.BackendSerial, inventory.BackendParallel:
	default:
		return inventoryUsage(stderr,
			"unsupported --backend %q (use auto|serial|parallel)", opts.backend)
	}
	if opts.top < 0 {
		return inventoryUsage(stderr, "--top cannot be negative")
	}
	if opts.maxOpenDirs < 0 {
		return inventoryUsage(stderr, "--max-open-dirs cannot be negative")
	}
	if opts.maxPendingDirs < 0 {
		return inventoryUsage(stderr, "--max-pending-dirs cannot be negative")
	}
	workerCount, err := parseInventoryWorkers(opts.workers)
	if err != nil {
		return inventoryUsage(stderr, "%v", err)
	}
	if opts.backend == inventory.BackendSerial &&
		opts.workers != "" && opts.workers != "auto" && workerCount != 1 {
		return inventoryUsage(stderr,
			"--backend serial requires --workers 1 or auto")
	}
	if opts.backend == inventory.BackendParallel && workerCount == 1 {
		return inventoryUsage(stderr,
			"--backend parallel with --workers 1 is refused; use --backend serial")
	}
	if opts.coverageAttest != "" {
		destination, err := coverageattestation.PreflightDestination(
			opts.coverageAttest, opts.roots)
		if err != nil {
			return inventoryUsage(stderr, "%v", err)
		}
		opts.coverageAttest = destination
	}

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	var sink inventory.Sink
	switch {
	case opts.directories && opts.format == "text":
		sink = &inventoryAuditTextSink{stdout: stdout, stderr: stderr}
	case opts.format == "jsonl":
		sink = inventory.NewJSONLSink(stdout)
	case opts.print0:
		sink = &inventoryPathSink{stdout: stdout, stderr: stderr}
	default:
		sink = &inventoryTextSink{
			stdout: stdout, stderr: stderr, basis: opts.sizeBasis,
		}
	}

	var attestationCollector *coverageattestation.Collector
	var auditAttestationCollector *coverageattestation.AuditCollector
	if opts.coverageAttest != "" && opts.directories {
		auditAttestationCollector, err = coverageattestation.NewAuditCollector(
			sink, coverageattestation.DefaultMaxGaps)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: %s\n", sanitizeTerminalText(err.Error()))
			return 2
		}
		sink = auditAttestationCollector
	} else if opts.coverageAttest != "" {
		attestationCollector, err = coverageattestation.NewCollector(
			sink, coverageattestation.DefaultMaxGaps)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: %s\n", sanitizeTerminalText(err.Error()))
			return 2
		}
		sink = attestationCollector
	}

	if !opts.quiet {
		_, _ = fmt.Fprintf(stderr, "Inventory: roots=%d backend=%s workers=%s\n",
			len(opts.roots), opts.backend, opts.workers)
	}
	emissionMode := inventory.EmissionEntries
	if opts.summaryOnly {
		emissionMode = inventory.EmissionSummaryOnly
	} else if opts.directorySummary {
		emissionMode = inventory.EmissionDirectorySummary
	} else if opts.directories {
		emissionMode = inventory.EmissionDirectoryAudit
	}
	noticeRemoteRoots(stderr, opts.roots)
	stallTimeout := walkStallTimeout(opts.stallTimeout)
	onStall := newStallAlerter(stderr)
	var progress func(inventory.Progress)
	if !opts.quiet {
		progress = func(snapshot inventory.Progress) {
			_, _ = fmt.Fprintf(stderr,
				"Inventory progress: elapsed=%s visited_entries=%d visited_directories=%d matched=%d affecting_gaps=%d active_root=%s completed_roots=%d/%d rate=%.1f_entries/s\n",
				snapshot.Elapsed.Round(time.Second), snapshot.VisitedEntries,
				snapshot.VisitedDirectories, snapshot.MatchedCount,
				snapshot.AffectingGapCount, snapshot.ActiveRootID,
				snapshot.CompletedRoots, snapshot.TotalRoots,
				snapshot.EntriesPerSecond)
		}
	}
	summary, err := inventory.Run(signalCtx, inventory.Options{
		Roots: opts.roots, Backend: opts.backend, Workers: workerCount,
		MaxOpenDirs: opts.maxOpenDirs, MaxPendingDirs: opts.maxPendingDirs,
		MinSize: minSize, Exclusions: opts.exclusions, EmissionMode: emissionMode,
		OlderThan: older, NewerThan: newer, SizeBasis: opts.sizeBasis,
		OneFilesystem: opts.oneFilesystem, Top: opts.top, RunID: opts.runID,
		Now: opts.now, Progress: progress, ProgressInterval: time.Second,
		MutationContract: ResolvedMutationContract(),
		DirectoryDepth:   opts.directoryDepth, DirectoryTop: opts.directoryTop,
		MaxAggregateDirectories: opts.maxAggregateDirectories,
		DirectoryAccounting:     opts.directoryAccounting,
		DirectoryFloor:          directoryFloor,
		IncludeRemote:           opts.includeRemote,
		StallTimeout:            stallTimeout,
		OnStall:                 onStall,
	}, sink)
	runErr := err
	var attestationErr error
	if attestationCollector != nil {
		attestationErr = publishInventoryAttestation(
			attestationCollector, opts.coverageAttest, identity)
	}
	if auditAttestationCollector != nil {
		if opts.beforeAttestationPublishForTest != nil {
			opts.beforeAttestationPublishForTest()
		}
		attestationErr = publishAuditAttestation(
			auditAttestationCollector, opts.coverageAttest, identity)
		// A withheld attestation is reported, not failed: the audit's own
		// result already determines the exit status.
		var withheld *coverageattestation.WithheldError
		if errors.As(attestationErr, &withheld) {
			_, _ = fmt.Fprintf(stderr, "Warning: %s\n", sanitizeTerminalText(withheld.Error()))
			attestationErr = nil
		}
	}
	if runErr != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %s\n", sanitizeTerminalText(runErr.Error()))
		var budgetErr *inventory.AggregateBudgetError
		if errors.As(runErr, &budgetErr) {
			_, _ = io.WriteString(stderr, aggregateBudgetGuidance)
		}
		return 2
	}
	if attestationErr != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %s\n", sanitizeTerminalText(attestationErr.Error()))
		return 2
	}
	if summary.Lifecycle == inventory.LifecyclePartial {
		return 1
	}
	return 0
}

func publishInventoryAttestation(
	collector *coverageattestation.Collector,
	destination string,
	identity *appidentity.Identity,
) error {
	evidence, err := collector.Evidence()
	if err != nil {
		return err
	}
	document, err := coverageattestation.Build(evidence, coverageattestation.BuildOptions{
		EmitterName: identity.BinaryName, EmitterVersion: getVersionString(),
	})
	if err != nil {
		return err
	}
	return coverageattestation.WriteDocument(destination, document, evidence)
}

// publishAuditAttestation builds and publishes the directory-audit
// attestation. Its outcome is reported only on stderr and in the exit status,
// never on the audit stream.
func publishAuditAttestation(
	collector *coverageattestation.AuditCollector,
	destination string,
	identity *appidentity.Identity,
) error {
	evidence, err := collector.Evidence()
	if err != nil {
		return err
	}
	document, err := coverageattestation.BuildAudit(evidence, coverageattestation.BuildOptions{
		EmitterName: identity.BinaryName, EmitterVersion: getVersionString(),
	})
	if err != nil {
		return err
	}
	return coverageattestation.WriteAuditDocument(destination, document, evidence)
}

func inventoryUsage(stderr io.Writer, format string, args ...any) int {
	_, _ = fmt.Fprintf(stderr, "Error: %s\n", sanitizeTerminalText(fmt.Sprintf(format, args...)))
	return 3
}

func parseInventoryWorkers(raw string) (int, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" || raw == "auto" {
		return 0, nil
	}
	workers, err := strconv.Atoi(raw)
	if err != nil || workers < 1 {
		return 0, fmt.Errorf("invalid --workers %q (use auto or a positive integer)", raw)
	}
	return workers, nil
}

type inventoryTextSink struct {
	stdout io.Writer
	stderr io.Writer
	basis  string
}

func (s *inventoryTextSink) Header(h inventory.Header) error {
	_, err := fmt.Fprintf(s.stdout,
		"Filesystem inventory (read-only)\nbackend=%s workers=%d roots=%d size_basis=%s ordering=%s\n\n",
		h.Backend, h.Workers, len(h.Roots), h.Filters.SizeBasis, h.Ordering)
	return err
}

func (s *inventoryTextSink) Entry(entry inventory.Entry) error {
	selected, _ := entry.SelectedSize(s.basis)
	_, err := fmt.Fprintf(s.stdout, "%-10s  %s  %s\n",
		engine.HumanSize(selected), entry.ModifiedAt.Format("2006-01-02T15:04:05Z"),
		entry.LocalAbsolutePath)
	return err
}

func (s *inventoryTextSink) Gap(gap inventory.Gap) error {
	_, err := fmt.Fprintf(s.stderr, "Warning: inventory gap kind=%s path=%s: %s\n",
		gap.Kind, gap.LocalAbsolutePath, gap.Detail)
	return err
}

func (s *inventoryTextSink) Summary(summary inventory.Summary) error {
	_, err := fmt.Fprintf(s.stdout,
		"\nSummary: lifecycle=%s visited=%d matched=%d emitted=%d gaps=%d boundary_skips=%d backend=%s\n",
		summary.Lifecycle, summary.VisitedEntries, summary.MatchedCount,
		summary.EmittedCount, summary.GapCount, summary.BoundarySkipCount,
		summary.Backend)
	return err
}

// allocatedSizesSupported mirrors the platform capability; tests override it
// to exercise the non-unix refusal path on any host.
var allocatedSizesSupported = inventory.AllocatedSizesSupported

// validateDirectoriesMode resolves the --directories defaults and rejects
// conflicting flags before any discovery. It returns (exit code, ok).
func validateDirectoriesMode(stderr io.Writer, opts *inventoryRunOpts) (int, bool) {
	explicit := map[string]bool{}
	for _, name := range opts.explicitFlags {
		explicit[name] = true
	}
	for _, name := range []string{
		"--min-size", "--older-than", "--newer-than", "--top", "--type",
		"--print0", "--summary-only", "--directory-summary", "--directory-accounting",
	} {
		if explicit[name] {
			return inventoryUsage(stderr,
				"%s is not accepted with --directories (folder totals always cover the full subject)", name), false
		}
	}
	if opts.directoryDepth < -1 {
		return inventoryUsage(stderr, "--directory-depth cannot be less than -1"), false
	}
	if opts.directoryTop < 0 {
		return inventoryUsage(stderr, "--directory-top cannot be negative"), false
	}
	if opts.maxAggregateDirectories <= 0 {
		return inventoryUsage(stderr, "--max-aggregate-directories must be positive"), false
	}
	if explicit["--size-basis"] && opts.sizeBasis == "" {
		return inventoryUsage(stderr, "--size-basis requires apparent or allocated"), false
	}
	if !explicit["--size-basis"] {
		if !allocatedSizesSupported {
			return inventoryUsage(stderr,
				"--directories defaults to allocated size, which this platform cannot observe; pass --size-basis apparent (or explicitly --size-basis allocated to report it as unavailable)"), false
		}
		opts.sizeBasis = inventory.SizeAllocated
	}
	return 0, true
}

// inventoryAuditTextSink renders the directory audit as a readable table.
// Every path-bearing field passes through sanitizeTerminalText.
type inventoryAuditTextSink struct {
	stdout io.Writer
	stderr io.Writer
	basis  string
	roots  map[string]string
}

func (s *inventoryAuditTextSink) Header(inventory.Header) error { return nil }

func (s *inventoryAuditTextSink) Entry(inventory.Entry) error { return nil }

func (s *inventoryAuditTextSink) Gap(gap inventory.Gap) error {
	_, err := fmt.Fprintf(s.stderr, "Warning: inventory gap kind=%s path=%s: %s\n",
		sanitizeTerminalText(gap.Kind), sanitizeTerminalText(gap.LocalAbsolutePath),
		sanitizeTerminalText(gap.Detail))
	return err
}

func (s *inventoryAuditTextSink) Summary(inventory.Summary) error { return nil }

func (s *inventoryAuditTextSink) AuditHeader(h inventory.AuditHeader) error {
	s.basis = h.DirectorySelection.SizeBasis
	s.roots = make(map[string]string, len(h.Roots))
	var b strings.Builder
	b.WriteString("Folder audit (read-only; totals overlap and never imply reclaimable space)\n")
	for _, root := range h.Roots {
		s.roots[root.ID] = root.Path
		fmt.Fprintf(&b, "root %s: %s\n", root.ID, sanitizeTerminalText(root.Path))
	}
	floor := "none"
	if h.DirectorySelection.FloorApplied && h.DirectorySelection.FloorBytes != nil {
		floor = formatFloor(*h.DirectorySelection.FloorBytes)
	}
	depth, top := "all", "all"
	if h.DirectorySelection.Depth >= 0 {
		depth = strconv.Itoa(h.DirectorySelection.Depth)
	}
	if h.DirectorySelection.Top > 0 {
		top = strconv.Itoa(h.DirectorySelection.Top)
	}
	fmt.Fprintf(&b, "size_basis=%s floor=%s depth=%s top=%s workers=%d\n\n",
		h.DirectorySelection.SizeBasis, floor, depth, top, h.Workers)
	fmt.Fprintf(&b, "%-10s  %-5s  %-8s  %-13s  %5s  %9s  %9s  %s\n",
		"SIZE", "BOUND", "COVERAGE", "FLOOR", "DEPTH", "FILES", "SUBDIRS", "PATH")
	_, err := io.WriteString(s.stdout, b.String())
	return err
}

func (s *inventoryAuditTextSink) AuditDirectory(d inventory.AuditDirectory) error {
	claim := d.SelectedClaim(s.basis)
	size, bound := "unavail", "-"
	if claim.Bytes != nil {
		size, bound = engine.HumanSize(*claim.Bytes), claim.Bound
	}
	path := d.RelativePath
	if root, ok := s.roots[d.RootID]; ok {
		if d.RelativePath == "." {
			path = root
		} else {
			path = filepath.Join(root, filepath.FromSlash(d.RelativePath))
		}
	}
	_, err := fmt.Fprintf(s.stdout, "%-10s  %-5s  %-8s  %-13s  %5d  %9d  %9d  %s\n",
		size, bound, d.Lifecycle, d.FloorDecision, d.Depth, d.FileCount,
		d.DescendantDirectoryCount, sanitizeTerminalText(path))
	return err
}

func (s *inventoryAuditTextSink) AuditSummary(summary inventory.AuditSummary) error {
	var b strings.Builder
	fmt.Fprintf(&b, "\nSummary: lifecycle=%s canceled=%t selection_reconciled=%t emission_completed=%t emitted=%d visited_dirs=%d gaps=%d\n",
		summary.Lifecycle, summary.Canceled, summary.SelectionReconciled,
		summary.EmissionCompleted, summary.DirectoryEmittedCount,
		summary.VisitedDirectories, summary.GapCount)
	if c := summary.AuditSelectionCounts; c != nil {
		fmt.Fprintf(&b, "Selection: eligible=%d meets=%d indeterminate=%d excluded=%d not_applied=%d selected=%d planned=%d truncated=%t unavailable_size=%d\n",
			c.DepthEligible, c.Meets, c.Indeterminate, c.Excluded, c.NotApplied,
			c.SelectedBeforeTop, c.PlannedEmission, c.SelectionTruncated, c.UnavailableSizeEligible)
		if summary.EmissionCompleted && c.SelectedBeforeTop == 0 {
			b.WriteString("No folders matched the selection over the observed subject.\n")
		}
		if c.UnavailableSizeEligible > 0 {
			fmt.Fprintf(&b, "Note: %d folder(s) have no %s size here and are listed as indeterminate or unavailable; add --directory-top or use --size-basis apparent to bound them.\n",
				c.UnavailableSizeEligible, s.basis)
		}
	}
	if summary.GapCount > 0 {
		b.WriteString("Coverage is partial: totals under affected folders are lower bounds.\n")
	}
	_, err := io.WriteString(s.stdout, b.String())
	return err
}

// formatFloor echoes a floor in binary units and exact bytes, because the
// size parser treats 5G, 5GB and 5GiB alike.
func formatFloor(bytes int64) string {
	units := []struct {
		name string
		size int64
	}{{"PiB", 1 << 50}, {"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}}
	for _, unit := range units {
		if bytes >= unit.size && bytes%unit.size == 0 {
			return fmt.Sprintf("%d %s (%d bytes)", bytes/unit.size, unit.name, bytes)
		}
	}
	return fmt.Sprintf("%d bytes", bytes)
}

type inventoryPathSink struct {
	stdout io.Writer
	stderr io.Writer
}

func (*inventoryPathSink) Header(inventory.Header) error { return nil }

func (s *inventoryPathSink) Entry(entry inventory.Entry) error {
	if _, err := io.WriteString(s.stdout, entry.LocalAbsolutePath); err != nil {
		return err
	}
	_, err := s.stdout.Write([]byte{0})
	return err
}

func (s *inventoryPathSink) Gap(gap inventory.Gap) error {
	_, err := fmt.Fprintf(s.stderr, "Warning: inventory gap kind=%s path=%s: %s\n",
		gap.Kind, gap.LocalAbsolutePath, gap.Detail)
	return err
}

func (s *inventoryPathSink) Summary(summary inventory.Summary) error {
	_, err := fmt.Fprintf(s.stderr,
		"Inventory summary: lifecycle=%s visited=%d matched=%d emitted=%d gaps=%d boundary_skips=%d\n",
		summary.Lifecycle, summary.VisitedEntries, summary.MatchedCount,
		summary.EmittedCount, summary.GapCount, summary.BoundarySkipCount)
	return err
}

// noticeRemoteRoots says once per remote root, path-free, that naming it is the
// opt-in to walk it.
func noticeRemoteRoots(stderr io.Writer, roots []string) {
	for _, root := range roots {
		if kind := inventory.RemoteRootKind(root); kind != "" {
			_, _ = fmt.Fprintf(stderr, "Notice: a named root is a %s; walking it because it was named explicitly (may download files or block)\n", kind)
		}
	}
}

// walkStallTimeout maps the --stall-timeout flag onto inventory.Options:
// 0 on the command line means never abandon an open. Negative values are
// rejected as usage errors before this is called.
func walkStallTimeout(flag time.Duration) time.Duration {
	if flag == 0 {
		return -1
	}
	return flag
}

// newStallAlerter returns an OnStall callback. Stall alerts are printed even
// with --quiet: a silent hang is the failure being guarded against. They are
// path-free by construction.
func newStallAlerter(stderr io.Writer) func(inventory.StallAlert) {
	var mu sync.Mutex
	return func(alert inventory.StallAlert) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprintln(stderr, stallAlertLine(alert))
	}
}

// stallAlertLine renders a stall alert from counters only; it has no path to
// render by construction.
func stallAlertLine(alert inventory.StallAlert) string {
	limit := "waiting with no limit (--stall-timeout 0)"
	if alert.Timeout > 0 {
		limit = "will skip after " + alert.Timeout.String() + " (--stall-timeout; --include-remote is off by default)"
	}
	return fmt.Sprintf("spanwit: %d directory open(s) not responding for %s; %s",
		alert.Pending, alert.Waited.Round(time.Second), limit)
}
