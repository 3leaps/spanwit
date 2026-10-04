package observe

import "time"

// ComputeGrowth derives rate/runway from two samples of the same volume and basis.
// Returns unavailable/indeterminate rather than fabricating numbers.
func ComputeGrowth(older, newer Sample) GrowthResult {
	out := GrowthResult{
		FromSeq:      older.Seq,
		ToSeq:        newer.Seq,
		SameVolumeID: newer.VolumeID,
	}
	if older.VolumeID == "" || newer.VolumeID == "" || older.VolumeID != newer.VolumeID {
		out.Status = CoverageUnavailable
		out.Detail = "volume identity mismatch or empty"
		return out
	}
	if older.Basis == "" || newer.Basis == "" || older.Basis != newer.Basis {
		out.Status = CoverageUnavailable
		out.Detail = "measurement basis mismatch"
		return out
	}
	if !older.SizeComplete || !newer.SizeComplete {
		out.Status = CoverageIndeterminate
		out.Detail = "incomplete or lower-bound size cannot support rate/runway"
		return out
	}
	// Fail closed: only two complete samples may produce rate/runway.
	// partial|degraded|incomparable|unavailable|empty all refuse fabrication.
	if older.Coverage != CoverageComplete || newer.Coverage != CoverageComplete {
		out.Status = CoverageIndeterminate
		out.Detail = "both samples must have complete coverage"
		return out
	}
	if older.CapturedAt.IsZero() || newer.CapturedAt.IsZero() {
		out.Status = CoverageUnavailable
		out.Detail = "missing capture time"
		return out
	}
	elapsed := newer.CapturedAt.Sub(older.CapturedAt)
	if elapsed <= 0 {
		out.Status = CoverageUnavailable
		out.Detail = "non-positive elapsed time (clock skew or unordered samples)"
		return out
	}
	// Free-space decrease → positive fill rate (bytes/sec of free space lost).
	deltaFree := older.AvailBytes - newer.AvailBytes
	sec := elapsed.Seconds()
	rate := float64(deltaFree) / sec
	out.Status = "measured"
	out.DeltaBytes = &deltaFree
	out.ElapsedSec = &sec
	out.RateBps = &rate
	if rate > 0 && newer.AvailBytes > 0 {
		runway := float64(newer.AvailBytes) / rate
		out.RunwaySec = &runway
	} else if rate <= 0 {
		// Free stable or growing — runway not finite/positive.
		out.Detail = "free space stable or increasing; no positive fill runway"
	}
	return out
}

// Ordered returns true when a is strictly before b in time.
func Ordered(a, b time.Time) bool { return a.Before(b) }
