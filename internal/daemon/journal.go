//go:build darwin || linux

// Package daemon provides an observation-only coordinator. It has no prune path.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/3leaps/recordprims"
	"github.com/3leaps/spanwit/internal/observe"
	"github.com/google/uuid"
)

const ProtocolVersion = 1
const SampleSchema = "https://schemas.3leaps.dev/spanwit/growth-sample/v0.json"
const MaxResponseBytes = 65536

// Status is a cached observation, not an authorization or reclaim estimate.
type Status struct {
	Protocol         int             `json:"protocol"`
	State            string          `json:"state"`
	Sample           *observe.Sample `json:"sample,omitempty"`
	RecordID         string          `json:"record_id,omitempty"`
	Attribution      *Attribution    `json:"attribution,omitempty"`
	AttributionState string          `json:"attribution_state,omitempty"`
}

type Coordinator struct {
	owner             *Ownership
	store             *recordprims.Store
	stream            sampleJournal
	sampler           observe.Sampler
	mu                sync.Mutex
	status            atomic.Pointer[Status]
	baselines         map[string]rootBaseline
	attributionStream sampleJournal
}

type sampleJournal interface {
	Append(context.Context, recordprims.Write) (recordprims.Receipt, error)
	Head(string) (recordprims.HeadInfo, bool, error)
}

// Open acquires ownership before opening or initializing any application state.
// A pre-existing incomplete journal fails closed; it is never reinitialized.
func Open(dir string, sampler observe.Sampler) (*Coordinator, error) {
	owner, err := Acquire(dir)
	if err != nil {
		return nil, err
	}
	good := false
	defer func() {
		if !good {
			_ = owner.Close()
		}
	}()
	journal := filepath.Join(owner.Dir, "journal")
	err = os.Mkdir(journal, 0700)
	if err == nil {
		if err = recordprims.Init(journal, recordprims.InitOptions{}); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	// Refuse oversized stores before recordprims replays or rebuilds indexes.
	if _, err := journalUsage(context.Background(), journal, journalWatermark); err != nil {
		return nil, err
	}
	store, err := recordprims.Open(journal, recordprims.OpenOptions{})
	if err != nil {
		return nil, err
	}
	stream, err := store.OpenStream("growth", recordprims.ModeSingleRevision)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if sampler == nil {
		sampler = observe.CapacitySampler{}
	}
	good = true
	c := &Coordinator{owner: owner, store: store, stream: stream, sampler: sampler}
	c.status.Store(&Status{Protocol: ProtocolVersion, State: "awaiting_sample"})
	return c, nil
}

func (c *Coordinator) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return nil
	}
	err := c.store.Close()
	c.store = nil
	return errors.Join(err, c.owner.Close())
}
func (c *Coordinator) Status() Status {
	s := *c.status.Load()
	if s.Sample != nil {
		sample := *s.Sample
		s.Sample = &sample
	}
	s.Attribution = cloneAttribution(s.Attribution)
	return s
}

// Sample commits the observation before publishing it to readers. Failure leaves
// the previous sample visible with degraded state, never a false success.
func (c *Coordinator) Sample(ctx context.Context, path string) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return Status{}, fmt.Errorf("coordinator_closed")
	}
	if journalStopped(c.Status().State) {
		return c.Status(), fmt.Errorf("%s: operator intervention required", c.Status().State)
	}
	if err := c.admitJournal(ctx); err != nil {
		return c.Status(), err
	}
	sample, err := c.sampler.Sample(ctx, path)
	if err != nil {
		c.setState("sample_unavailable")
		return c.Status(), err
	}
	if err = ctx.Err(); err != nil {
		return c.Status(), err
	}
	sample.CapturedAt = time.Now().UTC().Truncate(time.Microsecond)
	id := uuid.NewString()
	status := c.Status()
	status.State, status.Sample, status.RecordID = "observing", &sample, id
	if err := validateStatusSize(status); err != nil {
		c.setState("sample_unavailable")
		return c.Status(), err
	}
	payload := struct {
		Schema  string         `json:"$schema"`
		Version int            `json:"version"`
		Sample  observe.Sample `json:"sample"`
	}{SampleSchema, 0, sample}
	record := map[string]any{"v": 1, "stream": "growth", "record_id": id, "revision": 1, "payload_contract": "contract: growth-sample/v0", "payload": payload, "produced_at": map[string]string{"instant": sample.CapturedAt.Format("2006-01-02T15:04:05.000000Z"), "source": "producer_clock"}, "provenance": map[string]string{"actor_kind": "process", "actor": "capacity-sampler"}, "classification": map[string]string{"retention": "standard", "sensitivity": "1-confidential"}}
	raw, err := json.Marshal(record)
	if err != nil {
		return c.Status(), err
	}
	receipt, err := c.stream.Append(ctx, recordprims.Write{Record: raw, IdempotencyKey: id})
	if err != nil {
		c.setState("journal_unavailable")
		return c.Status(), err
	}
	if receipt.Outcome != recordprims.OutcomeAppended {
		c.setState("journal_unavailable")
		return c.Status(), fmt.Errorf("journal outcome %s", receipt.Outcome)
	}
	c.status.Store(&status)
	return c.Status(), nil
}

func (c *Coordinator) setState(state string) { s := c.Status(); s.State = state; c.status.Store(&s) }

func validateStatusSize(status Status) error {
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	// Leave room for bounded failure-state labels without invalidating a
	// previously published snapshot when a later observation fails.
	if len(raw) > MaxResponseBytes-1024 {
		return fmt.Errorf("status_response_too_large")
	}
	return nil
}
