//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/3leaps/spanwit/internal/approval"
	"github.com/3leaps/spanwit/internal/daemon"
	"github.com/3leaps/spanwit/internal/engine"
)

// pruneSession is the single platform gate for execution (darwin/linux):
// every --execute holds the coordinator for the whole run and journals every
// candidate outcome. There is no unjournaled path on these platforms.
type pruneSession struct {
	coord   *daemon.Coordinator
	signers string
}

// openPruneSession takes coordinator ownership before any discovery or plan
// work. A running daemon refuses with the remedy; a store that cannot be
// initialised or admitted refuses (never delete unjournaled).
func openPruneSession(stateDir, allowedSigners string) (*pruneSession, error) {
	if err := checkStateFilesystem(stateDir); err != nil {
		return nil, err
	}
	c, err := daemon.Open(stateDir, nil)
	if err != nil {
		if errors.Is(err, daemon.ErrBusy) {
			return nil, fmt.Errorf("writer_busy: the spanwit daemon holds %s; stop it (`spanwit daemon service stop`, or end a foreground `spanwit daemon run`), run prune, then start it again (`spanwit daemon service start`)", stateDir)
		}
		return nil, fmt.Errorf("execution journal unavailable at %s (nothing deleted): %w", stateDir, err)
	}
	return &pruneSession{coord: c, signers: allowedSigners}, nil
}

func (s *pruneSession) close() {
	if s != nil && s.coord != nil {
		_ = s.coord.Close()
	}
}

func (s *pruneSession) executor() approval.Executor {
	return approval.Executor{Coord: s.coord, AllowedSigners: s.signers, Now: time.Now, Delete: approval.GuardedDelete}
}

// executeApproved consumes a one-shot approval and executes the exact plan
// bytes it binds (read once by the caller).
func (s *pruneSession) executeApproved(ctx context.Context, approvalID, operationID string, raw []byte, minAge *string, opts []engine.PlanOptions) (PrunePlan, pruneExecutionInfo, *daemon.OperationReport, error) {
	ex := s.executor()
	ex.Plan = func(ctx context.Context, raw []byte, minAge string) (engine.PrunePlan, error) {
		return buildPrunePlanFromExactPlanBytes(ctx, raw, minAge, opts...)
	}
	res, err := ex.ExecuteApproved(ctx, approval.ExecuteInput{ApprovalID: approvalID, Plan: raw, MinAge: minAge, OperationID: operationID})
	info := pruneExecutionInfo{Journal: engine.ExecutionJournaled, OperationID: res.OperationID, ApprovalID: approvalID, Assertion: assertionLabel()}
	if err != nil {
		return res.Plan, info, nil, err
	}
	if res.Replayed {
		// An exact retry: the recorded result of the original operation,
		// rebuilt from its stored manifest and outcome records. Nothing was
		// replanned or deleted.
		return res.Plan, info, res.Report, reportError(res.Report)
	}
	return res.Plan, info, nil, outcomeError(res)
}

// executeUnapproved is the explicit --execute: same coordinator, guard and
// outcome records, no approval.
func (s *pruneSession) executeUnapproved(ctx context.Context, plan *PrunePlan) (pruneExecutionInfo, error) {
	res, err := s.executor().ExecuteUnapproved(ctx, *plan)
	*plan = res.Plan
	for _, w := range res.Outcomes {
		if w.Outcome == approval.OutcomeFailed || w.Outcome == approval.OutcomePartial {
			plan.Warnings = append(plan.Warnings, fmt.Errorf("%s %s: %s", w.Outcome, w.Path, w.Detail))
		}
	}
	info := pruneExecutionInfo{Journal: engine.ExecutionJournaled, OperationID: res.OperationID, Assertion: assertionLabel()}
	if err != nil {
		return info, err
	}
	return info, outcomeError(res)
}

func outcomeError(res approval.Result) error {
	var bad, attempted int
	for _, o := range res.Outcomes {
		switch o.Outcome {
		case approval.OutcomeApplied:
			attempted++
		case approval.OutcomeFailed, approval.OutcomePartial:
			attempted++
			bad++
		}
	}
	if bad > 0 {
		return fmt.Errorf("failed to delete %d of %d prune candidates (operation %s)", bad, attempted, res.OperationID)
	}
	return nil
}

// checkStateFilesystem refuses, before any discovery, a state directory
// (or, on a fresh host, its nearest existing ancestor) on a filesystem the
// journal store does not accept, naming the filesystem and the remedy.
func checkStateFilesystem(stateDir string) error {
	probe := filepath.Clean(stateDir)
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return nil // nothing exists; daemon.Open reports the real error
		}
		probe = parent
	}
	name, ok, err := stateFSType(probe)
	if err != nil || ok {
		return nil // daemon.Open performs the authoritative check
	}
	return fmt.Errorf("execution journal unavailable (nothing deleted): %s is on filesystem type %q; the journal needs APFS or HFS+ (macOS) or ext4, XFS or Btrfs (Linux). Use --state-dir to point at a private directory on one of those", stateDir, name)
}

// reportError is non-nil when a recorded operation did not fully succeed:
// a failed, partial or unattempted outcome, a candidate with no recorded
// outcome, an unfinished run, or a run that finished with a stop reason
// (limits exceeded, expiry, cancellation, plan failure). A retry never turns
// the original refusal into success.
func reportError(r *daemon.OperationReport) error {
	if r == nil {
		return fmt.Errorf("recorded result unavailable")
	}
	var bad, unattempted int
	for _, c := range r.Candidates {
		switch c.Outcome {
		case approval.OutcomeFailed, approval.OutcomePartial:
			bad++
		case approval.OutcomeUnattempted:
			unattempted++
		}
	}
	if bad > 0 || unattempted > 0 || r.Unknown > 0 || !r.Finished || r.FinishDetail != "" {
		detail := r.FinishDetail
		if detail == "" {
			detail = "none"
		}
		return fmt.Errorf("operation %s: %d failed or partial, %d unattempted, %d unknown, finished=%v, stop=%s (recorded result; nothing re-executed)",
			r.OperationID, bad, unattempted, r.Unknown, r.Finished, detail)
	}
	return nil
}

// writeOperationReport renders a recorded operation result (an exact retry):
// JSON as the operation report, text as one line per candidate.
func writeOperationReport(w io.Writer, r *daemon.OperationReport, format string) error {
	if format == "json" {
		return writeIndentedJSON(w, r)
	}
	_, _ = fmt.Fprintf(w, "Recorded result of operation %s (approval %s); nothing re-executed.\n", r.OperationID, r.ApprovalID)
	for _, c := range r.Candidates {
		line := fmt.Sprintf("  %-12s %s", c.Outcome, c.Path)
		if c.Reason != "" {
			line += " (" + c.Reason + ")"
		}
		_, _ = fmt.Fprintln(w, line)
	}
	_, _ = fmt.Fprintf(w, "%d candidates, %d unknown, finished=%v\n", len(r.Candidates), r.Unknown, r.Finished)
	return nil
}
