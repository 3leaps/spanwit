//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/inventory"
)

func TestAttributionRestartKeepsHistoryAndResetsBaseline(t *testing.T) {
	root, dir := privateDir(t), privateDir(t)
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	o, err := newTestRootObserver(t, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Sample(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Attribute(context.Background(), o, []DeepReason{DeepInitialBaseline}, nil); err != nil {
		t.Fatal(err)
	}
	first := c.Status().Attribution
	if err = o.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, next)
	if next.Status().Attribution != nil || len(next.baselines) != 0 {
		t.Fatal("restart adopted unverified runtime baseline")
	}
	observer, err := newTestRootObserver(t, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = next.Sample(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err = next.Attribute(context.Background(), observer, []DeepReason{DeepInitialBaseline}, nil); err != nil {
		t.Fatal(err)
	}
	current := next.Status().Attribution.Rows[0]
	if current.DeltaBytes != nil || current.BaselineRecordID != "" || current.Observation.Identity == first.Rows[0].Observation.Identity {
		t.Fatal("restart reused baseline/generation")
	}
	if _, ok, err := next.attributionStream.Head(first.RecordID); err != nil || !ok {
		t.Fatal("restart lost durable attribution history", err)
	}
}

func TestAttributionCancellationKeepsCachedReadsAndBaseline(t *testing.T) {
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
	if _, err = c.Sample(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Attribute(context.Background(), o, []DeepReason{DeepInitialBaseline}, nil); err != nil {
		t.Fatal(err)
	}
	first := c.Status().Attribution
	entered := make(chan struct{})
	o.run = func(ctx context.Context, _ inventory.Options, _ inventory.Sink) (inventory.Summary, error) {
		close(entered)
		<-ctx.Done()
		return inventory.Summary{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Attribute(ctx, o, []DeepReason{DeepHourlyBaseline}, nil); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("walk did not start")
	}
	read := make(chan Status, 1)
	go func() { read <- c.Status() }()
	select {
	case status := <-read:
		if status.Attribution.RecordID != first.RecordID {
			t.Fatal("inflight walk replaced cache")
		}
	case <-time.After(time.Second):
		t.Fatal("cached read blocked behind attribution")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("attribution did not cancel")
	}
	if c.Status().Attribution.RecordID != first.RecordID || c.baselines[root].recordID != first.RecordID {
		t.Fatal("canceled walk changed committed state")
	}
}
