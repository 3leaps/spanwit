package observe

// Hysteresis thresholds (bytes free and used%) — aligned with space pressure.
// Enter critical when free < enterCrit or used% >= enterCritPct.
// Leave critical only when free >= leaveCrit AND used% < leaveCritPct (hysteresis band).
const (
	gi = int64(1024 * 1024 * 1024)

	enterCritFree = 10 * gi
	leaveCritFree = 12 * gi // must clear above enter band
	enterWarnFree = 25 * gi
	leaveWarnFree = 28 * gi

	enterCritPct = 95.0
	leaveCritPct = 93.0
	enterWarnPct = 85.0
	leaveWarnPct = 83.0
)

// NextLevel applies hysteresis from current level given a new sample.
func NextLevel(current string, avail int64, usedPct float64) string {
	if current == "" {
		current = LevelOK
	}
	switch current {
	case LevelCritical:
		// Stay critical until both free and percent clear leave band.
		if avail >= leaveCritFree && usedPct < leaveCritPct {
			// May drop to warn or ok.
			if avail < enterWarnFree || usedPct >= enterWarnPct {
				return LevelWarn
			}
			return LevelOK
		}
		return LevelCritical
	case LevelWarn:
		if avail < enterCritFree || usedPct >= enterCritPct {
			return LevelCritical
		}
		if avail >= leaveWarnFree && usedPct < leaveWarnPct {
			return LevelOK
		}
		return LevelWarn
	default: // ok or unknown
		if avail < enterCritFree || usedPct >= enterCritPct {
			return LevelCritical
		}
		if avail < enterWarnFree || usedPct >= enterWarnPct {
			return LevelWarn
		}
		return LevelOK
	}
}

// ShouldNotify is true when level changed from last notified level.
func ShouldNotify(lastNotified, newLevel string) bool {
	if newLevel == "" {
		return false
	}
	return lastNotified != newLevel
}
