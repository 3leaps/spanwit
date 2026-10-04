package bench

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// RunConfig describes one measured run.
type RunConfig struct {
	// Root is the directory to traverse.
	Root string

	// Workers is the requested concurrency. Comparators that do not expose
	// concurrency record 1 and ignore this.
	Workers int

	// MinSize filters to entries at least this many bytes, or 0 for no size
	// filter. Comparators apply it natively where they can.
	MinSize int64

	// Timeout bounds a single run. A run that exceeds it is recorded as
	// failed rather than silently retried.
	Timeout time.Duration
}

// CountSemantics states what a comparator's entry count actually means.
//
// Different tools count different things: find prints one line per entry it
// visits, du reports directories, and a filtered search prints only matches.
// Recording the semantics beside the number stops a row from being read as a
// like-for-like entry rate when it is not one.
type CountSemantics string

const (
	// CountEntriesVisited means the count covers every entry the traversal
	// examined.
	CountEntriesVisited CountSemantics = "entries-visited"

	// CountLinesEmitted means the count is output lines, which equals
	// entries visited only when no filter applies.
	CountLinesEmitted CountSemantics = "lines-emitted"

	// CountNotReported means the comparator does not expose a count that
	// maps onto entries at all.
	CountNotReported CountSemantics = "not-reported"
)

// WorkMode states what a comparator actually did per entry.
//
// This distinction was found late and it invalidated a set of published
// comparisons. Bare find prints paths from the directory read alone and never
// calls stat; a walker that reports sizes stats every entry. Both were being
// timed under the heading "traversal speed" while doing measurably different
// work — roughly a 20-30% difference warm, and more cold, where each stat can
// become a device read.
//
// Rows carry the mode, and groups do not pool across modes, so the comparison
// cannot be made silently again.
type WorkMode string

const (
	// WorkPathsOnly means the run enumerated names without reading per-entry
	// metadata.
	WorkPathsOnly WorkMode = "paths-only"

	// WorkMetadata means the run obtained size and modification time for
	// each entry, which is what producing an inventory record requires.
	WorkMetadata WorkMode = "metadata"
)

// Runner measures one implementation.
type Runner interface {
	// Name identifies the implementation in results.
	Name() string

	// Semantics states what this runner's entry count means.
	Semantics() CountSemantics

	// Mode states what work the runner performed per entry. A paths-only
	// runner and a metadata runner are not comparable on wall time.
	Mode() WorkMode

	// Probe reports whether the runner can run here, and its version. An
	// unavailable runner returns the reason, which is recorded rather than
	// dropped: a comparison silently missing gdu looks identical to one
	// where gdu lost.
	Probe(ctx context.Context) (version string, available bool, reason string)

	// Run executes one measured traversal.
	Run(ctx context.Context, cfg RunConfig) (Result, error)
}

// ExternalRunner measures a command-line comparator such as find, du, fd, or
// gdu.
//
// Time to first result is taken as the time until the command's first byte of
// standard output. That is a proxy rather than an equivalence, and it is the
// fairest one available without instrumenting each tool: a tool that batches
// its entire result before printing genuinely does make a user wait that long.
type ExternalRunner struct {
	// ToolName is the name recorded in results.
	ToolName string

	// Binary is the executable to look up on PATH.
	Binary string

	// VersionArgs produce a version string on stdout or stderr.
	VersionArgs []string

	// Args builds the argument vector for a run.
	Args func(cfg RunConfig) []string

	// Counts states what a line of output means for this tool.
	Counts CountSemantics

	// PerEntryWork states whether the invocation reads per-entry metadata.
	PerEntryWork WorkMode
}

// Mode implements Runner.
func (r *ExternalRunner) Mode() WorkMode {
	if r.PerEntryWork == "" {
		return WorkPathsOnly
	}
	return r.PerEntryWork
}

// Name implements Runner.
func (r *ExternalRunner) Name() string { return r.ToolName }

// Semantics implements Runner.
func (r *ExternalRunner) Semantics() CountSemantics { return r.Counts }

// Probe implements Runner.
func (r *ExternalRunner) Probe(ctx context.Context) (string, bool, string) {
	path, err := exec.LookPath(r.Binary)
	if err != nil {
		return "", false, fmt.Sprintf("%s not found on PATH", r.Binary)
	}
	if len(r.VersionArgs) == 0 {
		return "unknown", true, ""
	}

	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, path, r.VersionArgs...) //nolint:gosec // operator-configured comparator
	out, err := cmd.CombinedOutput()
	line := firstLine(string(out))
	if err != nil || line == "" {
		// The binary is present, so the comparator is available; only its
		// version is unknown. BSD find, for one, has no version flag at
		// all. Recording "unknown" keeps the row emittable while stating
		// plainly that the build was not identified.
		return "unknown", true, ""
	}
	return line, true, ""
}

// Run implements Runner.
func (r *ExternalRunner) Run(ctx context.Context, cfg RunConfig) (Result, error) {
	return r.runWithStderr(ctx, cfg, nil)
}

// runWithStderr executes the command, optionally teeing stderr to capture so a
// caller can read a structured summary the child wrote there.
func (r *ExternalRunner) runWithStderr(ctx context.Context, cfg RunConfig, capture *stderrCapture) (Result, error) {
	path, err := exec.LookPath(r.Binary)
	if err != nil {
		return failedResult(err), err
	}

	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	cmd := exec.Command(path, r.Args(cfg)...) //nolint:gosec // operator-configured comparator
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return failedResult(err), err
	}
	var stderr bytes.Buffer
	if capture != nil {
		cmd.Stderr = io.MultiWriter(&stderr, capture)
	} else {
		cmd.Stderr = &stderr
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return failedResult(err), err
	}

	// Kill the process group if the run outlives its timeout, so a hung
	// comparator does not stall the sweep or leave descriptors open.
	done := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			killProcessGroup(cmd)
		case <-done:
		}
	}()

	lines, firstByteAt, readErr := countLines(stdout, start)
	waitErr := cmd.Wait()
	close(done)
	if capture != nil {
		capture.finish()
	}

	wall := time.Since(start)
	res := Result{
		WallTime:       wall,
		EntriesEmitted: lines,
		PeakOpenFDs:    -1,
	}
	if !firstByteAt.IsZero() {
		res.TimeToFirstResult = firstByteAt.Sub(start)
	}
	if cpu, rss, ok := processUsage(cmd.ProcessState); ok {
		res.CPUTime = cpu
		res.PeakRSSBytes = rss
	} else {
		res.PeakRSSBytes = -1
	}

	// Entries visited equals lines emitted only when the tool prints one
	// line per entry it looked at.
	if r.Counts == CountEntriesVisited {
		res.EntriesExamined = lines
	} else {
		res.EntriesExamined = -1
	}

	switch {
	case runCtx.Err() != nil:
		res.Outcome = OutcomeFailed
		res.Err = fmt.Sprintf("timed out after %s", cfg.Timeout)
		return res, fmt.Errorf("bench: %s timed out after %s", r.ToolName, cfg.Timeout)
	case readErr != nil:
		res.Outcome = OutcomeFailed
		res.Err = readErr.Error()
		return res, readErr
	case waitErr != nil:
		// A nonzero exit with output is the normal shape for a traversal
		// that met permission denials; record it as partial rather than
		// failed, and keep the diagnostic.
		if lines > 0 {
			res.Outcome = OutcomePartial
			res.Err = strings.TrimSpace(firstLine(stderr.String()))
			res.Gaps = int64(countStderrDenials(stderr.String()))
			return res, nil
		}
		res.Outcome = OutcomeFailed
		res.Err = fmt.Sprintf("%v: %s", waitErr, strings.TrimSpace(firstLine(stderr.String())))
		return res, waitErr
	}

	res.Gaps = int64(countStderrDenials(stderr.String()))
	if res.Gaps > 0 {
		res.Outcome = OutcomePartial
	} else {
		res.Outcome = OutcomeComplete
	}
	return res, nil
}

// countLines consumes a stream, counting newline-terminated records and
// recording when its first byte arrived.
func countLines(r io.Reader, start time.Time) (lines int64, firstByteAt time.Time, err error) {
	reader := bufio.NewReaderSize(r, 256<<10)
	var pending bool
	for {
		chunk, readErr := reader.ReadSlice('\n')
		if len(chunk) > 0 && firstByteAt.IsZero() {
			firstByteAt = time.Now()
		}
		switch readErr {
		case nil:
			lines++
			pending = false
		case bufio.ErrBufferFull:
			// A record longer than the buffer; keep reading it.
			pending = true
			continue
		case io.EOF:
			if len(chunk) > 0 || pending {
				lines++ // final record without a trailing newline
			}
			return lines, firstByteAt, nil
		default:
			return lines, firstByteAt, readErr
		}
	}
}

// countStderrDenials counts permission-denial lines, the usual shape of a gap
// in an external tool's coverage.
//
// This is a heuristic over human-readable text and it is not authoritative:
// tools word their errors differently and localize them. It is recorded as
// evidence that gaps occurred, never as a precise gap count, and the internal
// walkers report gaps structurally instead.
func countStderrDenials(stderr string) int {
	count := 0
	for _, line := range strings.Split(stderr, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "permission denied") ||
			strings.Contains(lower, "operation not permitted") {
			count++
		}
	}
	return count
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func failedResult(err error) Result {
	return Result{Outcome: OutcomeFailed, Err: err.Error(), PeakOpenFDs: -1, PeakRSSBytes: -1}
}
