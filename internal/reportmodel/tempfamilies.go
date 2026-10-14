package reportmodel

import (
	"time"
)

// TempFamily is one aggregate row keyed by a fixed label. It carries no path.
type TempFamily struct {
	Family  string `json:"family"`
	Entries int64  `json:"entries"`
	Bytes   int64  `json:"bytes"`
	// BytesBound is exact when every listed entry was measured, and lower when
	// an entry could not be stat'd and was left out of the sum; set on marshal.
	BytesBound string `json:"bytes_bound,omitempty"`
	// BytesIncomplete records that an entry of this family was skipped.
	BytesIncomplete bool      `json:"-"`
	Oldest          time.Time `json:"oldest"`
	Newest          time.Time `json:"newest"`
}

// TempResidueSummary aggregates the direct children of the temp planes by
// family. HiddenEntries counts dot-prefixed children inside the totals; hidden
// entries are never excluded. Bytes are each entry's own lstat size (for a
// directory, its record, not its contents): counts are the signal, and the
// plane probe owns content sizing.
type TempResidueSummary struct {
	EntriesTotal  int64 `json:"entries_total"`
	HiddenEntries int64 `json:"hidden_entries"`
	// SkippedEntries counts listed children that could not be stat'd. They are
	// in no family row; a family that lost an entry states a lower bound.
	SkippedEntries int64        `json:"skipped_entries,omitempty"`
	Families       []TempFamily `json:"families"`
}
