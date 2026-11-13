//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fixedActivity struct {
	roots map[string]bool
	err   error
}

func (p fixedActivity) Probe(context.Context, []string) (map[string]bool, error) {
	return p.roots, p.err
}

func TestCoordinatorActivityDefersOnlyPositiveObservations(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "positive", true: "failed_probe"}[failed], func(t *testing.T) {
			root := privateDir(t)
			c, err := Open(privateDir(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer closeTest(t, c)
			o, err := newTestRootObserver(t, []string{root})
			if err != nil {
				t.Fatal(err)
			}
			probe := fixedActivity{roots: map[string]bool{root: true}}
			if failed {
				probe.err = errors.New("partial probe")
			} else {
				o.run = nil
			} // Positive evidence must prevent any walk.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- coordinateRootsWithActivity(ctx, c, root, make(chan time.Time), make(chan error), o, probe)
			}()
			deadline := time.Now().Add(5 * time.Second)
			for c.Status().Attribution == nil && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			a := c.Status().Attribution
			if a == nil {
				t.Fatal("no attribution")
			}
			row := a.Rows[0].Observation
			if failed {
				if row.Activity != "unknown" || row.Coverage != "complete" {
					t.Fatalf("failed probe misreported: %+v", row)
				}
			} else {
				if row.Activity != "active" || row.Coverage != "deferred" || row.AllocatedBytes != nil || len(c.baselines) != 0 {
					t.Fatalf("active root scanned/adopted: %+v", row)
				}
			}
		})
	}
}
