package space

import "sort"

// sizeBoundLess implements the bound-aware size ranking rule:
//
//  1. Complete measurements form one band (ranked before incomplete rows).
//  2. Incomplete lower bounds form a second band (never ranked against complete
//     values as if raw byte magnitudes were comparable).
//  3. Within a band: larger measured/lower-bound first, then path ascending.
//
// Ranking is advisory display order only.
func sizeBoundLess(aBytes int64, aIncomplete bool, aPath string, bBytes int64, bIncomplete bool, bPath string) bool {
	if aIncomplete != bIncomplete {
		return !aIncomplete // complete before incomplete
	}
	if aBytes != bBytes {
		return aBytes > bBytes
	}
	return aPath < bPath
}

// SortEntriesByBoundAwareSize orders entries with the bound-aware size rule.
func SortEntriesByBoundAwareSize(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return sizeBoundLess(
			entries[i].SizeBytes, entries[i].SizeIncomplete, entries[i].Path,
			entries[j].SizeBytes, entries[j].SizeIncomplete, entries[j].Path,
		)
	})
}
