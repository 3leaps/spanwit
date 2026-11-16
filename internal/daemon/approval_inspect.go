//go:build darwin || linux

package daemon

import (
	"context"
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
	OperationID   string `json:"operation_id"`
	ApprovalID    string `json:"approval_id,omitempty"`
	Started       bool   `json:"started"`
	ManifestKnown bool   `json:"manifest_known"`
	// FromApprovedPlan is set when no manifest was stored (a crash before the
	// loop) and the candidates come from the consumed approval's exact plan,
	// all unknown.
	FromApprovedPlan bool              `json:"from_approved_plan,omitempty"`
	Finished         bool              `json:"finished"`
	FinishDetail     string            `json:"finish_detail,omitempty"`
	Candidates       []CandidateResult `json:"candidates"`
	Unknown          int               `json:"unknown"`
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

func projectOperation(ctx context.Context, exec, plan, approvals *recordprims.Stream, op, approvalID string) (OperationReport, error) {
	rep := OperationReport{OperationID: op, ApprovalID: approvalID, Candidates: []CandidateResult{}}
	var recs []ExecutionRecord
	if exec != nil {
		var err error
		if recs, err = executionRecordsFrom(ctx, exec, op); err != nil {
			return rep, err
		}
	}
	outcomes := map[int]ExecutionRecord{}
	var manifestPin *recordprims.Pin
	for _, r := range recs {
		switch r.Kind {
		case "started":
			rep.Started = true
			if r.ApprovalID != "" {
				rep.ApprovalID = r.ApprovalID
			}
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
	if !rep.ManifestKnown && rep.ApprovalID != "" && approvals != nil && plan != nil {
		if err := fallbackToApprovedPlan(ctx, &rep, approvals, plan, op, outcomes); err != nil {
			return rep, err
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
// approvalID (optional) enables the approved-plan fallback.
func (c *Coordinator) OperationReport(ctx context.Context, op, approvalID string) (OperationReport, error) {
	exec, err := c.openStream(executionStreamID, recordprims.ModeSingleRevision)
	if err != nil {
		return OperationReport{}, err
	}
	plan, err := c.openStream(planStreamID, recordprims.ModeSingleRevision)
	if err != nil {
		return OperationReport{}, err
	}
	approvals, err := c.openStream(approvalStreamID, recordprims.ModeRevisioned)
	if err != nil {
		return OperationReport{}, err
	}
	return projectOperation(ctx, exec, plan, approvals, op, approvalID)
}

// StoreExists reports whether dir holds a coordinator store, without
// creating or opening anything. Inspection commands use it so that asking
// about an absent store never creates one.
func StoreExists(dir string) (bool, error) {
	dirFD, canonical, err := OpenVerifiedDir(dir, false, 0700)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		return false, err
	}
	_ = unix.Close(dirFD)
	if _, err := os.Lstat(filepath.Join(canonical, "journal", "STORE")); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// InspectApproval reads an approval and, once consumed, its operation
// report. It runs under coordinator ownership: recordprims may rebuild
// derived indexes while reading, so inspection is a writer, not a read-only
// operation, until recordprims offers a non-repairing open.
func (c *Coordinator) InspectApproval(ctx context.Context, approvalID string) (ApprovalInspection, error) {
	head, err := c.LoadApproval(ctx, approvalID)
	if err != nil {
		return ApprovalInspection{}, err
	}
	out := ApprovalInspection{ApprovalID: approvalID, State: head.Record.State, Revision: head.Pin.Revision,
		UpdatedAt: head.Record.UpdatedAt, Decision: head.Record.Decision}
	if cons := head.Record.Consumption; cons != nil {
		rep, err := c.OperationReport(ctx, cons.OperationID, approvalID)
		if err != nil {
			return out, err
		}
		out.Operation = &rep
	}
	return out, nil
}

// fallbackToApprovedPlan lists the consumed approval's exact plan candidates
// as unknown when no manifest exists. Only an approval consumed by this very
// operation is used. Nothing is replanned.
func fallbackToApprovedPlan(ctx context.Context, rep *OperationReport, approvals, plan *recordprims.Stream, op string, outcomes map[int]ExecutionRecord) error {
	head, err := loadApprovalFrom(ctx, approvals, rep.ApprovalID)
	if err != nil {
		return err
	}
	cons := head.Record.Consumption
	if cons == nil || cons.OperationID != op {
		return nil
	}
	raw, err := loadBodyFrom(ctx, plan, cons.Plan)
	if err != nil {
		return fmt.Errorf("approved plan unreadable: %w", err)
	}
	var doc struct {
		Candidates []struct {
			Path string `json:"path"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("approved plan invalid: %w", err)
	}
	if len(outcomes) > 0 {
		return nil // outcomes without a manifest cannot be aligned to plan order
	}
	rep.FromApprovedPlan = true
	for i, c := range doc.Candidates {
		rep.Candidates = append(rep.Candidates, CandidateResult{Index: i, Path: c.Path, Outcome: "unknown"})
		rep.Unknown++
	}
	return nil
}
