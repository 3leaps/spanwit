package observe

import "unicode/utf8"

// Resource ceilings for first-slice observe (bounded state/health/work).
const (
	// MaxRegisteredVolumes is the maximum number of volumes persisted in one state file.
	MaxRegisteredVolumes = 64
	// MaxLabelLen is the max operator label length retained in state/health.
	MaxLabelLen = 128
	// MaxRegisterPathLen is the max register path length accepted at register time.
	MaxRegisterPathLen = 1024
	// MaxVolumeIDLen is the max opaque volume id string length accepted in state.
	MaxVolumeIDLen = 256
	// MaxStateFileBytes bounds allocation before persisted JSON is decoded.
	MaxStateFileBytes = 1 << 20
	// MaxStateStringLen bounds persisted diagnostic strings and health evidence.
	MaxStateStringLen = 128
	// MaxConcurrentHardMax is the absolute ceiling on sample worker goroutines.
	// Effective concurrency is min(configured, MaxConcurrentHardMax, len(volumes)).
	MaxConcurrentHardMax = 8
	// MaxHealthUnavailable is the max unavailable reason lines on a health report.
	MaxHealthUnavailable = 32
	// MaxHealthKnown is the max known lines on a health report.
	MaxHealthKnown = 16
)

// EffectiveSampleConcurrency clamps configured concurrency to hard max and useful work.
func EffectiveSampleConcurrency(configured, volumeCount int) int {
	if volumeCount <= 0 {
		return 1 // idle cycle still reports a unit concurrency floor for scheduling honesty
	}
	maxC := configured
	if maxC <= 0 {
		maxC = 1
	}
	if maxC > MaxConcurrentHardMax {
		maxC = MaxConcurrentHardMax
	}
	if maxC > volumeCount {
		maxC = volumeCount
	}
	if maxC < 1 {
		maxC = 1
	}
	return maxC
}

// BoundString truncates s to at most max bytes without splitting UTF-8.
func BoundString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	for max > 0 && !utf8.ValidString(s[:max]) {
		max--
	}
	return s[:max]
}

// CapStrings returns at most max elements (prefix).
func CapStrings(in []string, max int) []string {
	if max <= 0 || len(in) <= max {
		return in
	}
	return in[:max]
}
