package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/3leaps/recordprims"
	"github.com/google/uuid"
)

// Approval journal streams. All live in the coordinator's store and are
// written only while coordinator.lock is held (the daemon is stopped).
const (
	approvalStreamID  = "approval"
	executionStreamID = "execution"
	identityStreamID  = "identity"
	planStreamID      = "plan"
	identityRecordID  = "journal"

	ApprovalRecordSchema  = "spanwit-approval-record/v0"
	ExecutionRecordSchema = "spanwit-execution-record/v0"
	maxApprovalRecord     = 256 << 10
)

// Approval states (N7).
const (
	ApprovalRequested = "requested"
	ApprovalIssued    = "issued"
	ApprovalRevoked   = "revoked"
	ApprovalConsumed  = "consumed"
)

// Refusals the journal itself raises. Callers map them onto the contract
// vocabulary; persistence uncertainty always latches the coordinator.
var (
	ErrPersistenceUncertain = errors.New("persistence_uncertain")
	ErrApprovalTransition   = errors.New("approval transition not allowed from current state")
	ErrApprovalNotFound     = errors.New("approval not found")
	ErrApprovalConflict     = errors.New("approval record changed concurrently")
)

// ApprovalDecision is display/audit data recorded at issue. It is never read
// back as authority: execution re-verifies the stored signature.
type ApprovalDecision struct {
	Principal string `json:"principal"`
	KeyType   string `json:"key_type"`
	Assurance string `json:"assurance"`
	DecidedAt string `json:"decided_at"`
}

// ApprovalConsumption records the serialized grant-use decision.
type ApprovalConsumption struct {
	OperationID        string          `json:"operation_id"`
	RequestFingerprint string          `json:"request_fingerprint"`
	Plan               recordprims.Pin `json:"plan"` // plan stream record holding the exact plan bytes
	ConsumedAt         string          `json:"consumed_at"`
}

// ApprovalRecord is the payload of one revision of an approval record.
type ApprovalRecord struct {
	Schema      string               `json:"schema"`
	ApprovalID  string               `json:"approval_id"`
	State       string               `json:"state"`
	Payload     []byte               `json:"payload"` // canonical signed-payload bytes, exactly as requested
	Signature   string               `json:"signature,omitempty"`
	Decision    *ApprovalDecision    `json:"decision,omitempty"`
	Consumption *ApprovalConsumption `json:"consumption,omitempty"`
	RevokedAt   string               `json:"revoked_at,omitempty"`
	UpdatedAt   string               `json:"updated_at"`
}

// ApprovalHead is an approval record with the pin of the revision it was
// read from (the expected predecessor for the next transition).
type ApprovalHead struct {
	Record ApprovalRecord
	Pin    recordprims.Pin
}

func (c *Coordinator) openStream(id string, mode recordprims.Mode) (*recordprims.Stream, error) {
	if c.store == nil {
		return nil, ErrPersistenceUncertain
	}
	st, err := c.store.OpenStream(id, mode)
	if err != nil {
		c.setState("journal_unavailable")
		return nil, err
	}
	return st, nil
}

// readPinned returns the record document stored at pin.
func readPinned(ctx context.Context, st *recordprims.Stream, pin recordprims.Pin) ([]byte, error) {
	sink := &frameCollector{}
	res, err := st.Release(ctx, recordprims.UseRequest{
		Purpose:   recordprims.PurposeExport,
		Selection: []recordprims.Pin{pin},
		Closure:   recordprims.ClosureMetadataOnly,
	}, &limitedCollector{inner: sink, limit: maxApprovalRecord})
	if err != nil {
		return nil, err
	}
	if res.Outcome != recordprims.ReleaseReleased || !sink.hasFrame {
		return nil, fmt.Errorf("record %s/%s@%d unreadable: %s", pin.Stream, pin.RecordID, pin.Revision, res.Outcome)
	}
	return sink.lastFrame, nil
}

type limitedCollector struct {
	inner *frameCollector
	limit int64
}

func (l *limitedCollector) Frame(ctx context.Context, h recordprims.FrameHeader, r io.Reader) error {
	if h.Kind != recordprims.FrameKindRecord {
		return drain(r)
	}
	b, err := io.ReadAll(io.LimitReader(r, l.limit+1))
	if err != nil {
		return err
	}
	if int64(len(b)) > l.limit {
		return fmt.Errorf("record larger than %d bytes", l.limit)
	}
	l.inner.lastFrame, l.inner.hasFrame = b, true
	return nil
}

type recordEnvelope struct {
	RecordID string          `json:"record_id"`
	Revision uint64          `json:"revision"`
	Payload  json.RawMessage `json:"payload"`
}

// write appends one record revision and returns the receipt pin. Only the
// outcome named by want is success; any other outcome or error latches the
// coordinator (journal_unavailable) and returns ErrPersistenceUncertain,
// except a revision conflict, which is reported as ErrApprovalConflict
// (determinate: nothing was written).
func (c *Coordinator) write(ctx context.Context, st *recordprims.Stream, recordID string, revision uint64,
	supersedes *recordprims.Pin, contract string, payload any, key string, want recordprims.WriteOutcome) (recordprims.Pin, error) {
	return c.writeContent(ctx, st, recordID, revision, supersedes, contract, map[string]any{"payload": payload}, nil, key, want)
}

// writeContent is write with explicit record content: either a "payload"
// member or a "body" reference (with its publication lease), never both.
func (c *Coordinator) writeContent(ctx context.Context, st *recordprims.Stream, recordID string, revision uint64,
	supersedes *recordprims.Pin, contract string, content map[string]any, lease *recordprims.PublicationLease,
	key string, want recordprims.WriteOutcome) (recordprims.Pin, error) {
	if err := c.admitJournal(ctx); err != nil {
		return recordprims.Pin{}, fmt.Errorf("%w: %v", ErrPersistenceUncertain, err)
	}
	now := nowInstant()
	record := map[string]any{
		"v":                1,
		"stream":           st.ID(),
		"record_id":        recordID,
		"revision":         revision,
		"payload_contract": contract,
		"produced_at":      map[string]string{"instant": now, "source": "producer_clock"},
		"provenance":       map[string]string{"actor_kind": "process", "actor": "spanwit-approval"},
		"classification":   map[string]string{"retention": "standard", "sensitivity": "1-confidential"},
	}
	for k, v := range content {
		record[k] = v
	}
	if supersedes != nil {
		record["supersedes"] = map[string]any{"revision": supersedes.Revision, "digest": supersedes.Digest}
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return recordprims.Pin{}, err
	}
	receipt, err := st.Append(ctx, recordprims.Write{Record: raw, IdempotencyKey: key, Lease: lease})
	if err != nil {
		c.setState("journal_unavailable")
		return recordprims.Pin{}, fmt.Errorf("%w: %v", ErrPersistenceUncertain, err)
	}
	switch {
	case receipt.Outcome == want && receipt.Pin != nil:
		return *receipt.Pin, nil
	case receipt.Outcome == recordprims.OutcomeRevisionConflict:
		return recordprims.Pin{}, ErrApprovalConflict
	case receipt.Outcome == recordprims.OutcomeIdempotencyConflict:
		return recordprims.Pin{}, fmt.Errorf("%w: idempotency key %s reused with a different record", ErrApprovalConflict, key)
	default:
		c.setState("journal_unavailable")
		return recordprims.Pin{}, fmt.Errorf("%w: journal outcome %s", ErrPersistenceUncertain, receipt.Outcome)
	}
}

// JournalID returns this store's identity, creating it once. Approvals bind
// it so a signature cannot be consumed against a second store.
func (c *Coordinator) JournalID(ctx context.Context) (string, error) {
	st, err := c.openStream(identityStreamID, recordprims.ModeSingleRevision)
	if err != nil {
		return "", err
	}
	head, ok, err := st.Head(identityRecordID)
	if err != nil {
		return "", err
	}
	if ok {
		raw, err := readPinned(ctx, st, head.Pin)
		if err != nil {
			return "", err
		}
		var env struct {
			Payload struct {
				JournalID string `json:"journal_id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(raw, &env); err != nil || env.Payload.JournalID == "" {
			return "", fmt.Errorf("journal identity record unreadable")
		}
		return env.Payload.JournalID, nil
	}
	id := uuid.NewString()
	payload := map[string]string{"schema": "spanwit-journal-identity/v0", "journal_id": id}
	if _, err := c.write(ctx, st, identityRecordID, 1, nil, "contract: spanwit-journal-identity/v0", payload, "identity:"+id, recordprims.OutcomeAppended); err != nil {
		return "", err
	}
	return id, nil
}

// LoadApproval reads the head revision of an approval record.
func (c *Coordinator) LoadApproval(ctx context.Context, approvalID string) (ApprovalHead, error) {
	st, err := c.openStream(approvalStreamID, recordprims.ModeRevisioned)
	if err != nil {
		return ApprovalHead{}, err
	}
	head, ok, err := st.Head(approvalID)
	if err != nil {
		return ApprovalHead{}, err
	}
	if !ok {
		return ApprovalHead{}, ErrApprovalNotFound
	}
	raw, err := readPinned(ctx, st, head.Pin)
	if err != nil {
		return ApprovalHead{}, err
	}
	var env recordEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ApprovalHead{}, err
	}
	var rec ApprovalRecord
	dec := json.NewDecoder(bytes.NewReader(env.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return ApprovalHead{}, fmt.Errorf("approval record: %w", err)
	}
	if env.RecordID != approvalID || env.Revision != head.Pin.Revision || rec.ApprovalID != approvalID || rec.Schema != ApprovalRecordSchema {
		return ApprovalHead{}, fmt.Errorf("approval record %s inconsistent with its head", approvalID)
	}
	return ApprovalHead{Record: rec, Pin: head.Pin}, nil
}

// RequestApproval journals a new approval request (no authority). The
// approval id is chosen by the caller before signing and stored as-is.
func (c *Coordinator) RequestApproval(ctx context.Context, approvalID string, payload []byte) (ApprovalHead, error) {
	st, err := c.openStream(approvalStreamID, recordprims.ModeRevisioned)
	if err != nil {
		return ApprovalHead{}, err
	}
	rec := ApprovalRecord{Schema: ApprovalRecordSchema, ApprovalID: approvalID, State: ApprovalRequested,
		Payload: append([]byte(nil), payload...), UpdatedAt: nowInstant()}
	pin, err := c.write(ctx, st, approvalID, 1, nil, "contract: "+ApprovalRecordSchema, rec, "request:"+approvalID, recordprims.OutcomeAppended)
	if err != nil {
		return ApprovalHead{}, err
	}
	return ApprovalHead{Record: rec, Pin: pin}, nil
}

// transition revises an approval from one of the allowed states, using the
// head pin it was read at as the expected predecessor.
func (c *Coordinator) transition(ctx context.Context, prev ApprovalHead, from []string, next ApprovalRecord, key string) (ApprovalHead, error) {
	allowed := false
	for _, s := range from {
		if prev.Record.State == s {
			allowed = true
		}
	}
	if !allowed {
		return ApprovalHead{}, fmt.Errorf("%w: %s -> %s", ErrApprovalTransition, prev.Record.State, next.State)
	}
	st, err := c.openStream(approvalStreamID, recordprims.ModeRevisioned)
	if err != nil {
		return ApprovalHead{}, err
	}
	next.Schema, next.ApprovalID = ApprovalRecordSchema, prev.Record.ApprovalID
	next.UpdatedAt = nowInstant()
	pin, err := c.write(ctx, st, prev.Record.ApprovalID, prev.Pin.Revision+1, &prev.Pin,
		"contract: "+ApprovalRecordSchema, next, key, recordprims.OutcomeRevised)
	if err != nil {
		return ApprovalHead{}, err
	}
	return ApprovalHead{Record: next, Pin: pin}, nil
}

// IssueApproval records a verified signature over the requested payload.
func (c *Coordinator) IssueApproval(ctx context.Context, prev ApprovalHead, signature string, d ApprovalDecision) (ApprovalHead, error) {
	next := prev.Record
	next.State, next.Signature, next.Decision = ApprovalIssued, signature, &d
	return c.transition(ctx, prev, []string{ApprovalRequested}, next, "issue:"+prev.Record.ApprovalID)
}

// RevokeApproval revokes a requested or issued approval.
func (c *Coordinator) RevokeApproval(ctx context.Context, prev ApprovalHead) (ApprovalHead, error) {
	next := prev.Record
	next.State, next.RevokedAt = ApprovalRevoked, nowInstant()
	return c.transition(ctx, prev, []string{ApprovalRequested, ApprovalIssued}, next, "revoke:"+prev.Record.ApprovalID)
}

// ConsumeApproval is the serialized grant-use decision: it must succeed as
// a revision of the issued record before any effect.
func (c *Coordinator) ConsumeApproval(ctx context.Context, prev ApprovalHead, cons ApprovalConsumption) (ApprovalHead, error) {
	next := prev.Record
	next.State, next.Consumption = ApprovalConsumed, &cons
	return c.transition(ctx, prev, []string{ApprovalIssued}, next, "consume:"+prev.Record.ApprovalID+":"+cons.OperationID)
}

// StorePlan durably stores the exact plan bytes before any effect: a
// content-addressed body, bound by digest and length in a record of the
// plan stream (record id plan-<hex>). The consumption record references the
// returned pin, so recovery never depends on the original input file.
// Storing the same plan again is an idempotent replay of the same record.
func (c *Coordinator) StorePlan(ctx context.Context, plan []byte) (recordprims.Pin, error) {
	if c.store == nil {
		return recordprims.Pin{}, ErrPersistenceUncertain
	}
	if err := c.admitJournal(ctx); err != nil {
		return recordprims.Pin{}, fmt.Errorf("%w: %v", ErrPersistenceUncertain, err)
	}
	ref, lease, err := c.store.PutBody(ctx, bytes.NewReader(plan), "application/json")
	if err != nil {
		c.setState("journal_unavailable")
		return recordprims.Pin{}, fmt.Errorf("%w: %v", ErrPersistenceUncertain, err)
	}
	st, err := c.openStream(planStreamID, recordprims.ModeSingleRevision)
	if err != nil {
		return recordprims.Pin{}, err
	}
	id := "plan-" + strings.TrimPrefix(ref.Digest, "sha256:")
	body := map[string]any{"body": map[string]any{"digest": ref.Digest, "length": ref.Length, "media_type": ref.MediaType}}
	return c.writeContent(ctx, st, id, 1, nil, "contract: spanwit-exact-prune-plan/v1", body, &lease, "plan:"+id, recordprims.OutcomeAppended)
}

// LoadPlan returns the stored plan bytes for a plan record pin.
func (c *Coordinator) LoadPlan(ctx context.Context, pin recordprims.Pin) ([]byte, error) {
	st, err := c.openStream(planStreamID, recordprims.ModeSingleRevision)
	if err != nil {
		return nil, err
	}
	sink := &bodyCollector{limit: 64 << 20}
	res, err := st.Release(ctx, recordprims.UseRequest{
		Purpose: recordprims.PurposeExport, Selection: []recordprims.Pin{pin}, Closure: recordprims.ClosureFullContent,
	}, sink)
	if err != nil {
		return nil, err
	}
	if res.Outcome != recordprims.ReleaseReleased || !sink.has {
		return nil, fmt.Errorf("plan %s unreadable: %s", pin.RecordID, res.Outcome)
	}
	return sink.body, nil
}

type bodyCollector struct {
	limit int64
	body  []byte
	has   bool
}

func (b *bodyCollector) Frame(_ context.Context, h recordprims.FrameHeader, r io.Reader) error {
	if h.Kind != recordprims.FrameKindBody {
		return drain(r)
	}
	data, err := io.ReadAll(io.LimitReader(r, b.limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > b.limit {
		return fmt.Errorf("plan body larger than %d bytes", b.limit)
	}
	b.body, b.has = data, true
	return nil
}

// ExecutionRecord is one record of an approved (or unapproved) execution:
// kind started, outcome (one per candidate) or finished.
type ExecutionRecord struct {
	Schema      string `json:"schema"`
	OperationID string `json:"operation_id"`
	Kind        string `json:"kind"`
	ApprovalID  string `json:"approval_id,omitempty"`
	Index       int    `json:"index,omitempty"`
	Path        string `json:"path,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	Detail      string `json:"detail,omitempty"`
	At          string `json:"at"`
}

// AppendExecution journals one execution record. Record ids are
// <operation_id>:started, <operation_id>:c<index>, <operation_id>:finished.
func (c *Coordinator) AppendExecution(ctx context.Context, rec ExecutionRecord) error {
	st, err := c.openStream(executionStreamID, recordprims.ModeSingleRevision)
	if err != nil {
		return err
	}
	rec.Schema = ExecutionRecordSchema
	rec.At = nowInstant()
	id := rec.OperationID + ":" + rec.Kind
	if rec.Kind == "outcome" {
		id = fmt.Sprintf("%s:c%06d", rec.OperationID, rec.Index)
	}
	_, err = c.write(ctx, st, id, 1, nil, "contract: "+ExecutionRecordSchema, rec, "exec:"+id, recordprims.OutcomeAppended)
	return err
}

// ExecutionRecords returns every execution record of one operation, in
// record-id order (started, c000000.., finished).
func (c *Coordinator) ExecutionRecords(ctx context.Context, operationID string) ([]ExecutionRecord, error) {
	st, err := c.openStream(executionStreamID, recordprims.ModeSingleRevision)
	if err != nil {
		return nil, err
	}
	var out []ExecutionRecord
	sink := &execCollector{prefix: operationID + ":", out: &out}
	res, err := st.Release(ctx, recordprims.UseRequest{Purpose: recordprims.PurposeServe, Closure: recordprims.ClosureMetadataOnly}, sink)
	if err != nil {
		return nil, err
	}
	if res.Outcome != recordprims.ReleaseReleased {
		return nil, fmt.Errorf("execution records unreadable: %s", res.Outcome)
	}
	return out, nil
}

type execCollector struct {
	prefix string
	out    *[]ExecutionRecord
}

func (e *execCollector) Frame(_ context.Context, h recordprims.FrameHeader, r io.Reader) error {
	if h.Kind != recordprims.FrameKindRecord || !strings.HasPrefix(h.Pin.RecordID, e.prefix) {
		return drain(r)
	}
	b, err := io.ReadAll(io.LimitReader(r, maxApprovalRecord+1))
	if err != nil {
		return err
	}
	var env struct {
		Payload ExecutionRecord `json:"payload"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return err
	}
	*e.out = append(*e.out, env.Payload)
	return nil
}

// NewOperationID returns a fresh operation identity.
func NewOperationID() string { return uuid.NewString() }

// drain consumes a frame the caller does not need: recordprims reports a
// release as uncertain unless every delivered frame is read in full.
func drain(r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

// nowInstant formats the current time at the microsecond precision the
// durable-record contract requires (a sub-microsecond instant is refused).
func nowInstant() string {
	return time.Now().UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
}
