package engine

import (
	"sort"
	"time"
)

// Activity / rank constants. Ranking is advisory only — never a deletion trigger.
const (
	// ActivityBasisDescendantMtime means last_activity_at is the newest observed
	// file mtime under the candidate (symlink children not followed).
	ActivityBasisDescendantMtime = "descendant_mtime"
	// ActivityBasisUnknown means activity could not be established confidently.
	ActivityBasisUnknown = "unknown"

	// SortSize is largest-first (historical default for prune/scan).
	SortSize = "size"
	// SortIdle ranks complete cold activity first, incomplete/unknown last.
	SortIdle = "idle"

	// ReclaimScopeWhole deletes the entire verified candidate path (default).
	ReclaimScopeWhole = "whole"
	// ReclaimScopeIncremental deletes only Cargo incremental cache dirs under a
	// verified cargo-target parent.
	ReclaimScopeIncremental = "incremental"

	// Rebuild expectation values (refill likelihood; advisory only).
	RebuildHigh   = "high"
	RebuildMedium = "medium"
	RebuildLow    = "low"
)

// Coarse activity windows for advisory rebuild_expectation (churn).
var (
	activityActiveWindow = 7 * 24 * time.Hour
	activityWarmWindow   = 30 * 24 * time.Hour
)

// DeriveRebuildExpectation maps complete activity to a coarse refill expectation.
// Incomplete or missing activity returns "" (omit / unknown) — never false precision.
func DeriveRebuildExpectation(newest time.Time, incomplete bool, now time.Time) string {
	if incomplete || newest.IsZero() {
		return ""
	}
	age := now.Sub(newest)
	// Future mtime: fail-conservative as active (high churn).
	if age < 0 {
		return RebuildHigh
	}
	if age < activityActiveWindow {
		return RebuildHigh
	}
	if age < activityWarmWindow {
		return RebuildMedium
	}
	return RebuildLow
}

// SortCandidates orders candidates by mode. Idle ranking is advisory display
// order only; eligibility remains signature + filters + --execute.
//
// Idle order: complete activity first (oldest first), then bound-aware size,
// then path. Incomplete/unknown activity ranks last (never falsely cold).
//
// Size order: complete measurements first (larger first), then incomplete
// lower bounds (larger lower-bound first). Complete and incomplete rows are
// never ranked together by raw byte value as if comparable.
func SortCandidates(cands []PruneCandidate, mode string) {
	if mode == SortIdle {
		sort.SliceStable(cands, func(i, j int) bool {
			return idleLess(cands[i], cands[j])
		})
		return
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return sizeBoundLess(cands[i], cands[j])
	})
}

// sizeBoundLess ranks complete sizes before incomplete lower bounds, then by
// measured/lower-bound bytes descending, then path ascending.
func sizeBoundLess(a, b PruneCandidate) bool {
	if a.SizeIncomplete != b.SizeIncomplete {
		return !a.SizeIncomplete
	}
	if a.Size != b.Size {
		return a.Size > b.Size
	}
	return a.Path < b.Path
}

// ActivityKnown reports whether a candidate's last activity was established
// confidently. Idle ranking and every display of activity use this one
// predicate, so an incomplete or unknown measurement can never be ranked or
// presented as cold.
func ActivityKnown(c PruneCandidate) bool {
	return !c.ActivityIncomplete && !c.LastActivityAt.IsZero() && c.ActivityBasis != ActivityBasisUnknown
}

func idleLess(a, b PruneCandidate) bool {
	aUnk := !ActivityKnown(a)
	bUnk := !ActivityKnown(b)
	if aUnk != bUnk {
		return !aUnk // known activity ranks before unknown
	}
	if !aUnk && !a.LastActivityAt.Equal(b.LastActivityAt) {
		return a.LastActivityAt.Before(b.LastActivityAt) // oldest first
	}
	return sizeBoundLess(a, b)
}
