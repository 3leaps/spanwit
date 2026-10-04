package observe

import "time"

// Clock is injectable for deterministic tests.
type Clock interface {
	Now() time.Time
}

// RealClock uses the system wall clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

// FixedClock returns a fixed time (tests may advance by replacing NowFn).
type FixedClock struct {
	T time.Time
}

func (c *FixedClock) Now() time.Time {
	if c.T.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return c.T.UTC()
}

func (c *FixedClock) Advance(d time.Duration) { c.T = c.T.Add(d) }
