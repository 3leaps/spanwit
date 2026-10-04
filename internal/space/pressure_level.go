package space

// pressureLevel: critical when free < 10Gi or used% >= 95; warn when free < 25Gi or used% >= 85.
func pressureLevel(avail int64, usedPct float64) string {
	const (
		gi          = int64(1024 * 1024 * 1024)
		critFree    = 10 * gi
		warnFree    = 25 * gi
		critPercent = 95.0
		warnPercent = 85.0
	)
	if avail < critFree || usedPct >= critPercent {
		return PressureCritical
	}
	if avail < warnFree || usedPct >= warnPercent {
		return PressureWarn
	}
	return PressureOK
}
