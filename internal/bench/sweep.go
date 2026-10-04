package bench

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/3leaps/spanwit/internal/corpus"
)

// SweepConfig describes a full comparator sweep.
type SweepConfig struct {
	// WorkDir is where fixture corpora are built. Each corpus gets its own
	// subdirectory and is removed after measurement unless Keep is set.
	WorkDir string

	// Corpora are the fixture shapes to measure against.
	Corpora []corpus.Spec

	// WorkerCounts is the concurrency sweep. Comparators that do not expose
	// concurrency are measured once, at one worker.
	WorkerCounts []int

	// CacheStates selects warm, cold, or both.
	CacheStates []CacheState

	// Repetitions is how many times each row is measured.
	Repetitions int

	// MinSize is the size filter applied where a comparator supports it.
	MinSize int64

	// DeviceClass is the operator's declaration about the backing storage.
	// Required: pass DeviceUnknown to state that it is not known.
	DeviceClass DeviceClass

	// Timeout bounds a single run.
	Timeout time.Duration

	// IncludeExternal enables the external comparators.
	IncludeExternal bool

	// HarnessPath is the binary re-executed for in-process walkers.
	HarnessPath string

	// PurgeCommand overrides the page-cache purge invocation, as an argv.
	//
	// Dropping caches needs privilege the sweep itself must not have: running
	// the whole sweep as root would make every permission fixture inert,
	// since root ignores mode bits. Supplying "sudo -n purge" here keeps the
	// measured process unprivileged while still producing a genuinely cold
	// cache.
	PurgeCommand []string

	// AppName is the application whose write promise is under test. Used to
	// attribute observed writes; supplied by the caller rather than
	// hardcoded so a rename or refit stays correct.
	AppName string

	// IgnorePaths are locations the sweep itself legitimately writes to,
	// such as a results file. Each one is a named exception rather than a
	// broad filter.
	IgnorePaths []string

	// Keep leaves built corpora in place after the sweep.
	Keep bool

	// Log receives progress lines. Never stdout: results go there.
	Log io.Writer
}

// SweepReport is everything one sweep produced, including what it did not
// measure.
type SweepReport struct {
	// Set holds the measurement rows.
	Set *Set

	// Skipped lists comparators that were not measured, with reasons.
	Skipped []Skip

	// Omissions lists coverage the sweep could not obtain, such as a cold
	// cache it could not enforce or a device class it could not determine.
	Omissions []corpus.Omission

	// Writes records what changed on disk outside the corpus during the
	// sweep, split into changes attributable to this application and
	// ambient machine activity that nothing here can attribute.
	Writes WitnessReport
}

// Complete reports whether the sweep measured everything it set out to.
func (r *SweepReport) Complete() bool {
	return len(r.Skipped) == 0 && len(r.Omissions) == 0
}

// Summary states what the sweep covered and what it did not, in the form a
// claim built on it has to respect.
func (r *SweepReport) Summary() string {
	s := fmt.Sprintf("%d rows", r.Set.Len())
	if len(r.Skipped) > 0 {
		s += fmt.Sprintf("; %d comparator(s) not measured", len(r.Skipped))
	}
	if len(r.Omissions) > 0 {
		s += fmt.Sprintf("; %d coverage omission(s)", len(r.Omissions))
	}
	s += "; " + r.Writes.Summary()
	if r.Complete() && r.Writes.Clean() {
		s += "; full requested coverage"
	}
	return s
}

// Validate reports whether the sweep can run as configured.
func (c *SweepConfig) Validate() error {
	if c.WorkDir == "" {
		return fmt.Errorf("bench: sweep requires a work directory")
	}
	if len(c.Corpora) == 0 {
		return fmt.Errorf("bench: sweep requires at least one corpus")
	}
	if len(c.WorkerCounts) == 0 {
		return fmt.Errorf("bench: sweep requires at least one worker count")
	}
	if len(c.CacheStates) == 0 {
		return fmt.Errorf("bench: sweep requires at least one cache state")
	}
	if c.Repetitions < 1 {
		return fmt.Errorf("bench: sweep requires at least one repetition")
	}
	if !validDeviceClass(c.DeviceClass) {
		return fmt.Errorf(
			"bench: sweep requires a device class; pass %q to state that it is not known",
			DeviceUnknown)
	}
	return nil
}

// RunSweep measures every comparator against every corpus under every
// requested condition.
//
// Nothing here decides which implementation wins. It produces rows; the
// selection is a separate judgment made against rows that exist, on corpora
// that were actually built, under conditions each row states for itself.
func RunSweep(ctx context.Context, cfg SweepConfig) (*SweepReport, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}

	report := &SweepReport{Set: &Set{}}

	runners := make([]Runner, 0, 8)
	for _, name := range InProcessWalkers() {
		runners = append(runners, &WalkerRunner{Walker: name, HarnessPath: cfg.HarnessPath})
	}
	if cfg.IncludeExternal {
		runners = append(runners, ExternalComparators()...)
		runners = append(runners, &ShardedFindRunner{})
	}

	// Running the sweep itself as root would silently void every permission
	// fixture, since root ignores mode bits. The corpus records that as an
	// omission, but it is worth saying loudly here too: the intended way to
	// get a cold cache is an elevated purge command, not an elevated sweep.
	if os.Geteuid() == 0 {
		logf(cfg.Log, "WARNING: running as root; permission fixtures are inert "+
			"and mixed-permissions results do not measure denial handling")
	}

	available, versions, skipped := ProbeAll(ctx, runners)
	report.Skipped = skipped
	for _, s := range skipped {
		logf(cfg.Log, "not measured: %s (%s)", s.Tool, s.Reason)
	}
	if len(available) == 0 {
		return report, fmt.Errorf("bench: no comparators available")
	}

	witness, err := NewWriteWitness(DefaultWitnessRoots(), OwnedPaths(cfg.AppName))
	if err != nil {
		return report, fmt.Errorf("bench: snapshot write witness: %w", err)
	}
	// The harness's own streams are not the application under test writing
	// uninvited, even when an operator redirects them into a witnessed root.
	witness.IgnoreStreams(os.Stdout, os.Stderr)

	for _, spec := range cfg.Corpora {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := runCorpus(ctx, cfg, spec, available, versions, report); err != nil {
			return report, err
		}
	}

	// The corpora and any results file are writes the sweep makes on
	// purpose; everything else under an owned path is not.
	ignore := append([]string{cfg.WorkDir}, cfg.IgnorePaths...)
	writes, err := witness.Check(ignore...)
	if err != nil {
		return report, fmt.Errorf("bench: check write witness: %w", err)
	}
	report.Writes = writes

	return report, nil
}

// runCorpus builds one corpus and measures every comparator against it.
func runCorpus(
	ctx context.Context,
	cfg SweepConfig,
	spec corpus.Spec,
	runners []Runner,
	versions map[string]string,
	report *SweepReport,
) error {
	root := filepath.Join(cfg.WorkDir, sanitizeID(spec.ID()))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("bench: create corpus root: %w", err)
	}

	logf(cfg.Log, "building corpus %s", spec.ID())
	manifest, err := corpus.Build(root, spec)
	if err != nil {
		return fmt.Errorf("bench: build corpus %s: %w", spec.ID(), err)
	}
	if !cfg.Keep {
		defer func() {
			if err := corpus.Remove(manifest); err != nil {
				logf(cfg.Log, "warning: could not remove corpus %s: %v", spec.ID(), err)
			}
		}()
	}
	report.Omissions = append(report.Omissions, manifest.Omitted...)

	// A corpus with no mount boundary below it cannot demonstrate
	// device-boundary behavior, and a sweep that stayed silent about that
	// would leave the gap for a reader to assume away.
	boundaries, observable, err := corpus.MountBoundariesUnder(manifest.Root, 3)
	if err != nil {
		logf(cfg.Log, "warning: mount boundary scan failed: %v", err)
	} else if omission, omitted := corpus.FixtureOmission(observable, len(boundaries)); omitted {
		report.Omissions = append(report.Omissions, omission)
	}

	for _, cacheState := range cfg.CacheStates {
		for _, runner := range runners {
			workerCounts := cfg.WorkerCounts
			_, isWalker := runner.(*WalkerRunner)
			_, isSharded := runner.(*ShardedFindRunner)
			if !isWalker && !isSharded && runner.Name() != "fd" {
				// Comparators without a concurrency knob are measured once
				// rather than repeated identically under a sweep that does
				// not apply to them.
				workerCounts = []int{1}
			}

			for _, workers := range workerCounts {
				for rep := 0; rep < cfg.Repetitions; rep++ {
					if err := ctx.Err(); err != nil {
						return err
					}
					row, err := measureOne(ctx, cfg, manifest, runner, versions[runner.Name()],
						cacheState, workers, rep)
					if err != nil {
						logf(cfg.Log, "%s: %v", runner.Name(), err)
						continue
					}
					report.Set.Add(row)
				}
			}
		}
	}
	return nil
}

// measureOne performs a single measured run and binds it to its environment.
func measureOne(
	ctx context.Context,
	cfg SweepConfig,
	manifest *corpus.Manifest,
	runner Runner,
	version string,
	cacheState CacheState,
	workers, rep int,
) (*Measurement, error) {
	attempted, succeeded, purgeDetail := prepareCache(ctx, cacheState, manifest.Root, cfg.PurgeCommand)

	res, runErr := runner.Run(ctx, RunConfig{
		Root:    manifest.Root,
		Workers: workers,
		MinSize: cfg.MinSize,
		Timeout: cfg.Timeout,
	})
	if runErr != nil && res.Outcome == OutcomeFailed {
		logf(cfg.Log, "%s failed: %v", runner.Name(), runErr)
	}

	env := CaptureEnvironment(manifest.Root, EnvOptions{
		DeviceClass:         cfg.DeviceClass,
		CacheState:          cacheState,
		CachePurgeAttempted: attempted,
		CachePurgeSucceeded: succeeded,
		CachePurgeDetail:    purgeDetail,
		Workers:             workers,
		CorpusID:            manifest.ID,
		CorpusCoverage:      manifest.CoverageNote(),
	})

	return NewMeasurement(runner.Name(), version, rep, env, res, runner.Semantics(), runner.Mode())
}

// prepareCache brings the page cache to the requested state and reports
// whether a cold state was enforced or merely asserted.
//
// Dropping caches needs privilege the harness does not assume. When the purge
// fails the row is still emitted, carrying the fact that its cold claim was
// not enforced — which is more useful than refusing to measure and more honest
// than a cold label over a warm cache.
func prepareCache(
	ctx context.Context,
	state CacheState,
	root string,
	purgeCommand []string,
) (attempted, succeeded bool, detail string) {
	switch state {
	case CacheWarm:
		// Walk once so the measured run finds metadata already cached.
		_, _ = SerialWalk(ctx, WalkOptions{Root: root, Sink: io.Discard})
		return false, false, ""
	case CacheCold:
		succeeded, detail := dropCaches(ctx, purgeCommand)
		return true, succeeded, detail
	default:
		return false, false, ""
	}
}

// dropCaches attempts a platform page-cache purge, reporting whether it
// actually happened and why not when it did not.
//
// The exit code is not sufficient evidence on Darwin: purge returns 0 while
// printing "Unable to purge disk buffers: Operation not permitted" when it
// lacks privilege. Trusting the status here would mark every unprivileged cold
// row as an enforced cold cache — an unenforced condition presented as a
// controlled one, which is precisely what the cache-state provenance fields
// exist to prevent. A successful purge is silent, so any output is treated as
// failure.
func dropCaches(ctx context.Context, override []string) (bool, string) {
	if len(override) > 0 {
		cmd := exec.CommandContext(ctx, override[0], override[1:]...) //nolint:gosec // operator-supplied purge command
		out, err := cmd.CombinedOutput()
		message := strings.TrimSpace(string(out))
		switch {
		case err != nil && message != "":
			return false, message
		case err != nil:
			return false, err.Error()
		case message != "":
			return false, message
		default:
			return true, ""
		}
	}
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.CommandContext(ctx, "purge")
		out, err := cmd.CombinedOutput()
		message := strings.TrimSpace(string(out))
		switch {
		case err != nil:
			if message != "" {
				return false, message
			}
			return false, err.Error()
		case message != "":
			return false, message
		default:
			return true, ""
		}
	case "linux":
		f, err := os.OpenFile("/proc/sys/vm/drop_caches", os.O_WRONLY, 0)
		if err != nil {
			return false, err.Error()
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString("3"); err != nil {
			return false, err.Error()
		}
		return true, ""
	default:
		return false, "no page-cache purge mechanism on " + runtime.GOOS
	}
}

// sanitizeID turns a corpus ID into a single safe path segment.
func sanitizeID(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

func logf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	// Progress output only; a failed write here must not derail a sweep, and
	// there is nowhere useful to report it to.
	_, _ = fmt.Fprintf(w, format+"\n", args...)
}
