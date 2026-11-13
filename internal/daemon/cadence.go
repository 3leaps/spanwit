//go:build darwin || linux

package daemon

import (
	"sort"
	"time"

	"github.com/3leaps/spanwit/internal/observe"
)

const gib = int64(1024 * 1024 * 1024)

// DeepReason records why a bounded attribution walk was requested. Reasons are
// observations, never authority to evaluate or execute a cleanup.
type DeepReason string

const (
	DeepInitialBaseline  DeepReason = "initial_baseline"
	DeepBaselineLoss     DeepReason = "free_space_loss_since_baseline"
	DeepHeartbeatLoss    DeepReason = "free_space_loss_since_heartbeat"
	DeepHourlyBaseline   DeepReason = "hourly_baseline"
	DeepPressureWorsened DeepReason = "pressure_worsened"
)

// DeepRequest is returned at most once until Complete is called. The caller
// owns the actual walk and must report whether it produced a comparable,
// complete observation.
type DeepRequest struct{ Reasons []DeepReason }

// CadenceConfig bounds how frequently expensive attribution can be requested.
// Zero values select the reviewed v0.3 observation cadence.
type CadenceConfig struct {
	Cooldown      time.Duration
	BaselineEvery time.Duration
	BaselineLoss  int64
	HeartbeatLoss int64
}

func defaultCadenceConfig(c CadenceConfig) CadenceConfig {
	if c.Cooldown <= 0 {
		c.Cooldown = 15 * time.Minute
	}
	if c.BaselineEvery <= 0 {
		c.BaselineEvery = time.Hour
	}
	if c.BaselineLoss <= 0 {
		c.BaselineLoss = 5 * gib
	}
	if c.HeartbeatLoss <= 0 {
		c.HeartbeatLoss = 2 * gib
	}
	return c
}

// Cadence is a pure, single-owner state machine; callers must serialize
// Heartbeat and Complete. Its times are monotonic elapsed durations supplied
// by the coordinator, never wall-clock timestamps.
type Cadence struct {
	config         CadenceConfig
	lastNow        time.Duration
	seenNow        bool
	lastHeartbeat  *observe.Sample
	baseline       *observe.Sample
	baselineAt     time.Duration
	lastAttempt    time.Duration
	hasAttempt     bool
	inFlight       bool
	inFlightAt     uint64
	lastLevel      string
	criticalBypass bool
	event          uint64
	pending        map[DeepReason]uint64
}

func NewCadence(config CadenceConfig) *Cadence {
	return &Cadence{config: defaultCadenceConfig(config), pending: make(map[DeepReason]uint64)}
}

// Heartbeat records one cheap sample and, when allowed, returns a coalesced
// request for one deep attribution walk. Invalid or incomparable samples never
// produce a growth claim.
func (c *Cadence) Heartbeat(now time.Duration, sample observe.Sample) *DeepRequest {
	now = c.monotonic(now)
	if !comparableSample(sample) {
		return nil
	}
	if c.lastHeartbeat != nil && !sameSubject(*c.lastHeartbeat, sample) {
		c.resetComparableState()
	}
	if c.lastHeartbeat == nil {
		c.add(DeepInitialBaseline)
	} else if loss(*c.lastHeartbeat, sample) >= c.config.HeartbeatLoss {
		c.add(DeepHeartbeatLoss)
	}
	if c.baseline != nil && sameSubject(*c.baseline, sample) {
		if loss(*c.baseline, sample) >= c.config.BaselineLoss {
			c.add(DeepBaselineLoss)
		}
		if now-c.baselineAt >= c.config.BaselineEvery {
			c.add(DeepHourlyBaseline)
		}
	}
	level := sample.Level
	if pressureRank(level) > pressureRank(c.lastLevel) {
		c.add(DeepPressureWorsened)
	}
	criticalTransition := level == observe.LevelCritical && c.lastLevel != observe.LevelCritical
	if level != observe.LevelCritical {
		c.criticalBypass = false
	}
	c.lastLevel = level
	copy := sample
	c.lastHeartbeat = &copy
	return c.request(now, criticalTransition && !c.criticalBypass)
}

// Complete reports a requested scan. Only a successful, comparable result
// becomes the baseline. Failed, partial or canceled work retains pending
// reasons for a cooldown-bounded retry.
func (c *Cadence) Complete(now time.Duration, sample observe.Sample, complete bool) {
	now = c.monotonic(now)
	if !c.inFlight {
		return
	}
	c.inFlight = false
	if !complete || !comparableSample(sample) {
		return
	}
	if c.lastHeartbeat != nil && !sameSubject(*c.lastHeartbeat, sample) {
		c.resetComparableState()
		c.add(DeepInitialBaseline)
		return
	}
	copy := sample
	c.baseline, c.baselineAt = &copy, now
	for reason, seen := range c.pending {
		if seen <= c.inFlightAt {
			delete(c.pending, reason)
		}
	}
}

func (c *Cadence) request(now time.Duration, criticalBypass bool) *DeepRequest {
	if c.inFlight || len(c.pending) == 0 {
		return nil
	}
	if c.hasAttempt && now-c.lastAttempt < c.config.Cooldown && !criticalBypass {
		return nil
	}
	c.inFlight, c.lastAttempt, c.hasAttempt, c.inFlightAt = true, now, true, c.event
	if criticalBypass {
		c.criticalBypass = true
	}
	reasons := make([]DeepReason, 0, len(c.pending))
	for reason := range c.pending {
		reasons = append(reasons, reason)
	}
	sort.Slice(reasons, func(i, j int) bool { return reasons[i] < reasons[j] })
	return &DeepRequest{Reasons: reasons}
}

func (c *Cadence) resetComparableState() {
	c.lastHeartbeat, c.baseline = nil, nil
	c.baselineAt, c.lastLevel, c.criticalBypass = 0, "", false
	c.pending = make(map[DeepReason]uint64)
}
func (c *Cadence) add(reason DeepReason) { c.event++; c.pending[reason] = c.event }
func (c *Cadence) monotonic(now time.Duration) time.Duration {
	if !c.seenNow || now >= c.lastNow {
		c.lastNow, c.seenNow = now, true
	}
	return c.lastNow
}
func comparableSample(sample observe.Sample) bool {
	if sample.Coverage != observe.CoverageComplete || !sample.SizeComplete || sample.VolumeID == "" || sample.Basis != observe.BasisStatfs || sample.AvailBytes < 0 {
		return false
	}
	return sample.Level == observe.LevelOK || sample.Level == observe.LevelWarn || sample.Level == observe.LevelCritical
}
func sameSubject(a, b observe.Sample) bool { return a.VolumeID == b.VolumeID && a.Basis == b.Basis }
func loss(from, to observe.Sample) int64 {
	if to.AvailBytes >= from.AvailBytes {
		return 0
	}
	return from.AvailBytes - to.AvailBytes
}
func pressureRank(level string) int {
	if level == observe.LevelCritical {
		return 2
	}
	if level == observe.LevelWarn {
		return 1
	}
	return 0
}
