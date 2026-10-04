package bench

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/3leaps/spanwit/internal/inventory"
)

// WalkStats is what a benchmarked product walker reports about its traversal.
type WalkStats struct {
	EntriesExamined int64 `json:"entries_examined"`
	EntriesMatched  int64 `json:"entries_matched"`
	EntriesEmitted  int64 `json:"entries_emitted"`
	ApparentBytes   int64 `json:"apparent_bytes"`
	Gaps            int64 `json:"gaps"`
	Vanished        int64 `json:"vanished"`

	TimeToFirstMatch time.Duration `json:"time_to_first_match_ns"`
	PeakDepth        int64         `json:"peak_depth"`
}

// Complete reports whether the walk reached its declared scope.
func (s WalkStats) Complete() bool { return s.Gaps == 0 }

// WalkOptions configures a benchmarked product walk.
type WalkOptions struct {
	Root        string
	Workers     int
	MaxOpenDirs int
	MinSize     int64
	Sink        io.Writer
}

// SerialWalk benchmarks the same serial backend shipped by `spanwit inventory`.
func SerialWalk(ctx context.Context, opts WalkOptions) (WalkStats, error) {
	return productWalk(ctx, inventory.BackendSerial, opts)
}

// ParallelWalk benchmarks the same bounded parallel backend shipped by
// `spanwit inventory`.
func ParallelWalk(ctx context.Context, opts WalkOptions) (WalkStats, error) {
	return productWalk(ctx, inventory.BackendParallel, opts)
}

func productWalk(
	ctx context.Context,
	backend string,
	opts WalkOptions,
) (WalkStats, error) {
	workers := opts.Workers
	if backend == inventory.BackendSerial {
		workers = 1
	}
	sink := &walkSink{output: opts.Sink}
	summary, err := inventory.Run(ctx, inventory.Options{
		Roots: []string{opts.Root}, Backend: backend, Workers: workers,
		MaxOpenDirs: opts.MaxOpenDirs, MinSize: opts.MinSize,
		SizeBasis: inventory.SizeApparent,
	}, sink)
	stats := WalkStats{
		EntriesExamined:  summary.VisitedEntries,
		EntriesMatched:   summary.MatchedCount,
		EntriesEmitted:   summary.EmittedCount,
		ApparentBytes:    summary.MatchedApparentBytes,
		Gaps:             summary.GapCount,
		Vanished:         summary.VanishedCount,
		TimeToFirstMatch: time.Duration(summary.TimeToFirstMatchNanos),
		PeakDepth:        summary.PeakDepth,
	}
	if err == nil && summary.Canceled {
		err = ctx.Err()
	}
	return stats, err
}

type walkSink struct {
	output io.Writer
}

func (*walkSink) Header(inventory.Header) error { return nil }

func (s *walkSink) Entry(entry inventory.Entry) error {
	if s.output == nil {
		return nil
	}
	_, err := fmt.Fprintln(s.output, entry.LocalAbsolutePath)
	return err
}

func (*walkSink) Gap(inventory.Gap) error { return nil }

func (*walkSink) Summary(inventory.Summary) error { return nil }
