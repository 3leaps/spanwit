package space

import "encoding/json"

// Every byte figure in the space report states its bound from the shared
// byte_bound vocabulary (exact|lower|upper|unknown). Bounds are derived at
// marshal time from the evidence that already decides them (size_incomplete,
// size_status, probe status), so an emitted bound can never disagree with the
// flag it restates. The bound fields on the structs exist so documents load
// back (including under unknown-field rejection); whatever they hold in
// memory is overwritten on marshal.
//
// The schemas keep these fields optional so reports written before bounds
// existed still load; tests require every emitted document to carry them.

// boundFor maps an incompleteness flag to its byte bound: an incomplete size
// is an observed minimum (lower), a complete one is exact.
func boundFor(incomplete bool) string {
	if incomplete {
		return BoundLower
	}
	return BoundExact
}

// MarshalJSON emits size_bound derived from size_incomplete.
func (e Entry) MarshalJSON() ([]byte, error) {
	type plain Entry
	p := plain(e)
	p.SizeBound = boundFor(e.SizeIncomplete)
	return json.Marshal(p)
}

// MarshalJSON emits size_bound derived from size_incomplete.
func (c ExactCandidate) MarshalJSON() ([]byte, error) {
	type plain ExactCandidate
	p := plain(c)
	p.SizeBound = boundFor(c.SizeIncomplete)
	return json.Marshal(p)
}

// MarshalJSON emits size_bound (of prunable_bytes) derived from size_incomplete.
func (h PruneHandoff) MarshalJSON() ([]byte, error) {
	type plain PruneHandoff
	p := plain(h)
	p.SizeBound = boundFor(h.SizeIncomplete)
	return json.Marshal(p)
}

// MarshalJSON emits size_bound (of total_bytes) derived from size_incomplete.
func (u UnverifiedSection) MarshalJSON() ([]byte, error) {
	type plain UnverifiedSection
	p := plain(u)
	p.SizeBound = boundFor(u.SizeIncomplete)
	return json.Marshal(p)
}

// MarshalJSON emits prunable_bound and withheld_bound from the per-state
// incompleteness helpers. size_incomplete stays the single section flag: it
// is true whenever either subtotal is a lower bound. When the per-state split
// is unknown (a section loaded from JSON keeps only size_incomplete), both
// subtotals are stated as lower bounds rather than guessed exact.
func (v VerifiedSection) MarshalJSON() ([]byte, error) {
	type plain VerifiedSection
	p := plain(v)
	prunable := v.PrunableSizeIncomplete || v.PrunableBound == BoundLower
	withheld := v.WithheldSizeIncomplete || v.WithheldBound == BoundLower
	// A section loaded from JSON keeps its emitted bounds; only when neither the
	// per-state flags nor loaded bounds say which side is incomplete are both
	// stated as lower bounds rather than guessed exact.
	if v.SizeIncomplete && !prunable && !withheld {
		prunable, withheld = true, true
	}
	p.SizeIncomplete = v.SizeIncomplete || prunable || withheld
	p.PrunableBound = boundFor(prunable)
	p.WithheldBound = boundFor(withheld)
	return json.Marshal(p)
}

// MarshalJSON emits bytes_bound: exact for the one statfs sample.
func (pr Pressure) MarshalJSON() ([]byte, error) {
	type plain Pressure
	p := plain(pr)
	p.BytesBound = BoundExact
	return json.Marshal(p)
}

// MarshalJSON emits size_bound from size_status: measured is exact, partial is
// a lower bound, unavailable has neither bytes nor bound.
func (t TempPlane) MarshalJSON() ([]byte, error) {
	type plain TempPlane
	p := plain(t)
	switch t.SizeStatus {
	case TempPlaneSizeMeasured:
		p.SizeBound = BoundExact
	case TempPlaneSizePartial:
		p.SizeBound = BoundLower
	default:
		p.SizeBound = ""
	}
	return json.Marshal(p)
}

// MarshalJSON emits bytes_bound. Family bytes are each listed entry's own
// lstat size, read without descending: exact when every entry was measured,
// lower when an entry could not be stat'd and was left out of the sum. A
// family loaded from JSON has lost the in-memory flag, so a loaded lower
// bound is kept: a round trip never upgrades lower to exact.
func (f TempFamily) MarshalJSON() ([]byte, error) {
	type plain TempFamily
	p := plain(f)
	p.BytesBound = boundFor(f.bytesIncomplete || f.BytesBound == BoundLower)
	return json.Marshal(p)
}

// MarshalJSON emits bound exact when the guest filesystem was measured.
func (g GuestFilesystem) MarshalJSON() ([]byte, error) {
	type plain GuestFilesystem
	p := plain(g)
	p.Bound = measuredBound(g.Status)
	return json.Marshal(p)
}

// MarshalJSON emits bound exact when the bucket was measured.
func (b RuntimeBucket) MarshalJSON() ([]byte, error) {
	type plain RuntimeBucket
	p := plain(b)
	p.Bound = measuredBound(b.Status)
	return json.Marshal(p)
}

// MarshalJSON emits bound exact for the engine-reported unique_bytes.
func (r RuntimeImageRef) MarshalJSON() ([]byte, error) {
	type plain RuntimeImageRef
	p := plain(r)
	p.Bound = BoundExact
	return json.Marshal(p)
}

// measuredBound is exact for an engine-measured figure and absent otherwise.
func measuredBound(status string) string {
	if status == ProbeMeasured {
		return BoundExact
	}
	return ""
}
