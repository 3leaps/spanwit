//go:build !(darwin || linux)

package cmd

import (
	"context"
	"fmt"

	"github.com/3leaps/spanwit/internal/approval"
	"github.com/3leaps/spanwit/internal/engine"
)

// pruneSession is the single platform gate for execution on platforms
// without the coordinator store: --execute keeps the v0.2 unjournaled
// behaviour and says so; approved execution is unavailable.
type pruneSession struct{}

func openPruneSession(string, string) (*pruneSession, error) { return &pruneSession{}, nil }

func (s *pruneSession) close() {}

func (s *pruneSession) executeApproved(context.Context, string, string, []byte, *string, []engine.PlanOptions) (PrunePlan, pruneExecutionInfo, error) {
	return PrunePlan{}, pruneExecutionInfo{}, &approval.Refusal{Code: approval.CodePlatformUnavailable, Detail: "approved execution is unavailable on this platform"}
}

func (s *pruneSession) executeUnapproved(ctx context.Context, plan *PrunePlan) (pruneExecutionInfo, error) {
	info := pruneExecutionInfo{Journal: engine.ExecutionUnavailableOnPlatform, Assertion: engine.ExecutionAssertionOpen}
	if err := executePrunePlan(ctx, plan); err != nil {
		return info, fmt.Errorf("%w (this run is not journaled on this platform)", err)
	}
	return info, nil
}
