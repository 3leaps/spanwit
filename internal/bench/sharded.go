package bench

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ShardedFindRunner runs several `find` processes concurrently, one per shard
// of the root's top-level entries.
//
// This exists because "if you need parallelism, shell out to find N times" is a
// real architectural option and not obviously worse than writing a walker. It
// gets a hand-tuned C traversal for free and pays for it in process startup,
// output merging, and a sharding strategy that is only as good as the tree's
// top-level balance.
//
// Measuring it settles the question with rows instead of intuition. If it wins,
// the honest conclusion is that spanwit should orchestrate find rather than
// reimplement it — and that conclusion should be reachable from this harness
// rather than argued around it.
type ShardedFindRunner struct {
	// Binary is the find executable. Defaults to "find" on PATH.
	Binary string

	// MaxShards caps concurrent find processes. Zero means use the worker
	// count from the run config.
	MaxShards int

	// lastShardCount records how the last run was actually partitioned,
	// since a tree with fewer top-level entries than requested workers
	// cannot use them all.
	lastShardCount int
}

// Name implements Runner.
func (r *ShardedFindRunner) Name() string { return "find-sharded" }

// Semantics implements Runner. Each find prints one line per entry it visits,
// so the merged count is entries visited.
func (r *ShardedFindRunner) Semantics() CountSemantics { return CountEntriesVisited }

// Mode implements Runner. The sharded runner invokes find without primaries,
// so it enumerates names without reading metadata.
func (r *ShardedFindRunner) Mode() WorkMode { return WorkPathsOnly }

// ShardCount reports how many find processes the last run used.
func (r *ShardedFindRunner) ShardCount() int { return r.lastShardCount }

// Probe implements Runner.
func (r *ShardedFindRunner) Probe(ctx context.Context) (string, bool, string) {
	probe := &ExternalRunner{
		ToolName:    "find",
		Binary:      r.binary(),
		VersionArgs: []string{"--version"},
	}
	return probe.Probe(ctx)
}

func (r *ShardedFindRunner) binary() string {
	if r.Binary != "" {
		return r.Binary
	}
	return "find"
}

// Run implements Runner.
func (r *ShardedFindRunner) Run(ctx context.Context, cfg RunConfig) (Result, error) {
	r.lastShardCount = 0

	path, err := exec.LookPath(r.binary())
	if err != nil {
		return failedResult(err), err
	}

	shardTargets, err := shardRoot(cfg.Root, r.shardLimit(cfg))
	if err != nil {
		return failedResult(err), err
	}
	r.lastShardCount = len(shardTargets)

	runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	var (
		lines     int64
		firstByte int64 // UnixNano of the first byte seen by any shard
		cpuTotal  int64
		peakRSS   int64
		failures  int64
		wg        sync.WaitGroup
	)

	start := time.Now()
	for _, target := range shardTargets {
		wg.Add(1)
		go func(roots []string) {
			defer wg.Done()

			// Roots go out in batches: a shard holding thousands of starting
			// points exceeds the operating system's argument limit, and the
			// exec fails outright. A tree with 40,000 top-level directories
			// is not exotic — the mixed-permissions corpus is one, and so is
			// a Maildir or a hash-fanned cache.
			for _, batch := range batchRoots(roots, maxRootsPerExec) {
				if runCtx.Err() != nil {
					return
				}
				cmd := exec.Command(path, batch...) //nolint:gosec // operator-configured comparator
				setProcessGroup(cmd)
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					atomic.AddInt64(&failures, 1)
					return
				}
				cmd.Stderr = io.Discard

				if err := cmd.Start(); err != nil {
					atomic.AddInt64(&failures, 1)
					return
				}
				done := make(chan struct{})
				go func() {
					select {
					case <-runCtx.Done():
						killProcessGroup(cmd)
					case <-done:
					}
				}()

				n, first, _ := countLines(stdout, start)
				waitErr := cmd.Wait()
				close(done)

				if waitErr != nil && n == 0 {
					atomic.AddInt64(&failures, 1)
				}

				atomic.AddInt64(&lines, n)
				if !first.IsZero() {
					// Record the earliest first byte across all shards: that
					// is when a user would have seen something.
					for {
						current := atomic.LoadInt64(&firstByte)
						candidate := first.UnixNano()
						if current != 0 && current <= candidate {
							break
						}
						if atomic.CompareAndSwapInt64(&firstByte, current, candidate) {
							break
						}
					}
				}
				if cpu, rss, ok := processUsage(cmd.ProcessState); ok {
					atomic.AddInt64(&cpuTotal, int64(cpu))
					for {
						current := atomic.LoadInt64(&peakRSS)
						if rss <= current {
							break
						}
						if atomic.CompareAndSwapInt64(&peakRSS, current, rss) {
							break
						}
					}
				}
			}
		}(target)
	}
	wg.Wait()

	wall := time.Since(start)
	res := Result{
		WallTime:        wall,
		EntriesExamined: atomic.LoadInt64(&lines),
		EntriesEmitted:  atomic.LoadInt64(&lines),
		CPUTime:         time.Duration(atomic.LoadInt64(&cpuTotal)),
		PeakRSSBytes:    atomic.LoadInt64(&peakRSS),
		PeakOpenFDs:     -1,
	}
	if fb := atomic.LoadInt64(&firstByte); fb != 0 {
		res.TimeToFirstResult = time.Unix(0, fb).Sub(start)
	}

	// Peak RSS is the largest single shard, not the sum. Concurrent shards do
	// hold memory simultaneously, so the true peak is somewhere between the
	// two; reporting the maximum understates it and reporting the sum
	// overstates it. The maximum is chosen because it is the figure that is
	// certainly reached, and the choice is recorded here rather than left for
	// a reader to assume.

	switch {
	case runCtx.Err() != nil:
		res.Outcome = OutcomeFailed
		res.Err = fmt.Sprintf("timed out after %s", cfg.Timeout)
		return res, fmt.Errorf("bench: find-sharded timed out after %s", cfg.Timeout)
	case atomic.LoadInt64(&failures) > 0:
		// A shard that never ran means part of the tree was never
		// enumerated, which is a different thing from a shard that ran and
		// met denials. Recording this as partial would let a run that
		// enumerated a fraction of the tree publish a wall time and an
		// entry rate that look entirely reasonable beside a complete one.
		res.Outcome = OutcomeFailed
		res.Err = fmt.Sprintf("%d find invocation(s) did not run; the tree was not fully enumerated",
			atomic.LoadInt64(&failures))
		return res, fmt.Errorf("bench: find-sharded: %s", res.Err)
	default:
		res.Outcome = OutcomeComplete
	}
	return res, nil
}

func (r *ShardedFindRunner) shardLimit(cfg RunConfig) int {
	if r.MaxShards > 0 {
		return r.MaxShards
	}
	if cfg.Workers > 0 {
		return cfg.Workers
	}
	return 1
}

// shardRoot partitions a root into at most limit traversal targets.
//
// Sharding by top-level entry is the obvious strategy and its weakness is
// worth stating: a tree whose mass sits under one top-level directory cannot
// be split this way at all, and the sharded run degenerates to a single find
// while still paying for the extra processes. The dev-tree corpus is balanced
// and the wide corpus has four top-level directories, so neither is a
// worst case. A real home directory frequently is.
func shardRoot(root string, limit int) ([][]string, error) {
	if limit < 1 {
		limit = 1
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("bench: read root for sharding: %w", err)
	}

	var dirs []string
	var loose bool
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(root, e.Name()))
		} else {
			loose = true
		}
	}
	sort.Strings(dirs)

	// Files directly under the root belong to no shard; give the whole root
	// to one process when there are none to split, or accept that a
	// top-level file is not enumerated by the directory shards. Enumerating
	// the root non-recursively would need a different find invocation, so
	// the simpler correct choice is to fall back to a single unsharded run.
	if len(dirs) == 0 || (loose && len(dirs) < 2) {
		return [][]string{{root}}, nil
	}
	if loose {
		// Add the root itself at depth 0 so top-level files are counted.
		// -maxdepth is not portable across find implementations, so instead
		// the root goes to its own shard alongside the directories, and the
		// resulting double-count of directory entries would corrupt the
		// total. Fall back to a single run rather than report a wrong count.
		return [][]string{{root}}, nil
	}

	if len(dirs) <= limit {
		out := make([][]string, 0, len(dirs))
		for _, dir := range dirs {
			out = append(out, []string{dir})
		}
		return out, nil
	}

	// More top-level directories than shards: deal them round-robin so each
	// process gets roughly the same number of starting points. find accepts
	// several starting points, so one shard is one process with several
	// roots — not one process per directory, which would start as many
	// processes as the tree has top-level entries.
	shards := make([][]string, 0, limit)
	for i := 0; i < limit; i++ {
		shards = append(shards, nil)
	}
	for i, dir := range dirs {
		shards[i%limit] = append(shards[i%limit], dir)
	}

	out := make([][]string, 0, limit)
	for _, group := range shards {
		if len(group) > 0 {
			out = append(out, group)
		}
	}
	return out, nil
}

// maxRootsPerExec bounds how many starting points are passed to one find
// invocation.
//
// The operating system limit is on the order of a megabyte of argument bytes;
// this is far below it deliberately. The cost of an extra exec is small and
// bounded, while exceeding the limit fails the invocation outright — and a
// failed invocation is a piece of the tree that was never looked at.
const maxRootsPerExec = 128

// batchRoots splits starting points into groups no larger than size.
func batchRoots(roots []string, size int) [][]string {
	if size < 1 {
		size = 1
	}
	var out [][]string
	for start := 0; start < len(roots); start += size {
		end := start + size
		if end > len(roots) {
			end = len(roots)
		}
		out = append(out, roots[start:end])
	}
	return out
}
