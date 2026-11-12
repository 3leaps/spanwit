//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/3leaps/recordprims"
	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/google/uuid"
)

const AttributionSchema = "https://schemas.3leaps.dev/spanwit/root-attribution/v0.json"

type AttributionRow struct {
	Observation      RootObservation `json:"observation"`
	DeltaBytes       *int64          `json:"delta_bytes,omitempty"`
	BaselineRecordID string          `json:"baseline_record_id,omitempty"`
}

// Attribution records a bounded observation attempt tied to the triggering
// capacity record. Rows are independent and must not be summed into a claim
// about volume free-space loss. A complete baseline is kept separately from
// the latest attempt; partial observations never overwrite it.
type Attribution struct {
	Schema           string           `json:"$schema"`
	Version          int              `json:"version"`
	RecordID         string           `json:"record_id"`
	CapacityRecordID string           `json:"capacity_record_id"`
	CapturedAt       time.Time        `json:"captured_at"`
	Complete         bool             `json:"complete"`
	Reasons          []DeepReason     `json:"reasons"`
	Rows             []AttributionRow `json:"rows"`
}

type rootBaseline struct {
	observation RootObservation
	recordID    string
}

// Attribute is called by the single scheduler after a successful cheap sample.
// It leaves cached reads available while walking and commits before publishing.
// Canceled batches produce no record. Unknown append outcomes latch all writes.
func (c *Coordinator) Attribute(ctx context.Context, observer *RootObserver, reasons []DeepReason, active map[string]bool) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := c.Status()
	if c.store == nil {
		return false, fmt.Errorf("coordinator_closed")
	}
	if status.State != "observing" || status.Sample == nil {
		return false, fmt.Errorf("capacity observation unavailable")
	}
	if observer == nil || len(reasons) == 0 || len(reasons) > 5 {
		return false, fmt.Errorf("invalid attribution request")
	}
	rows, err := observer.Observe(ctx, active)
	if err != nil {
		return false, err
	}
	a := &Attribution{Schema: AttributionSchema, Version: 0, RecordID: uuid.NewString(), CapacityRecordID: status.RecordID,
		CapturedAt: time.Now().UTC().Truncate(time.Microsecond), Complete: true, Reasons: append([]DeepReason(nil), reasons...), Rows: make([]AttributionRow, 0, len(rows))}
	for _, row := range rows {
		out := AttributionRow{Observation: row}
		if old, ok := c.baselines[row.Path]; ok {
			if delta, comparable := RootDelta(old.observation, row); comparable {
				out.DeltaBytes = &delta
				out.BaselineRecordID = old.recordID
			}
		}
		if row.Coverage != "complete" {
			a.Complete = false
		}
		a.Rows = append(a.Rows, out)
	}
	if c.attributionStream == nil {
		stream, err := c.store.OpenStream("attribution", recordprims.ModeSingleRevision)
		if err != nil {
			c.setState("journal_unavailable")
			return false, err
		}
		c.attributionStream = stream
	}
	payload, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	if len(payload) > 32*1024 {
		return false, fmt.Errorf("attribution_payload_too_large")
	}
	if err = contract.ValidateJSON(spanwitschema.SpanwitRootAttributionV0, payload); err != nil {
		return false, err
	}
	status.Attribution = a
	status.AttributionState = "partial"
	if a.Complete {
		status.AttributionState = "complete"
	}
	if err := validateStatusSize(status); err != nil {
		return false, err
	}
	record := map[string]any{"v": 1, "stream": "attribution", "record_id": a.RecordID, "revision": 1, "payload_contract": "contract: root-attribution/v0", "payload": a,
		"produced_at": map[string]string{"instant": a.CapturedAt.Format("2006-01-02T15:04:05.000000Z"), "source": "producer_clock"},
		"provenance":  map[string]string{"actor_kind": "process", "actor": "root-observer"}, "classification": map[string]string{"retention": "standard", "sensitivity": "1-confidential"}}
	raw, err := json.Marshal(record)
	if err != nil {
		return false, err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	receipt, err := c.attributionStream.Append(ctx, recordprims.Write{Record: raw, IdempotencyKey: a.RecordID})
	if err != nil || receipt.Outcome != recordprims.OutcomeAppended {
		c.setState("journal_unavailable")
		if err == nil {
			err = fmt.Errorf("journal outcome %s", receipt.Outcome)
		}
		return false, err
	}
	baselines := make(map[string]rootBaseline, len(rows))
	for _, row := range rows {
		if row.Coverage == "complete" {
			baselines[row.Path] = rootBaseline{row, a.RecordID}
		} else if old, ok := c.baselines[row.Path]; ok {
			baselines[row.Path] = old
		}
	}
	c.baselines = baselines
	c.status.Store(&status)
	return a.Complete, nil
}

func (c *Coordinator) setAttributionState(state string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.Status()
	s.AttributionState = state
	c.status.Store(&s)
}

func cloneAttribution(a *Attribution) *Attribution {
	if a == nil {
		return nil
	}
	copy := *a
	copy.Reasons = append([]DeepReason(nil), a.Reasons...)
	copy.Rows = append([]AttributionRow(nil), a.Rows...)
	for i := range copy.Rows {
		if n := copy.Rows[i].DeltaBytes; n != nil {
			v := *n
			copy.Rows[i].DeltaBytes = &v
		}
		if n := copy.Rows[i].Observation.AllocatedBytes; n != nil {
			v := *n
			copy.Rows[i].Observation.AllocatedBytes = &v
		}
	}
	return &copy
}
