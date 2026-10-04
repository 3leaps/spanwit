package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Walker names for the in-process implementations.
const (
	WalkerSerial   = "walkdir-serial"
	WalkerParallel = "parallel-bounded"
)

// InProcessWalkers lists the implementations this harness can run itself.
func InProcessWalkers() []string {
	return []string{WalkerSerial, WalkerParallel}
}

// FindRunner enumerates every entry below the root without reading metadata.
//
// find prints one line per entry it visits, so its line count is entries
// visited rather than a filtered match count. That makes it the closest
// like-for-like comparison to a metadata walk available from a standard tool.
func FindRunner() *ExternalRunner {
	return &ExternalRunner{
		ToolName:     "find",
		Binary:       "find",
		VersionArgs:  []string{"--version"},
		Counts:       CountEntriesVisited,
		PerEntryWork: WorkPathsOnly,
		Args: func(cfg RunConfig) []string {
			// No -size filter: adding one would change the line count from
			// entries visited to entries matched, and the two are not the
			// same measurement.
			return []string{cfg.Root}
		},
	}
}

// FindStatRunner enumerates every entry and reads its size, which is the work
// an inventory actually requires.
//
// This is the fair comparator for a walker that reports sizes. `-size +0c`
// forces a stat on every entry while excluding nothing that a bare run would
// have printed, so the entry count stays comparable and only the work changes.
func FindStatRunner() *ExternalRunner {
	return &ExternalRunner{
		ToolName:     "find-stat",
		Binary:       "find",
		VersionArgs:  []string{"--version"},
		Counts:       CountEntriesVisited,
		PerEntryWork: WorkMetadata,
		Args: func(cfg RunConfig) []string {
			return []string{cfg.Root, "-size", "+0c"}
		},
	}
}

// DuRunner walks the tree accumulating sizes.
//
// du counts a hard-linked inode once no matter how many directory entries
// point at it, so its totals legitimately differ from an entry-wise sum. That
// difference is the accounting distinction under test, not a discrepancy.
func DuRunner() *ExternalRunner {
	return &ExternalRunner{
		ToolName:    "du",
		Binary:      "du",
		VersionArgs: []string{"--version"},
		Counts:      CountLinesEmitted,
		Args: func(cfg RunConfig) []string {
			return []string{"-a", cfg.Root}
		},
	}
}

// FdRunner performs a parallel filtered file search.
func FdRunner() *ExternalRunner {
	return &ExternalRunner{
		ToolName:    "fd",
		Binary:      "fd",
		VersionArgs: []string{"--version"},
		Counts:      CountLinesEmitted,
		Args: func(cfg RunConfig) []string {
			args := []string{
				"--type", "f",
				"--hidden",
				"--no-ignore",
				"--absolute-path",
			}
			if cfg.Workers > 0 {
				args = append(args, "--threads", strconv.Itoa(cfg.Workers))
			}
			if cfg.MinSize > 0 {
				args = append(args, "--size", "+"+strconv.FormatInt(cfg.MinSize, 10)+"b")
			}
			return append(args, ".", cfg.Root)
		},
	}
}

// GduRunner performs a parallel disk usage analysis.
//
// gdu reports a summary rather than per-entry output, so it contributes wall
// time and resource figures but no entry rate. Recording that as
// CountNotReported keeps it out of throughput comparisons it cannot honestly
// join.
func GduRunner() *ExternalRunner {
	return &ExternalRunner{
		ToolName:    "gdu",
		Binary:      "gdu",
		VersionArgs: []string{"--version"},
		Counts:      CountNotReported,
		Args: func(cfg RunConfig) []string {
			return []string{"--non-interactive", "--no-progress", cfg.Root}
		},
	}
}

// ExternalComparators returns the standard external comparator set.
func ExternalComparators() []Runner {
	return []Runner{FindRunner(), FindStatRunner(), DuRunner(), FdRunner(), GduRunner()}
}

// WalkerRunner measures an in-process walker by re-executing the harness
// binary as a child process.
//
// Running our own walkers through exec rather than in-process is deliberate.
// Peak resident set size is a high-water mark for the life of a process, so an
// in-process measurement would carry over from whichever comparator ran first,
// and our implementations would be measured on a different basis from the
// external tools they are being compared against. A comparison whose
// measurement method favors one side is not a comparison.
type WalkerRunner struct {
	// Walker is one of the names in InProcessWalkers.
	Walker string

	// HarnessPath is the executable to re-invoke. Defaults to the running
	// binary.
	HarnessPath string

	// MaxOpenDirs bounds concurrent directory handles.
	MaxOpenDirs int

	// lastStats holds the structured summary the child reported.
	lastStats *WalkStats
}

// Name implements Runner.
func (r *WalkerRunner) Name() string { return r.Walker }

// Semantics implements Runner. In-process walkers report the count
// structurally, so it genuinely is entries visited.
func (r *WalkerRunner) Semantics() CountSemantics { return CountEntriesVisited }

// Mode implements Runner. Both walkers stat every regular entry to obtain its
// size, so they do metadata work whether or not a size filter is set.
func (r *WalkerRunner) Mode() WorkMode { return WorkMetadata }

// Probe implements Runner.
func (r *WalkerRunner) Probe(_ context.Context) (string, bool, string) {
	path, err := r.harness()
	if err != nil {
		return "", false, err.Error()
	}
	if _, err := os.Stat(path); err != nil {
		return "", false, fmt.Sprintf("harness binary not found at %s", path)
	}
	return runtime.Version(), true, ""
}

// Stats returns the structured summary from the most recent run, or nil when
// the child did not report one.
func (r *WalkerRunner) Stats() *WalkStats { return r.lastStats }

// Run implements Runner.
func (r *WalkerRunner) Run(ctx context.Context, cfg RunConfig) (Result, error) {
	r.lastStats = nil

	harness, err := r.harness()
	if err != nil {
		return failedResult(err), err
	}

	maxOpen := r.MaxOpenDirs
	if maxOpen < 1 {
		maxOpen = cfg.Workers
	}
	args := []string{
		"walk",
		"--impl", r.Walker,
		"--root", cfg.Root,
		"--workers", strconv.Itoa(cfg.Workers),
		"--max-open-dirs", strconv.Itoa(maxOpen),
		"--min-size", strconv.FormatInt(cfg.MinSize, 10),
	}

	child := &ExternalRunner{
		ToolName:     r.Walker,
		Binary:       harness,
		Counts:       CountLinesEmitted,
		PerEntryWork: WorkMetadata,
		Args:         func(RunConfig) []string { return args },
	}

	res, runErr := child.runCapturingStderr(ctx, cfg, func(stderr string) {
		if stats, ok := parseWalkStats(stderr); ok {
			r.lastStats = &stats
		}
	})

	// Prefer the child's structural counts over line counting: it knows what
	// it examined, what matched, and what it could not read, and it does not
	// have to infer gaps from error text.
	if r.lastStats != nil {
		res.EntriesExamined = r.lastStats.EntriesExamined
		res.EntriesMatched = r.lastStats.EntriesMatched
		res.EntriesEmitted = r.lastStats.EntriesEmitted
		res.ApparentBytes = r.lastStats.ApparentBytes
		res.Gaps = r.lastStats.Gaps
		if r.lastStats.TimeToFirstMatch > 0 {
			res.TimeToFirstResult = r.lastStats.TimeToFirstMatch
		}
		if res.Outcome != OutcomeFailed {
			if r.lastStats.Complete() {
				res.Outcome = OutcomeComplete
			} else {
				res.Outcome = OutcomePartial
			}
		}
	}
	return res, runErr
}

func (r *WalkerRunner) harness() (string, error) {
	if r.HarnessPath != "" {
		return r.HarnessPath, nil
	}
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("bench: locate harness binary: %w", err)
	}
	return path, nil
}

// walkStatsPrefix marks the structured summary line on the child's stderr,
// keeping stdout pure for entry output.
const walkStatsPrefix = "WALKSTATS "

// WriteWalkStats emits a walker's summary in the form the parent parses.
func WriteWalkStats(stats WalkStats) (string, error) {
	encoded, err := json.Marshal(stats)
	if err != nil {
		return "", fmt.Errorf("bench: encode walk stats: %w", err)
	}
	return walkStatsPrefix + string(encoded), nil
}

// parseWalkStats extracts a summary from a child's stderr.
func parseWalkStats(stderr string) (WalkStats, bool) {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, walkStatsPrefix) {
			continue
		}
		var stats WalkStats
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, walkStatsPrefix)), &stats); err != nil {
			return WalkStats{}, false
		}
		return stats, true
	}
	return WalkStats{}, false
}

// runCapturingStderr runs the command and hands the collected stderr to a
// callback before the result is finalized.
func (r *ExternalRunner) runCapturingStderr(
	ctx context.Context,
	cfg RunConfig,
	onStderr func(string),
) (Result, error) {
	capture := &stderrCapture{onDone: onStderr}
	res, err := r.runWithStderr(ctx, cfg, capture)
	return res, err
}

// stderrCapture accumulates a child's stderr for later inspection.
type stderrCapture struct {
	buf    strings.Builder
	onDone func(string)
}

func (c *stderrCapture) Write(p []byte) (int, error) {
	return c.buf.Write(p)
}

func (c *stderrCapture) finish() {
	if c.onDone != nil {
		c.onDone(c.buf.String())
	}
}

// ProbeAll probes every runner, returning the available ones and a record of
// what was skipped and why.
//
// The skip list is not incidental. A results table missing gdu because gdu was
// not installed looks exactly like one where gdu was measured and lost, and
// only one of those supports a claim.
func ProbeAll(ctx context.Context, runners []Runner) (available []Runner, versions map[string]string, skipped []Skip) {
	versions = map[string]string{}
	for _, runner := range runners {
		version, ok, reason := runner.Probe(ctx)
		if !ok {
			skipped = append(skipped, Skip{Tool: runner.Name(), Reason: reason})
			continue
		}
		versions[runner.Name()] = version
		available = append(available, runner)
	}
	return available, versions, skipped
}

// Skip records a comparator that was not measured, and why.
type Skip struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason"`
}

// DefaultTimeout bounds a single comparator run.
const DefaultTimeout = 10 * time.Minute
