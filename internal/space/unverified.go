package space

import (
	"context"
	"path/filepath"

	"github.com/3leaps/spanwit/internal/engine"
)

// reclaimablePatterns powers the unverified (name-shaped) transparency list.
// Same names as scan's footer — not counted as reclaimable.
var reclaimablePatterns = []string{
	"target", "node_modules", "dist", "build", ".next", ".turbo", ".cache", "out", "tmp", "temp",
}

// nameShapedUnverified walks root for name-shaped reclaimable dirs that are not
// context-verified. Discovery is depth-bounded; sizing of each match is also
// depth-bounded relative to the match path. Results are complete (not
// top-truncated); callers truncate for display only.
func nameShapedUnverified(ctx context.Context, root string, verified map[string]bool, minSize int64, maxDepth int, walker *observationWalker) (UnverifiedSection, []string) {
	coverage := newObservationCoverage()
	paths := walker.discover(ctx, root, maxDepth, func(path string, _ int) bool {
		for _, pattern := range reclaimablePatterns {
			if filepath.Base(path) == pattern {
				return true
			}
		}
		return false
	}, coverage)
	var candidates []Entry
	for _, path := range paths {
		if !verified[path] {
			candidates = append(candidates, Entry{Path: path, State: StateUnverified,
				Label: "name-shaped (not context-verified)", Pattern: filepath.Base(path)})
		}
	}
	entries := walker.size(ctx, candidates, maxDepth, minSize, coverage)
	var total int64
	var incomplete bool
	for _, e := range entries {
		total += e.SizeBytes
		incomplete = incomplete || e.SizeIncomplete
	}
	return UnverifiedSection{
		Count:          len(entries),
		TotalBytes:     total,
		TotalHuman:     engine.HumanSize(total),
		Entries:        entries,
		SizeIncomplete: incomplete,
	}, coverage.warnings(PhaseUnverified)
}

// topUnknown lists large immediate children of root that are not already
// classified. skip must contain the full classified path set (not display-truncated).
// Sizing is depth-bounded (sizeDepth relative to each child).
func topUnknown(ctx context.Context, root string, skip map[string]bool, minSize int64, sizeDepth int, walker *observationWalker) ([]Entry, []string) {
	coverage := newObservationCoverage()
	paths := walker.discover(ctx, root, 1, func(string, int) bool { return true }, coverage)
	var candidates []Entry
	for _, path := range paths {
		if !skip[path] {
			candidates = append(candidates, Entry{Path: path, State: StateUnknown, Label: "unclassified directory"})
		}
	}
	out := walker.size(ctx, candidates, sizeDepth, minSize, coverage)
	return out, coverage.warnings(PhaseUnknown)
}

// truncateHotspotDisplay caps complete hotspot rows at top while always
// retaining incomplete (partial) rows outside the budget. A denied, bounded,
// or otherwise partially sized path carries coverage information: dropping it
// silently would let a TCC/SIP denial read as a clean bill of health.
// Order is preserved; partial rows keep their sorted positions.
func truncateHotspotDisplay(entries []Entry, top int) []Entry {
	if entries == nil {
		return []Entry{}
	}
	if top <= 0 {
		return entries
	}
	// Complete rows count against the budget; every incomplete row is
	// retained independently. An incomplete row inside the first N positions
	// must not consume a complete row's slot (or vice versa): partial
	// coverage stays explicit wherever it sorts.
	out := make([]Entry, 0, len(entries))
	complete := 0
	for _, e := range entries {
		if e.SizeIncomplete {
			out = append(out, e)
		} else if complete < top {
			out = append(out, e)
			complete++
		}
	}
	return out
}

// truncateVerifiedDisplay applies --top per action section/state (prunable and
// withheld separately), preserving relative order within each state. Totals and
// handoff remain based on the full filtered set; this is display-only.
// Result order is prunable rows then withheld rows (actionability layout).
func truncateVerifiedDisplay(entries []Entry, top int) []Entry {
	if entries == nil {
		return []Entry{}
	}
	if top <= 0 {
		return entries
	}
	prunable := make([]Entry, 0, top)
	withheld := make([]Entry, 0, top)
	for _, e := range entries {
		switch e.State {
		case StatePrunable:
			if len(prunable) < top {
				prunable = append(prunable, e)
			}
		case StateWithheld:
			if len(withheld) < top {
				withheld = append(withheld, e)
			}
		}
	}
	out := make([]Entry, 0, len(prunable)+len(withheld))
	out = append(out, prunable...)
	out = append(out, withheld...)
	return out
}
