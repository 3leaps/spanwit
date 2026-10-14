package planspec

const (
	// SortSize is largest-first (historical default for prune/scan).
	SortSize = "size"
	// SortIdle ranks complete cold activity first, incomplete/unknown last.
	SortIdle = "idle"
	// ReclaimScopeWhole deletes the entire verified candidate path (default).
	ReclaimScopeWhole = "whole"
	// ReclaimScopeIncremental deletes only Cargo incremental cache dirs under a
	// verified cargo-target parent.
	ReclaimScopeIncremental = "incremental"
)

const (
	// FilterDisplayNone is the operator-visible value when a gate is unset.
	FilterDisplayNone = "none"
)
