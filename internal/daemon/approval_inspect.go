//go:build darwin || linux

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/3leaps/recordprims"
	"golang.org/x/sys/unix"
)

// ExecutionManifest is the stored list of candidates an execution attempts,
// in loop order, written before the first effect.
type ExecutionManifest struct {
	Schema      string              `json:"schema"`
	OperationID string              `json:"operation_id"`
	Candidates  []ManifestCandidate `json:"candidates"`
}

// ManifestCandidate is one candidate the loop will visit.
type ManifestCandidate struct {
	Index     int    `json:"index"`
	Path      string `json:"path"`
	State     string `json:"state"`
	SizeBytes int64  `json:"size_bytes"`
}

// CandidateResult is a candidate's recorded outcome, or "unknown" when no
// outcome record exists (never inferred to be unattempted).
type CandidateResult struct {
	Index    int    `json:"index"`
	Path     string `json:"path"`
	Outcome  string `json:"outcome"`
	Reason   string `json:"reason,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Recorded bool   `json:"recorded"`
}

// OperationReport projects an execution from its journal records and its
// stored manifest. Candidates with no outcome record are reported unknown.
type OperationReport struct {
	OperationID   string            `json:"operation_id"`
	ApprovalID    string            `json:"approval_id,omitempty"`
	Started       bool              `json:"started"`
	ManifestKnown bool              `json:"manifest_known"`
	Finished      bool              `json:"finished"`
	FinishDetail  string            `json:"finish_detail,omitempty"`
	Candidates    []CandidateResult `json:"candidates"`
	Unknown       int               `json:"unknown"`
}

// ApprovalInspection is an approval's journaled state for display.
type ApprovalInspection struct {
	ApprovalID string            `json:"approval_id"`
	State      string            `json:"state"`
	Revision   uint64            `json:"revision"`
	UpdatedAt  string            `json:"updated_at"`
	Decision   *ApprovalDecision `json:"decision,omitempty"`
	Operation  *OperationReport  `json:"operation,omitempty"`
}

// ErrStoreHeld is returned by inspection while another process (normally
// the daemon) holds the store.
var ErrStoreHeld = errors.New("the store is held by a running coordinator; stop the daemon or retry")

func projectOperation(ctx context.Context, exec, plan *recordprims.Stream, op string) (OperationReport, error) {
	rep := OperationReport{OperationID: op, Candidates: []CandidateResult{}}
	recs, err := executionRecordsFrom(ctx, exec, op)
	if err != nil {
		return rep, err
	}
	outcomes := map[int]ExecutionRecord{}
	var manifestPin *recordprims.Pin
	for _, r := range recs {
		switch r.Kind {
		case "started":
			rep.Started, rep.ApprovalID = true, r.ApprovalID
		case "manifest":
			manifestPin = r.Manifest
		case "outcome":
			outcomes[r.Index] = r
		case "finished":
			rep.Finished, rep.FinishDetail = true, r.Detail
		}
	}
	if manifestPin != nil && plan != nil {
		raw, err := loadBodyFrom(ctx, plan, *manifestPin)
		if err != nil {
			return rep, fmt.Errorf("manifest unreadable: %w", err)
		}
		var m ExecutionManifest
		if err := json.Unmarshal(raw, &m); err != nil || m.Schema != ExecutionManifestSchema || m.OperationID != op {
			return rep, fmt.Errorf("manifest for %s invalid", op)
		}
		rep.ManifestKnown = true
		for _, c := range m.Candidates {
			cr := CandidateResult{Index: c.Index, Path: c.Path, Outcome: "unknown"}
			if o, ok := outcomes[c.Index]; ok {
				cr.Outcome, cr.Reason, cr.Detail, cr.Recorded = o.Outcome, o.Reason, o.Detail, true
				delete(outcomes, c.Index)
			} else {
				rep.Unknown++
			}
			rep.Candidates = append(rep.Candidates, cr)
		}
	}
	// Outcomes outside the manifest (or with no manifest) are still shown.
	var rest []int
	for i := range outcomes {
		rest = append(rest, i)
	}
	sort.Ints(rest)
	for _, i := range rest {
		o := outcomes[i]
		rep.Candidates = append(rep.Candidates, CandidateResult{Index: i, Path: o.Path, Outcome: o.Outcome, Reason: o.Reason, Detail: o.Detail, Recorded: true})
	}
	return rep, nil
}

// OperationReport projects an operation through this coordinator.
func (c *Coordinator) OperationReport(ctx context.Context, op string) (OperationReport, error) {
	exec, err := c.openStream(executionStreamID, recordprims.ModeSingleRevision)
	if err != nil {
		return OperationReport{}, err
	}
	plan, err := c.openStream(planStreamID, recordprims.ModeSingleRevision)
	if err != nil {
		return OperationReport{}, err
	}
	return projectOperation(ctx, exec, plan, op)
}

// inspectionStore opens an existing store for reading without acquiring
// coordinator.lock and without creating the state directory, the store or
// any stream. ok is false when there is no store.
func inspectionStore(ctx context.Context, dir string) (*recordprims.Store, string, bool, error) {
	dirFD, canonical, err := OpenVerifiedDir(dir, false, 0700)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	_ = unix.Close(dirFD)
	journalDir := filepath.Join(canonical, "journal")
	if _, err := os.Lstat(filepath.Join(journalDir, "STORE")); err != nil {
		if os.IsNotExist(err) {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	if _, err := journalUsage(ctx, journalDir, journalWatermark); err != nil {
		return nil, "", false, err
	}
	store, err := recordprims.Open(journalDir, recordprims.OpenOptions{})
	if err != nil {
		if errors.Is(err, recordprims.ErrLocked) {
			return nil, "", false, ErrStoreHeld
		}
		return nil, "", false, err
	}
	return store, journalDir, true, nil
}

func streamExistsOnDisk(journalDir, id string) bool {
	sum := sha256.Sum256([]byte(id))
	info, err := os.Lstat(filepath.Join(journalDir, "streams", fmt.Sprintf("%x", sum[:])[:32], "journal.ndjson"))
	return err == nil && info.Mode().IsRegular()
}

func openExisting(store *recordprims.Store, journalDir, id string, mode recordprims.Mode) (*recordprims.Stream, error) {
	if !streamExistsOnDisk(journalDir, id) {
		return nil, nil
	}
	st, err := store.OpenStream(id, mode)
	if errors.Is(err, recordprims.ErrLocked) {
		return nil, ErrStoreHeld
	}
	return st, err
}

// InspectApproval reads an approval (and, when consumed, its operation)
// without mutating the state directory.
func InspectApproval(ctx context.Context, dir, approvalID string) (ApprovalInspection, error) {
	store, journalDir, ok, err := inspectionStore(ctx, dir)
	if err != nil || !ok {
		if err == nil {
			err = ErrApprovalNotFound
		}
		return ApprovalInspection{}, err
	}
	defer func() { _ = store.Close() }()
	ast, err := openExisting(store, journalDir, approvalStreamID, recordprims.ModeRevisioned)
	if err != nil {
		return ApprovalInspection{}, err
	}
	if ast == nil {
		return ApprovalInspection{}, ErrApprovalNotFound
	}
	head, err := loadApprovalFrom(ctx, ast, approvalID)
	if err != nil {
		return ApprovalInspection{}, err
	}
	out := ApprovalInspection{ApprovalID: approvalID, State: head.Record.State, Revision: head.Pin.Revision,
		UpdatedAt: head.Record.UpdatedAt, Decision: head.Record.Decision}
	if cons := head.Record.Consumption; cons != nil {
		rep, err := inspectOperationIn(ctx, store, journalDir, cons.OperationID)
		if err != nil {
			return out, err
		}
		out.Operation = &rep
	}
	return out, nil
}

// InspectOperation projects one operation without mutating the state
// directory; it does not need the original plan file.
func InspectOperation(ctx context.Context, dir, op string) (OperationReport, error) {
	store, journalDir, ok, err := inspectionStore(ctx, dir)
	if err != nil {
		return OperationReport{}, err
	}
	if !ok {
		return OperationReport{}, fmt.Errorf("no execution journal at %s", dir)
	}
	defer func() { _ = store.Close() }()
	return inspectOperationIn(ctx, store, journalDir, op)
}

func inspectOperationIn(ctx context.Context, store *recordprims.Store, journalDir, op string) (OperationReport, error) {
	exec, err := openExisting(store, journalDir, executionStreamID, recordprims.ModeSingleRevision)
	if err != nil {
		return OperationReport{}, err
	}
	if exec == nil {
		return OperationReport{OperationID: op, Candidates: []CandidateResult{}}, nil
	}
	plan, err := openExisting(store, journalDir, planStreamID, recordprims.ModeSingleRevision)
	if err != nil {
		return OperationReport{}, err
	}
	return projectOperation(ctx, exec, plan, op)
}
