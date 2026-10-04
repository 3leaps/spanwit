package space

// includeMeasuredSize decides whether a depth-bounded measurement should appear
// in a diagnostic section under an opt-in min_size floor.
//
// Incomplete lower bounds below the floor are indeterminate (not proven small
// and not proven large), so they stay visible rather than matched or excluded.
// Complete measurements below the floor are excluded.
func includeMeasuredSize(bytes int64, incomplete bool, minSize int64) bool {
	if minSize <= 0 {
		return true
	}
	return bytes >= minSize || incomplete
}

// minSizeThresholdOutcome classifies an opt-in min_size decision for a measured size.
//
//	matched       — complete size meets or exceeds the floor
//	excluded      — complete size is below the floor
//	indeterminate — incomplete lower bound is below the floor (true size unknown)
//	none          — no min_size floor is active
func minSizeThresholdOutcome(bytes int64, incomplete bool, minSize int64) string {
	if minSize <= 0 {
		return "none"
	}
	if bytes >= minSize {
		return "matched"
	}
	if incomplete {
		return "indeterminate"
	}
	return "excluded"
}
