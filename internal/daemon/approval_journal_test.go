package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func openApprovalCoordinator(t *testing.T) *Coordinator {
	t.Helper()
	c, err := Open(privateDir(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestJournalIDStableAcrossReopen(t *testing.T) {
	dir := privateDir(t)
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, err := c.JournalID(ctx)
	if err != nil || a == "" {
		t.Fatalf("journal id %q %v", a, err)
	}
	b, _ := c.JournalID(ctx)
	_ = c.Close()
	c2, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.Close() }()
	again, err := c2.JournalID(ctx)
	if err != nil || a != b || a != again {
		t.Fatalf("journal id changed: %s %s %s (%v)", a, b, again, err)
	}
	if other, _ := openApprovalCoordinator(t).JournalID(ctx); other == a {
		t.Fatal("two stores share a journal id")
	}
}

func TestApprovalLifecycleTransitions(t *testing.T) {
	c := openApprovalCoordinator(t)
	ctx := context.Background()
	payload := []byte(`{"approval_id":"a-1","mode":"one_shot_plan"}`)
	req, err := c.RequestApproval(ctx, "a-1", payload)
	if err != nil {
		t.Fatal(err)
	}
	head, err := c.LoadApproval(ctx, "a-1")
	if err != nil || head.Record.State != ApprovalRequested || string(head.Record.Payload) != string(payload) || head.Pin != req.Pin {
		t.Fatalf("requested head %+v %v", head, err)
	}
	// consume before issue is refused
	if _, err := c.ConsumeApproval(ctx, head, ApprovalConsumption{OperationID: "op"}); !errors.Is(err, ErrApprovalTransition) {
		t.Fatalf("consume from requested: %v", err)
	}
	issued, err := c.IssueApproval(ctx, head, "SIG", ApprovalDecision{Principal: "m", Assurance: "A1"})
	if err != nil || issued.Record.State != ApprovalIssued || issued.Pin.Revision != 2 {
		t.Fatalf("issue: %+v %v", issued, err)
	}
	// a stale predecessor (the requested head) cannot be revised again
	if _, err := c.RevokeApproval(ctx, head); err == nil {
		t.Fatal("revoke from a stale predecessor accepted")
	}
	plan, err := c.StorePlan(ctx, []byte(`{"plan":1}`))
	if err != nil {
		t.Fatal(err)
	}
	cur, _ := c.LoadApproval(ctx, "a-1")
	consumed, err := c.ConsumeApproval(ctx, cur, ApprovalConsumption{OperationID: "op-1", RequestFingerprint: "fp", Plan: plan})
	if err != nil || consumed.Record.State != ApprovalConsumed {
		t.Fatalf("consume: %+v %v", consumed, err)
	}
	cur, _ = c.LoadApproval(ctx, "a-1")
	if _, err := c.ConsumeApproval(ctx, cur, ApprovalConsumption{OperationID: "op-2"}); !errors.Is(err, ErrApprovalTransition) {
		t.Fatalf("second consume: %v", err)
	}
	if _, err := c.RevokeApproval(ctx, cur); !errors.Is(err, ErrApprovalTransition) {
		t.Fatalf("revoke after consume: %v", err)
	}
	got, err := c.LoadPlan(ctx, cur.Record.Consumption.Plan)
	if err != nil || string(got) != `{"plan":1}` {
		t.Fatalf("plan body %q %v", got, err)
	}
}

func TestApprovalRevokeWinsBeforeConsume(t *testing.T) {
	c := openApprovalCoordinator(t)
	ctx := context.Background()
	if _, err := c.RequestApproval(ctx, "a-2", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	h, _ := c.LoadApproval(ctx, "a-2")
	h, err := c.IssueApproval(ctx, h, "S", ApprovalDecision{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RevokeApproval(ctx, h); err != nil {
		t.Fatal(err)
	}
	h, _ = c.LoadApproval(ctx, "a-2")
	if _, err := c.ConsumeApproval(ctx, h, ApprovalConsumption{OperationID: "op"}); !errors.Is(err, ErrApprovalTransition) {
		t.Fatalf("consume after revoke: %v", err)
	}
}

func TestApprovalDuplicateRequestAndMissing(t *testing.T) {
	c := openApprovalCoordinator(t)
	ctx := context.Background()
	if _, err := c.RequestApproval(ctx, "a-3", []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RequestApproval(ctx, "a-3", []byte(`{"x":2}`)); err == nil {
		t.Fatal("re-request of an existing approval id with a different payload accepted")
	}
	if _, err := c.LoadApproval(ctx, "absent"); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestExecutionRecordsRoundTrip(t *testing.T) {
	c := openApprovalCoordinator(t)
	ctx := context.Background()
	op := NewOperationID()
	for _, r := range []ExecutionRecord{
		{OperationID: op, Kind: "started", ApprovalID: "a"},
		{OperationID: op, Kind: "outcome", Index: 0, Path: "/x", Outcome: "applied", Bytes: 5},
		{OperationID: op, Kind: "outcome", Index: 1, Path: "/y", Outcome: "withheld", Reason: "drift"},
		{OperationID: op, Kind: "finished"},
		{OperationID: "other", Kind: "started"},
	} {
		if err := c.AppendExecution(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := c.ExecutionRecords(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, r := range recs {
		kinds = append(kinds, r.Kind+":"+r.Outcome)
	}
	if strings.Join(kinds, ",") != "outcome:applied,outcome:withheld,finished:,started:" {
		t.Fatalf("records %v", kinds)
	}
	// the same outcome record cannot be rewritten
	if err := c.AppendExecution(ctx, ExecutionRecord{OperationID: op, Kind: "outcome", Index: 0, Outcome: "failed"}); err == nil {
		t.Fatal("outcome record rewritten")
	}
}
