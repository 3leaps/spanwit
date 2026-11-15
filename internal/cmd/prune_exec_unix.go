//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"fmt"
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
func (s *pruneSession) executeApproved(ctx context.Context, approvalID, operationID string, raw []byte, minAge *string, opts []engine.PlanOptions) (PrunePlan, pruneExecutionInfo, error) {
	ex := s.executor()
	ex.Plan = func(ctx context.Context, raw []byte, minAge string) (engine.PrunePlan, error) {
		return buildPrunePlanFromExactPlanBytes(ctx, raw, minAge, opts...)
	}
	res, err := ex.ExecuteApproved(ctx, approval.ExecuteInput{ApprovalID: approvalID, Plan: raw, MinAge: minAge, OperationID: operationID})
	info := pruneExecutionInfo{Journal: engine.ExecutionJournaled, OperationID: res.OperationID, ApprovalID: approvalID, Assertion: assertionLabel()}
	if err != nil {
		return res.Plan, info, err
	}
	if res.Replayed {
		return res.Plan, info, fmt.Errorf("operation %s already ran for approval %s; nothing re-executed (%d recorded outcomes)", res.OperationID, approvalID, len(res.Outcomes))
	}
	return res.Plan, info, outcomeError(res)
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

func assertionLabel() string {
	if ResolvedApprovedOnly() {
		return engine.ExecutionAssertionApprovalBound
	}
	return engine.ExecutionAssertionOpen
}
