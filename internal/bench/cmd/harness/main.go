// Command harness measures filesystem inventory implementations against
// fixture corpora.
//
// It lives under internal/ so it cannot become a product surface: this is a
// development tool for producing evidence, not a shipped command. The release
// build targets ./cmd/spanwit only.
//
// Two modes:
//
//	harness sweep   run comparators over fixture corpora and emit rows
//	harness walk    run one in-process walker; used by sweep as a child
//
// Every emitted row states the conditions it was measured under. A row that
// cannot state them is not emitted, which is enforced in the bench package
// rather than here.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	appidentityembed "github.com/3leaps/spanwit/internal/assets/appidentity"
	"github.com/3leaps/spanwit/internal/bench"
	"github.com/3leaps/spanwit/internal/corpus"
	"github.com/fulmenhq/gofulmen/appidentity"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "sweep":
		err = runSweep(ctx, os.Args[2:])
	case "walk":
		err = runWalk(ctx, os.Args[2:])
	case "summarize":
		err = runSummarize(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "harness: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `harness — inventory measurement

  harness sweep     [flags]   measure comparators over fixture corpora
  harness summarize [flags]   group repeated rows and report medians
  harness walk      [flags]   run one in-process walker (child mode)

Run a subcommand with --help for its flags.
`)
}

// runWalk executes one in-process walker, printing matching paths to stdout
// and a structured summary to stderr.
//
// Keeping the summary off stdout is what lets the parent count output lines
// and time the first byte without the summary contaminating either.
func runWalk(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("walk", flag.ContinueOnError)
	impl := fs.String("impl", bench.WalkerSerial,
		"walker implementation: "+strings.Join(bench.InProcessWalkers(), ", "))
	root := fs.String("root", "", "directory to traverse (required)")
	workers := fs.Int("workers", 1, "worker count for parallel walkers")
	maxOpenDirs := fs.Int("max-open-dirs", 0, "bound on concurrently open directories (0 = worker count)")
	minSize := fs.Int64("min-size", 0, "emit only files of at least this many bytes")
	quiet := fs.Bool("quiet", false, "discard entry output instead of writing it to stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *root == "" {
		return fmt.Errorf("walk requires --root")
	}

	out := bufio.NewWriterSize(os.Stdout, 256<<10)
	opts := bench.WalkOptions{
		Root:        *root,
		Workers:     *workers,
		MaxOpenDirs: *maxOpenDirs,
		MinSize:     *minSize,
		Sink:        out,
	}
	if *quiet {
		opts.Sink = nil
	}

	var (
		stats bench.WalkStats
		err   error
	)
	switch *impl {
	case bench.WalkerSerial:
		stats, err = bench.SerialWalk(ctx, opts)
	case bench.WalkerParallel:
		stats, err = bench.ParallelWalk(ctx, opts)
	default:
		return fmt.Errorf("unknown walker %q; known: %s",
			*impl, strings.Join(bench.InProcessWalkers(), ", "))
	}
	if flushErr := out.Flush(); flushErr != nil && err == nil {
		// A broken pipe must not be reported as a completed walk.
		err = fmt.Errorf("flush output: %w", flushErr)
	}
	if err != nil {
		return err
	}

	line, err := bench.WriteWalkStats(stats)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, line)
	return nil
}

// runSweep measures every available comparator and writes rows as JSONL.
func runSweep(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	workDir := fs.String("work-dir", "", "directory to build fixture corpora in (required)")
	kinds := fs.String("corpora", "wide,deep,dev-tree,mixed-permissions",
		"comma-separated corpus kinds")
	scale := fs.Int("scale", 500, "corpus scale; interpreted per shape")
	fileSize := fs.Int64("file-size", 0, "nominal fixture file size in bytes (0 = shape default)")
	workerList := fs.String("workers", "1,2,4,8", "comma-separated worker counts to sweep")
	cacheList := fs.String("cache", "warm", "comma-separated cache states: warm, cold")
	reps := fs.Int("repetitions", 3, "measurements per row")
	minSize := fs.Int64("min-size", 0, "size filter applied where a comparator supports it")
	deviceClass := fs.String("device-class", "",
		"backing storage: ssd, rotational, network, or unknown (required)")
	external := fs.Bool("external", true, "include external comparators (find, du, fd, gdu)")
	keep := fs.Bool("keep", false, "leave built corpora in place")
	timeout := fs.Duration("timeout", bench.DefaultTimeout, "bound on a single run")
	outPath := fs.String("out", "", "write JSONL rows here instead of stdout")
	purgeCmd := fs.String("purge-command", "",
		"argv used to drop the page cache, e.g. \"sudo -n purge\"; needed for genuinely cold rows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *workDir == "" {
		return fmt.Errorf("sweep requires --work-dir")
	}
	if *deviceClass == "" {
		return fmt.Errorf(
			"sweep requires --device-class; pass 'unknown' to state that it is not known " +
				"(an unstated condition cannot be recovered after the run)")
	}

	specs, err := parseCorpora(*kinds, *scale, *fileSize)
	if err != nil {
		return err
	}
	workers, err := parseInts(*workerList)
	if err != nil {
		return fmt.Errorf("parse --workers: %w", err)
	}
	cacheStates, err := parseCacheStates(*cacheList)
	if err != nil {
		return err
	}

	harnessPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate harness binary: %w", err)
	}

	// The write promise belongs to the application, so its name comes from
	// app identity rather than a literal here.
	appName, err := applicationName(ctx)
	if err != nil {
		return err
	}

	// The results file is a write the sweep makes on purpose.
	var ignore []string
	if *outPath != "" {
		if abs, err := filepath.Abs(*outPath); err == nil {
			ignore = append(ignore, abs)
		}
	}

	report, err := bench.RunSweep(ctx, bench.SweepConfig{
		WorkDir:         *workDir,
		Corpora:         specs,
		WorkerCounts:    workers,
		CacheStates:     cacheStates,
		Repetitions:     *reps,
		MinSize:         *minSize,
		DeviceClass:     bench.DeviceClass(*deviceClass),
		PurgeCommand:    strings.Fields(*purgeCmd),
		Timeout:         *timeout,
		IncludeExternal: *external,
		HarnessPath:     harnessPath,
		AppName:         appName,
		IgnorePaths:     ignore,
		Keep:            *keep,
		Log:             os.Stderr,
	})
	if err != nil {
		return err
	}

	sink := os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			return fmt.Errorf("create %s: %w", *outPath, err)
		}
		defer func() { _ = f.Close() }()
		sink = f
	}
	if err := report.Set.WriteJSONL(sink); err != nil {
		return err
	}

	// The coverage statement goes to stderr, beside the rows rather than
	// inside them, so a reader of the results sees what was not measured
	// without having to notice an absence.
	fmt.Fprintf(os.Stderr, "\nsweep: %s\n", report.Summary())
	for _, s := range report.Skipped {
		fmt.Fprintf(os.Stderr, "  not measured: %s — %s\n", s.Tool, s.Reason)
	}
	for _, o := range report.Omissions {
		fmt.Fprintf(os.Stderr, "  omitted: %s — %s\n", o.Element, o.Reason)
	}
	for _, v := range report.Writes.Attributable {
		fmt.Fprintf(os.Stderr, "  WRITE VIOLATION: %s (%s)\n", v.Path, v.Change)
	}
	if n := report.Writes.Ambient; n > 0 {
		fmt.Fprintf(os.Stderr, "  ambient machine activity: %d file(s) changed elsewhere, e.g. %s\n",
			n, strings.Join(report.Writes.AmbientSample, ", "))
	}
	if !report.Writes.Clean() {
		return fmt.Errorf("%d attributable write(s): the zero-write promise does not hold for this sweep",
			len(report.Writes.Attributable))
	}
	return nil
}

// applicationName reads the binary name from app identity, so the write
// promise is attributed to whatever this application is actually called rather
// than to a literal that a rename would silently invalidate.
func applicationName(ctx context.Context) (string, error) {
	if err := appidentityembed.Register(); err != nil {
		return "", fmt.Errorf("register embedded app identity: %w", err)
	}
	identity, err := appidentity.Get(ctx)
	if err != nil {
		return "", fmt.Errorf("load app identity: %w", err)
	}
	return identity.Binary(), nil
}

func parseCorpora(list string, scale int, fileSize int64) ([]corpus.Spec, error) {
	var specs []corpus.Spec
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		spec := corpus.Spec{
			Kind:     corpus.Kind(name),
			Scale:    scale,
			FileSize: fileSize,
		}
		if err := spec.Validate(); err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("no corpora selected")
	}
	return specs, nil
}

func parseInts(list string) ([]int, error) {
	var out []int
	for _, field := range strings.Split(list, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(field, "%d", &n); err != nil {
			return nil, fmt.Errorf("invalid number %q", field)
		}
		if n < 1 {
			return nil, fmt.Errorf("worker count must be positive, got %d", n)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no values")
	}
	return out, nil
}

func parseCacheStates(list string) ([]bench.CacheState, error) {
	var out []bench.CacheState
	for _, field := range strings.Split(list, ",") {
		switch strings.TrimSpace(field) {
		case "warm":
			out = append(out, bench.CacheWarm)
		case "cold":
			out = append(out, bench.CacheCold)
		case "":
		default:
			return nil, fmt.Errorf("unknown cache state %q; use warm or cold", field)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no cache states selected")
	}
	return out, nil
}
