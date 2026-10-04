package space

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/inventory"
)

// Observation has no reference to the verified plan or handoff. Its only
// outputs are diagnostic rows and path-free coverage warnings.
var observationInventoryRun = inventory.Run

type observationWalker struct {
	opts       Options
	classified map[string]bool
}

func newObservationWalker(opts Options) *observationWalker {
	return &observationWalker{opts: opts, classified: make(map[string]bool)}
}

type observationSink struct {
	roots map[string]string
	sizes map[string]inventory.RootSummary
	gaps  map[string]int
}

func (s *observationSink) Header(h inventory.Header) error {
	for _, r := range h.Roots {
		s.roots[r.ID] = r.Path
	}
	return nil
}
func (*observationSink) Entry(inventory.Entry) error { return nil }
func (s *observationSink) Gap(g inventory.Gap) error {
	s.gaps[g.Kind]++
	return nil
}
func (s *observationSink) Summary(summary inventory.Summary) error {
	for _, r := range summary.Roots {
		s.sizes[s.roots[r.RootID]] = r
	}
	return nil
}

type observationCoverage struct {
	gaps       map[string]int
	unmeasured int
}

func newObservationCoverage() *observationCoverage {
	return &observationCoverage{gaps: make(map[string]int)}
}

func (c *observationCoverage) warnings(plane string) []string {
	if len(c.gaps) == 0 && c.unmeasured == 0 {
		return nil
	}
	parts := make([]string, 0, len(c.gaps))
	for kind, count := range c.gaps {
		parts = append(parts, fmt.Sprintf("%s=%d", kind, count))
	}
	sort.Strings(parts)
	return []string{fmt.Sprintf("%s observation coverage: %s; wholly_unmeasured=%d (partial)",
		plane, strings.Join(parts, ", "), c.unmeasured)}
}

func (w *observationWalker) run(ctx context.Context, paths []string, depth int, discovered bool,
	visit func(inventory.DirectoryVisit) bool, coverage *observationCoverage,
) *observationSink {
	s := &observationSink{roots: make(map[string]string), sizes: make(map[string]inventory.RootSummary), gaps: make(map[string]int)}
	if len(paths) == 0 {
		return s
	}
	if depth < 0 {
		depth = -1 // space accepts any negative depth as unlimited.
	}
	_, err := observationInventoryRun(ctx, inventory.Options{
		Roots: paths, Workers: w.opts.Workers, Backend: w.opts.Backend,
		EmissionMode:  inventory.EmissionSummaryOnly,
		IncludeRemote: w.opts.IncludeRemote, StallTimeout: w.opts.StallTimeout,
		OnStall: w.opts.OnStall, MutationContract: w.opts.MutationContract,
		Traversal: &inventory.TraversalOptions{MaxDepth: depth, DiscoveredRoots: discovered, OnDirectory: visit},
	}, s)
	for kind, n := range s.gaps {
		coverage.gaps[kind] += n
	}
	if err != nil {
		// Do not leak root names or per-path admission errors through warnings.
		coverage.gaps["run-failed"]++
	}
	return s
}

// size measures a non-overlapping discovered-root batch with per-root admission
// accounting. A never-read root produces a gap, never a numeric Entry. Repeated
// catalog aliases share one measurement; nested roots are disclosed as overlap
// gaps by inventory rather than being summed twice.
func (w *observationWalker) size(ctx context.Context, rows []Entry, depth int, minSize int64, coverage *observationCoverage) []Entry {
	paths := make([]string, 0, len(rows))
	seen := make(map[string]bool)
	for _, row := range rows {
		w.classified[row.Path] = true
		if !seen[row.Path] {
			seen[row.Path] = true
			paths = append(paths, row.Path)
		}
	}
	s := w.run(ctx, paths, depth, true, nil, coverage)
	for _, p := range paths {
		r, ok := s.sizes[p]
		if !ok || r.Observed == nil || !*r.Observed {
			coverage.unmeasured++
		}
	}
	out := make([]Entry, 0, len(rows))
	emitted := make(map[string]bool)
	for _, row := range rows {
		if emitted[row.Path] {
			continue
		}
		r, ok := s.sizes[row.Path]
		if !ok || r.Observed == nil || !*r.Observed {
			continue
		}
		row.SizeBytes = r.MatchedApparentBytes
		row.SizeHuman = engine.HumanSize(row.SizeBytes)
		row.SizeIncomplete = r.Lifecycle != inventory.LifecycleComplete || r.RemoteSkipCount > 0
		if includeMeasuredSize(row.SizeBytes, row.SizeIncomplete, minSize) {
			out = append(out, row)
			emitted[row.Path] = true
		}
	}
	SortEntriesByBoundAwareSize(out)
	return out
}

// discover uses the shared bounded walker without collecting file entries. Paths
// are mapped back to the caller's spelling (explicit root admission may resolve
// a symlink such as /var -> /private/var).
func (w *observationWalker) discover(ctx context.Context, root string, depth int,
	selectDir func(string, int) bool, coverage *observationCoverage,
) []string {
	var paths []string
	s := w.run(ctx, []string{root}, depth, false, func(v inventory.DirectoryVisit) bool {
		p := filepath.Join(root, filepath.FromSlash(v.RelativePath))
		if selectDir(p, v.Depth) {
			paths = append(paths, p)
			w.classified[p] = true
			return true
		}
		return false
	}, coverage)
	for _, r := range s.sizes {
		if r.Observed == nil || !*r.Observed {
			coverage.unmeasured++
		}
	}
	if len(s.sizes) == 0 {
		coverage.unmeasured++
	}
	sort.Strings(paths)
	return paths
}
