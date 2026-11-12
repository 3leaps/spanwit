//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/observe"
)

func TestAttributionDurableBaselineAndPartialRetention(t *testing.T) {
	root := privateDir(t)
	c, err := Open(privateDir(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	if _, err = c.Sample(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	o, err := newTestRootObserver(t, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	complete, err := c.Attribute(context.Background(), o, []DeepReason{DeepInitialBaseline}, nil)
	if err != nil || !complete {
		t.Fatalf("%v %v", complete, err)
	}
	first := c.Status().Attribution
	if first == nil || first.Rows[0].DeltaBytes != nil {
		t.Fatalf("%+v", first)
	}
	if _, ok, err := c.attributionStream.Head(first.RecordID); err != nil || !ok {
		t.Fatalf("not committed: %v %v", ok, err)
	}
	// Mutating a caller's snapshot must not alter either cached state or baseline.
	*first.Rows[0].Observation.AllocatedBytes = 123456
	first.Reasons[0] = "bad"
	if c.Status().Attribution.Reasons[0] != DeepInitialBaseline || *c.Status().Attribution.Rows[0].Observation.AllocatedBytes == 123456 {
		t.Fatal("mutable cached snapshot")
	}
	complete, err = c.Attribute(context.Background(), o, []DeepReason{DeepHourlyBaseline}, map[string]bool{root: true})
	if err != nil || complete || c.Status().Attribution.Rows[0].Observation.Coverage != "deferred" {
		t.Fatalf("%v %v", complete, err)
	}
	if c.baselines[root].recordID != first.RecordID {
		t.Fatal("partial replaced baseline")
	}
	if err := os.WriteFile(filepath.Join(root, "growth"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	complete, err = c.Attribute(context.Background(), o, []DeepReason{DeepHourlyBaseline}, nil)
	if err != nil || !complete {
		t.Fatalf("%v %v", complete, err)
	}
	latest := c.Status().Attribution
	if latest.Rows[0].DeltaBytes == nil || latest.Rows[0].BaselineRecordID != first.RecordID {
		t.Fatal("lost comparable baseline")
	}
	if _, err = c.Sample(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if c.Status().Attribution.RecordID != latest.RecordID {
		t.Fatal("heartbeat erased attribution")
	}
	w := httptest.NewRecorder()
	Handler(c).ServeHTTP(w, httptest.NewRequest("GET", "/v1/status", nil))
	if err := contract.ValidateJSON(spanwitschema.SpanwitDaemonResponseV1, w.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(latest)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitRootAttributionV0, raw); err != nil {
		t.Fatal(err)
	}
}

type oversizedPathSampler struct{}

func (oversizedPathSampler) Sample(ctx context.Context, path string) (observe.Sample, error) {
	s, err := (observe.CapacitySampler{}).Sample(ctx, path)
	s.MountPath = strings.Repeat("\x01", MaxResponseBytes)
	return s, err
}

func TestOversizedStatusWithheldBeforeAppend(t *testing.T) {
	root := privateDir(t)
	c, err := Open(privateDir(t), oversizedPathSampler{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	j := &uncertainJournal{sampleJournal: c.stream}
	c.stream = j
	status, err := c.Sample(context.Background(), root)
	if err == nil || j.calls != 0 || status.Sample != nil || status.State != "sample_unavailable" {
		t.Fatalf("oversized publication: %+v calls=%d err=%v", status, j.calls, err)
	}
	// A known pre-append size rejection does not falsely latch an uncertain write.
	c.stream = j.sampleJournal
	c.sampler = observe.CapacitySampler{}
	if _, err := c.Sample(context.Background(), root); err != nil {
		t.Fatal(err)
	}
}

func TestAttributionUncertainAppendLatchesAllWrites(t *testing.T) {
	root := privateDir(t)
	c, err := Open(privateDir(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	if _, err = c.Sample(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	o, err := newTestRootObserver(t, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Attribute(context.Background(), o, []DeepReason{DeepInitialBaseline}, nil); err != nil {
		t.Fatal(err)
	}
	first := c.Status().Attribution
	j := &uncertainJournal{sampleJournal: c.attributionStream}
	c.attributionStream = j
	if _, err = c.Attribute(context.Background(), o, []DeepReason{DeepHourlyBaseline}, nil); err == nil {
		t.Fatal("accepted unknown append")
	}
	if c.Status().State != "journal_unavailable" || c.Status().Attribution.RecordID != first.RecordID || c.baselines[root].recordID != first.RecordID {
		t.Fatal("published unknown outcome")
	}
	if _, ok, err := j.Head(j.id); err != nil || !ok {
		t.Fatal("fault was not after append", err)
	}
	if _, err = c.Attribute(context.Background(), o, []DeepReason{DeepHourlyBaseline}, nil); err == nil || j.calls != 1 {
		t.Fatal("retried uncertain append")
	}
	if _, err = c.Sample(context.Background(), root); err == nil {
		t.Fatal("capacity write admitted after uncertain attribution")
	}
}

func TestCoordinateRootsPublishesInitialObservation(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- coordinateRoots(ctx, c, root, make(chan time.Time), make(chan error), o) }()
	deadline := time.Now().Add(5 * time.Second)
	for c.Status().Attribution == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if c.Status().Attribution == nil {
		t.Fatal("no initial attribution")
	}
}
