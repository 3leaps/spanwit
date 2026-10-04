package inventory

import (
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	RecordHeader    = "spanwit.inventory.header.v1"
	RecordEntry     = "spanwit.inventory.entry.v1"
	RecordGap       = "spanwit.inventory.gap.v1"
	RecordSummary   = "spanwit.inventory.summary.v1"
	RecordDirectory = "spanwit.inventory.directory.v1"
)

// JSONLSink writes the producer-owned v0 typed stream. The portable profile is
// intentionally producer-scoped pending field-catalog and protection review.
type JSONLSink struct {
	mu      sync.Mutex
	encoder *json.Encoder
	runID   string
	seq     int64
	failed  error
	now     func() time.Time
}

// NewJSONLSink creates a streaming encoder. It does not buffer the result set
// or create an artifact file.
func NewJSONLSink(w io.Writer) *JSONLSink {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return &JSONLSink{encoder: encoder, now: time.Now}
}

func (s *JSONLSink) Header(value Header) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.RunID == "" {
		return errors.New("header run id is required")
	}
	s.runID = value.RunID
	ts := value.CapturedAt
	if ts.IsZero() {
		ts = s.now()
	}
	return s.writeLocked(RecordHeader, ts, value)
}

func (s *JSONLSink) Entry(value Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(RecordEntry, s.now(), value)
}

func (s *JSONLSink) Gap(value Gap) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(RecordGap, s.now(), value)
}

func (s *JSONLSink) Directory(value Directory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(RecordDirectory, s.now(), value)
}

func (s *JSONLSink) Summary(value Summary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := value.TerminalObservedAt
	if ts.IsZero() {
		return errors.New("summary terminal observation timestamp is required")
	}
	return s.writeLocked(RecordSummary, ts, value)
}

// AuditHeader writes the directory-audit header.v2 record.
func (s *JSONLSink) AuditHeader(value AuditHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value.RunID == "" {
		return errors.New("header run id is required")
	}
	s.runID = value.RunID
	ts := value.CapturedAt
	if ts.IsZero() {
		ts = s.now()
	}
	return s.writeLocked(RecordHeaderV2, ts, value)
}

// AuditDirectory writes one directory.v2 record.
func (s *JSONLSink) AuditDirectory(value AuditDirectory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(RecordDirectoryV2, s.now(), value)
}

// AuditSummary writes the terminal summary.v2 record.
func (s *JSONLSink) AuditSummary(value AuditSummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := value.TerminalObservedAt
	if ts.IsZero() {
		return errors.New("summary terminal observation timestamp is required")
	}
	return s.writeLocked(RecordSummaryV2, ts, value)
}

func (s *JSONLSink) writeLocked(recordType string, ts time.Time, data any) error {
	if s.failed != nil {
		return s.failed
	}
	record := struct {
		Type  string `json:"type"`
		RunID string `json:"run_id"`
		Seq   int64  `json:"seq"`
		TS    string `json:"ts"`
		Data  any    `json:"data"`
	}{
		Type: recordType, RunID: s.runID, Seq: s.seq,
		TS: ts.UTC().Format(time.RFC3339Nano), Data: data,
	}
	if err := s.encoder.Encode(record); err != nil {
		s.failed = err
		return err
	}
	s.seq++
	return nil
}
