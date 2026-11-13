//go:build darwin || linux

package daemon

import (
	"reflect"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/observe"
)

func cadenceSample(avail int64, level string) observe.Sample {
	return observe.Sample{VolumeID: "volume-a", Basis: observe.BasisStatfs, Coverage: observe.CoverageComplete, SizeComplete: true, AvailBytes: avail, Level: level}
}

func requireReasons(t *testing.T, request *DeepRequest, want ...DeepReason) {
	t.Helper()
	if request == nil || !reflect.DeepEqual(request.Reasons, want) {
		t.Fatalf("request=%+v, want reasons %v", request, want)
	}
}

func TestCadenceInitialBaselineAndCompletion(t *testing.T) {
	c := NewCadence(CadenceConfig{})
	s := cadenceSample(20*gib, observe.LevelOK)
	requireReasons(t, c.Heartbeat(0, s), DeepInitialBaseline)
	c.Complete(time.Minute, s, true)
	if got := c.Heartbeat(2*time.Minute, s); got != nil {
		t.Fatalf("unexpected request: %+v", got)
	}
}

func TestCadenceLossAndHourlyTriggersAreInclusiveAndCoalesced(t *testing.T) {
	c := NewCadence(CadenceConfig{})
	base := cadenceSample(20*gib, observe.LevelOK)
	requireReasons(t, c.Heartbeat(0, base), DeepInitialBaseline)
	c.Complete(0, base, true)
	next := cadenceSample(15*gib, observe.LevelOK)
	requireReasons(t, c.Heartbeat(15*time.Minute, next), DeepBaselineLoss, DeepHeartbeatLoss)
	c.Complete(15*time.Minute, next, true)
	if got := c.Heartbeat(15*time.Minute+time.Nanosecond, cadenceSample(13*gib+1, observe.LevelOK)); got != nil {
		t.Fatalf("sub-threshold loss triggered: %+v", got)
	}
	requireReasons(t, c.Heartbeat(75*time.Minute, cadenceSample(13*gib+1, observe.LevelOK)), DeepHourlyBaseline)
}

func TestCadenceFailureRetainsBaselineAndReasonsUntilCooldown(t *testing.T) {
	c := NewCadence(CadenceConfig{})
	base := cadenceSample(20*gib, observe.LevelOK)
	requireReasons(t, c.Heartbeat(0, base), DeepInitialBaseline)
	c.Complete(0, base, true)
	lost := cadenceSample(15*gib, observe.LevelOK)
	requireReasons(t, c.Heartbeat(15*time.Minute, lost), DeepBaselineLoss, DeepHeartbeatLoss)
	c.Complete(16*time.Minute, observe.Sample{}, false)
	if got := c.Heartbeat(20*time.Minute, lost); got != nil {
		t.Fatalf("cooldown bypassed: %+v", got)
	}
	requireReasons(t, c.Heartbeat(30*time.Minute, lost), DeepBaselineLoss, DeepHeartbeatLoss)
	c.Complete(31*time.Minute, lost, true)
}

func TestCadenceCriticalTransitionBypassesCooldownOnlyOnce(t *testing.T) {
	c := NewCadence(CadenceConfig{})
	base := cadenceSample(20*gib, observe.LevelOK)
	requireReasons(t, c.Heartbeat(0, base), DeepInitialBaseline)
	c.Complete(0, base, true)
	c.add(DeepHourlyBaseline)
	requireReasons(t, c.request(15*time.Minute, false), DeepHourlyBaseline)
	c.Complete(15*time.Minute, base, true)
	critical := cadenceSample(20*gib, observe.LevelCritical)
	requireReasons(t, c.Heartbeat(16*time.Minute, critical), DeepPressureWorsened)
	c.Complete(16*time.Minute, critical, false)
	if got := c.Heartbeat(17*time.Minute, critical); got != nil {
		t.Fatalf("unchanged critical state spun: %+v", got)
	}
	if got := c.Heartbeat(18*time.Minute, cadenceSample(20*gib, observe.LevelWarn)); got != nil {
		t.Fatalf("unexpected warning request during cooldown: %+v", got)
	}
	requireReasons(t, c.Heartbeat(19*time.Minute, critical), DeepPressureWorsened)
}

func TestCadenceRejectsIncompleteAndIncomparableSamples(t *testing.T) {
	c := NewCadence(CadenceConfig{})
	incomplete := cadenceSample(20*gib, observe.LevelOK)
	incomplete.Coverage = observe.CoveragePartial
	if got := c.Heartbeat(0, incomplete); got != nil {
		t.Fatalf("partial sample triggered: %+v", got)
	}
	base := cadenceSample(20*gib, observe.LevelOK)
	requireReasons(t, c.Heartbeat(time.Minute, base), DeepInitialBaseline)
	c.Complete(time.Minute, base, true)
	other := cadenceSample(10*gib, observe.LevelCritical)
	other.VolumeID = "volume-b"
	requireReasons(t, c.Heartbeat(16*time.Minute, other), DeepInitialBaseline, DeepPressureWorsened)
}

func TestCadenceInvalidHeartbeatCannotReleasePendingRetry(t *testing.T) {
	c := NewCadence(CadenceConfig{})
	s := cadenceSample(20*gib, observe.LevelOK)
	requireReasons(t, c.Heartbeat(0, s), DeepInitialBaseline)
	c.Complete(time.Minute, observe.Sample{}, false)
	invalid := cadenceSample(-1, observe.LevelOK)
	if got := c.Heartbeat(20*time.Minute, invalid); got != nil {
		t.Fatalf("invalid sample released retry: %+v", got)
	}
	unknownBasis := cadenceSample(20*gib, observe.LevelOK)
	unknownBasis.Basis = "unreviewed"
	if got := c.Heartbeat(20*time.Minute, unknownBasis); got != nil {
		t.Fatalf("unknown basis released retry: %+v", got)
	}
	unknownLevel := cadenceSample(20*gib, "unknown")
	if got := c.Heartbeat(20*time.Minute, unknownLevel); got != nil {
		t.Fatalf("unknown level released retry: %+v", got)
	}
	requireReasons(t, c.Heartbeat(15*time.Minute+time.Nanosecond, s), DeepInitialBaseline)
}

func TestCadenceCompletionRetainsReasonsAddedAfterAttempt(t *testing.T) {
	c := NewCadence(CadenceConfig{})
	base := cadenceSample(20*gib, observe.LevelOK)
	requireReasons(t, c.Heartbeat(0, base), DeepInitialBaseline)
	// The caller is single-owner, but it can receive another heartbeat while a
	// deep operation is outstanding. Keep that later evidence after completion.
	if got := c.Heartbeat(time.Minute, cadenceSample(18*gib, observe.LevelOK)); got != nil {
		t.Fatalf("in-flight request repeated: %+v", got)
	}
	c.Complete(2*time.Minute, cadenceSample(18*gib, observe.LevelOK), true)
	if got := c.Heartbeat(3*time.Minute, cadenceSample(18*gib, observe.LevelOK)); got != nil {
		t.Fatalf("cooldown released retained reason: %+v", got)
	}
	requireReasons(t, c.Heartbeat(15*time.Minute, cadenceSample(18*gib, observe.LevelOK)), DeepHeartbeatLoss)
}

func TestCadenceClockRollbackDoesNotRetryEarly(t *testing.T) {
	c := NewCadence(CadenceConfig{})
	s := cadenceSample(20*gib, observe.LevelOK)
	requireReasons(t, c.Heartbeat(10*time.Minute, s), DeepInitialBaseline)
	c.Complete(10*time.Minute, observe.Sample{}, false)
	if got := c.Heartbeat(time.Minute, s); got != nil {
		t.Fatalf("rollback retried: %+v", got)
	}
	requireReasons(t, c.Heartbeat(25*time.Minute, s), DeepInitialBaseline)
}
