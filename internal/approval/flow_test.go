//go:build darwin || linux

package approval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/canonjson"
	"github.com/3leaps/spanwit/internal/daemon"
	"github.com/3leaps/spanwit/internal/engine"
)

const flowPlan = `{"$schema":"x","version":1,"analysis_root":"/r","min_size":0,"sort_mode":"size","reclaim_scope":"whole",
"candidates":[{"path":"/r/a","signature":"s1","size_bytes":10},{"path":"/r/b","signature":"s1","size_bytes":20}]}`

type flowRig struct {
	t       *testing.T
	coord   *daemon.Coordinator
	key     softSK
	signers string
	now     time.Time
	deleted []string
	drift   map[string]string // path -> drift size value for the fake planner
	extra   int               // extra prunable candidates the fake planner adds
}

func newFlowRig(t *testing.T) *flowRig {
	t.Helper()
	requireKeygen(t)
	trustAsOther(t)
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	state, _ = filepath.EvalSymlinks(state)
	c, err := daemon.Open(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	k := newSoftSK(t)
	return &flowRig{t: t, coord: c, key: k,
		signers: writeSigners(t, trustedDir(t), "maintainer@example "+k.authorizedKey()+"\n"),
		now:     time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), drift: map[string]string{}}
}

func (r *flowRig) executor() Executor {
	return Executor{
		Coord: r.coord, AllowedSigners: r.signers, Now: func() time.Time { return r.now },
		Plan: func(_ context.Context, raw []byte, _ string) (engine.PrunePlan, error) {
			v, err := canonjson.Parse(raw)
			if err != nil {
				return engine.PrunePlan{}, err
			}
			var plan engine.PrunePlan
			for _, c := range v.(canonjson.Object)["candidates"].([]canonjson.Value) {
				o := c.(canonjson.Object)
				cand := engine.PruneCandidate{Path: o["path"].(string), Size: o["size_bytes"].(int64), State: engine.CandidateStatePrunable,
					Drift: &engine.CandidateDrift{Size: engine.DriftUnchanged, Activity: engine.DriftUnchanged}}
				if d, ok := r.drift[cand.Path]; ok {
					cand.Drift.Size = d
				}
				plan.Candidates = append(plan.Candidates, cand)
			}
			for i := 0; i < r.extra; i++ {
				plan.Candidates = append(plan.Candidates, engine.PruneCandidate{Path: "/r/extra", Size: 1, State: engine.CandidateStatePrunable})
			}
			return plan, nil
		},
		Delete: func(_ context.Context, c *engine.PruneCandidate) (string, string) {
			r.deleted = append(r.deleted, c.Path)
			return OutcomeApplied, ""
		},
	}
}

// approve runs request -> show -> sign (software key, presence set) -> issue.
func (r *flowRig) approve(plan string, minAge *string) Payload {
	r.t.Helper()
	p, raw, err := Request(context.Background(), r.coord, RequestInput{Plan: []byte(plan), MinAge: minAge, Expires: time.Hour, Now: r.now})
	if err != nil {
		r.t.Fatal(err)
	}
	if _, _, err := Show([]byte(plan), raw); err != nil {
		r.t.Fatal(err)
	}
	if _, _, err := Issue(context.Background(), r.coord, raw, r.key.sign(raw, SignatureNamespace, 0x01), r.signers, r.now); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if RefusalCode(err) != code {
		t.Fatalf("got %v (code %q), want %s", err, RefusalCode(err), code)
	}
}

func TestApprovedExecutionHappyPathAndReplay(t *testing.T) {
	r := newFlowRig(t)
	p := r.approve(flowPlan, nil)
	ex := r.executor()
	res, err := ex.ExecuteApproved(context.Background(), ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.deleted, ",") != "/r/a,/r/b" || len(res.Outcomes) != 2 || res.Outcomes[1].Outcome != OutcomeApplied {
		t.Fatalf("deleted %v outcomes %+v", r.deleted, res.Outcomes)
	}
	recs, err := r.coord.ExecutionRecords(context.Background(), res.OperationID)
	if err != nil || len(recs) != 4 { // c0, c1, finished, started
		t.Fatalf("journaled %d records: %v", len(recs), err)
	}
	// stored plan body equals the executed bytes
	head, _ := r.coord.LoadApproval(context.Background(), p.ApprovalID)
	if body, err := r.coord.LoadPlan(context.Background(), head.Record.Consumption.Plan); err != nil || string(body) != flowPlan {
		t.Fatalf("stored plan %q %v", body, err)
	}
	// a new attempt: grant_already_consumed
	_, err = ex.ExecuteApproved(context.Background(), ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan)})
	wantCode(t, err, CodeGrantAlreadyConsumed)
	// exact retry of the operation: original result, no new effect
	again, err := ex.ExecuteApproved(context.Background(), ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan), OperationID: res.OperationID})
	if err != nil || !again.Replayed || again.OperationID != res.OperationID || len(r.deleted) != 2 {
		t.Fatalf("replay: %+v %v deleted=%v", again, err, r.deleted)
	}
	// same operation, changed request: idempotency_payload_mismatch
	age := "48h"
	_, err = ex.ExecuteApproved(context.Background(), ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan), MinAge: &age, OperationID: res.OperationID})
	wantCode(t, err, CodeIdempotencyPayloadMismatch)
	if len(r.deleted) != 2 {
		t.Fatalf("refusals deleted more: %v", r.deleted)
	}
}

func TestApprovedExecutionRefusalsBeforeConsume(t *testing.T) {
	changed := strings.Replace(flowPlan, `"size_bytes":20`, `"size_bytes":21`, 1)
	age := "48h"
	cases := []struct {
		name  string
		setup func(r *flowRig, p Payload) ExecuteInput
		code  string
	}{
		{"plan changed", func(r *flowRig, p Payload) ExecuteInput {
			return ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(changed)}
		}, CodeStalePlanOrDrift},
		{"unbound min-age override", func(r *flowRig, p Payload) ExecuteInput {
			return ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan), MinAge: &age}
		}, CodeStalePlanOrDrift},
		{"expired", func(r *flowRig, p Payload) ExecuteInput {
			r.now = p.ExpiresAt
			return ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan)}
		}, CodeGrantExpiredOrRevoked},
		{"revoked", func(r *flowRig, p Payload) ExecuteInput {
			if _, err := Revoke(context.Background(), r.coord, p.ApprovalID); err != nil {
				r.t.Fatal(err)
			}
			return ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan)}
		}, CodeGrantExpiredOrRevoked},
		{"unknown approval", func(r *flowRig, p Payload) ExecuteInput {
			return ExecuteInput{ApprovalID: "nope", Plan: []byte(flowPlan)}
		}, CodeUnauthorizedPrincipal},
		{"operation id that never ran", func(r *flowRig, p Payload) ExecuteInput {
			return ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan), OperationID: "made-up"}
		}, CodeIdempotencyPayloadMismatch},
		{"allowed signers now untrusted", func(r *flowRig, p Payload) ExecuteInput {
			if err := os.Chmod(r.signers, 0o666); err != nil {
				r.t.Fatal(err)
			}
			return ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan)}
		}, CodeAllowedSignersUntrusted},
		{"key removed from allowed signers", func(r *flowRig, p Payload) ExecuteInput {
			other := newSoftSK(r.t)
			if err := os.WriteFile(r.signers, []byte("maintainer@example "+other.authorizedKey()+"\n"), 0o644); err != nil {
				r.t.Fatal(err)
			}
			return ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan)}
		}, CodeUnauthorizedPrincipal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newFlowRig(t)
			p := r.approve(flowPlan, nil)
			in := c.setup(r, p)
			_, err := r.executor().ExecuteApproved(context.Background(), in)
			wantCode(t, err, c.code)
			if len(r.deleted) != 0 {
				t.Fatalf("deleted %v", r.deleted)
			}
			if in.ApprovalID == p.ApprovalID {
				head, _ := r.coord.LoadApproval(context.Background(), p.ApprovalID)
				if head.Record.State == daemon.ApprovalConsumed {
					t.Fatal("refusal consumed the approval")
				}
			}
		})
	}
}

func TestApprovedExecutionBoundMinAge(t *testing.T) {
	r := newFlowRig(t)
	age := "48h"
	p := r.approve(flowPlan, &age)
	if _, err := r.executor().ExecuteApproved(context.Background(), ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan)}); RefusalCode(err) != CodeStalePlanOrDrift {
		t.Fatalf("missing bound --min-age: %v", err)
	}
	if _, err := r.executor().ExecuteApproved(context.Background(), ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan), MinAge: &age}); err != nil {
		t.Fatalf("bound --min-age: %v", err)
	}
}

func TestIssueRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("presence clear", func(t *testing.T) {
		r := newFlowRig(t)
		_, raw, _ := Request(ctx, r.coord, RequestInput{Plan: []byte(flowPlan), Expires: time.Hour, Now: r.now})
		_, _, err := Issue(ctx, r.coord, raw, r.key.sign(raw, SignatureNamespace, 0x00), r.signers, r.now)
		wantCode(t, err, CodeUnauthorizedPrincipal)
	})
	t.Run("other key", func(t *testing.T) {
		r := newFlowRig(t)
		_, raw, _ := Request(ctx, r.coord, RequestInput{Plan: []byte(flowPlan), Expires: time.Hour, Now: r.now})
		_, _, err := Issue(ctx, r.coord, raw, newSoftSK(t).sign(raw, SignatureNamespace, 0x01), r.signers, r.now)
		wantCode(t, err, CodeUnauthorizedPrincipal)
	})
	t.Run("payload field mutated after signing", func(t *testing.T) {
		r := newFlowRig(t)
		p, raw, _ := Request(ctx, r.coord, RequestInput{Plan: []byte(flowPlan), Expires: time.Hour, Now: r.now})
		sig := r.key.sign(raw, SignatureNamespace, 0x01)
		p.Limits.MaxReclaimBytes++
		mutated, _ := p.Canonical()
		_, _, err := Issue(ctx, r.coord, mutated, sig, r.signers, r.now)
		wantCode(t, err, CodeStalePlanOrDrift)
		// and a signature over the mutated bytes still cannot pass the journaled request
		_, _, err = Issue(ctx, r.coord, mutated, r.key.sign(mutated, SignatureNamespace, 0x01), r.signers, r.now)
		wantCode(t, err, CodeStalePlanOrDrift)
	})
	t.Run("window closed at issue", func(t *testing.T) {
		r := newFlowRig(t)
		p, raw, _ := Request(ctx, r.coord, RequestInput{Plan: []byte(flowPlan), Expires: time.Hour, Now: r.now})
		_, _, err := Issue(ctx, r.coord, raw, r.key.sign(raw, SignatureNamespace, 0x01), r.signers, p.ExpiresAt)
		wantCode(t, err, CodeGrantExpiredOrRevoked)
	})
	t.Run("request refuses a window over 24h", func(t *testing.T) {
		r := newFlowRig(t)
		if _, _, err := Request(ctx, r.coord, RequestInput{Plan: []byte(flowPlan), Expires: 25 * time.Hour, Now: r.now}); err == nil {
			t.Fatal("25h window accepted")
		}
	})
	t.Run("signature from another store", func(t *testing.T) {
		a := newFlowRig(t)
		b := newFlowRig(t)
		b.key, b.signers = a.key, a.signers
		_, raw, _ := Request(ctx, a.coord, RequestInput{Plan: []byte(flowPlan), Expires: time.Hour, Now: a.now})
		_, _, err := Issue(ctx, b.coord, raw, a.key.sign(raw, SignatureNamespace, 0x01), a.signers, a.now)
		wantCode(t, err, CodeUnauthorizedPrincipal) // no such request in store b
	})
}

func TestShowRefusesMismatchedPlan(t *testing.T) {
	r := newFlowRig(t)
	_, raw, _ := Request(context.Background(), r.coord, RequestInput{Plan: []byte(flowPlan), Expires: time.Hour, Now: r.now})
	other := strings.Replace(flowPlan, `/r/b`, `/r/c`, 1)
	if _, _, err := Show([]byte(other), raw); RefusalCode(err) != CodeStalePlanOrDrift {
		t.Fatalf("show accepted a different plan: %v", err)
	}
}

func TestApprovedExecutionWithholdsUnknownDriftAndEnforcesLimits(t *testing.T) {
	r := newFlowRig(t)
	p := r.approve(flowPlan, nil)
	r.drift["/r/a"] = engine.DriftUnknown
	res, err := r.executor().ExecuteApproved(context.Background(), ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan)})
	if err != nil || strings.Join(r.deleted, ",") != "/r/b" || res.Outcomes[0].Outcome != OutcomeWithheld {
		t.Fatalf("unknown drift: %v deleted=%v outcomes=%+v", err, r.deleted, res.Outcomes)
	}

	r2 := newFlowRig(t)
	p2 := r2.approve(flowPlan, nil)
	r2.extra = 1
	_, err = r2.executor().ExecuteApproved(context.Background(), ExecuteInput{ApprovalID: p2.ApprovalID, Plan: []byte(flowPlan)})
	wantCode(t, err, CodeLimitsExceeded)
	if len(r2.deleted) != 0 {
		t.Fatalf("limits: deleted %v", r2.deleted)
	}
	// limits are checked after consumption: the approval is spent (C3)
	head, _ := r2.coord.LoadApproval(context.Background(), p2.ApprovalID)
	if head.Record.State != daemon.ApprovalConsumed {
		t.Fatalf("state %s", head.Record.State)
	}
}

func TestApprovedExecutionExpiryMidRunLeavesRestUnattempted(t *testing.T) {
	r := newFlowRig(t)
	p := r.approve(flowPlan, nil)
	ex := r.executor()
	inner := ex.Delete
	ex.Delete = func(ctx context.Context, c *engine.PruneCandidate) (string, string) {
		out, d := inner(ctx, c)
		r.now = p.ExpiresAt // expires after the first effect
		return out, d
	}
	res, err := ex.ExecuteApproved(context.Background(), ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan)})
	wantCode(t, err, CodeGrantExpiredOrRevoked)
	if strings.Join(r.deleted, ",") != "/r/a" || res.Outcomes[1].Outcome != OutcomeUnattempted {
		t.Fatalf("deleted %v outcomes %+v", r.deleted, res.Outcomes)
	}
}

func TestApprovedExecutionCancelLeavesRestUnattempted(t *testing.T) {
	r := newFlowRig(t)
	p := r.approve(flowPlan, nil)
	ex := r.executor()
	ctx, cancel := context.WithCancel(context.Background())
	inner := ex.Delete
	ex.Delete = func(c context.Context, cand *engine.PruneCandidate) (string, string) {
		out, d := inner(c, cand)
		cancel()
		return out, d
	}
	res, err := ex.ExecuteApproved(ctx, ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan)})
	if err == nil || len(r.deleted) != 1 || res.Outcomes[1].Outcome != OutcomeUnattempted {
		t.Fatalf("cancel: %v deleted=%v outcomes=%+v", err, r.deleted, res.Outcomes)
	}
}

func TestPersistenceUncertainBeforeAnyEffect(t *testing.T) {
	r := newFlowRig(t)
	p := r.approve(flowPlan, nil)
	_ = r.coord.Close() // the store is gone: nothing can be journaled
	_, err := r.executor().ExecuteApproved(context.Background(), ExecuteInput{ApprovalID: p.ApprovalID, Plan: []byte(flowPlan)})
	wantCode(t, err, CodePersistenceUncertain)
	if len(r.deleted) != 0 {
		t.Fatalf("deleted %v", r.deleted)
	}
}
