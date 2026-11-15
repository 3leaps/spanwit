//go:build darwin || linux

package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// TestProfileHeadChangesOnSamePackReactivation is the negative control for
// policy_ref.profile_head: deactivating and reactivating the same pack keeps
// pack_revision and compiled_policy_digest but must change the head pin, so
// an approval bound to the earlier activation no longer matches.
func TestProfileHeadChangesOnSamePackReactivation(t *testing.T) {
	ctx := context.Background()
	stateDir := privateDir(t)
	pack, _ := buildTestProfilePack(t, privateDir(t), 100)
	head := func() ProfileHeadState {
		t.Helper()
		c, err := Open(stateDir, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		h, err := c.ProfileHead(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	if h := head(); h.Active {
		t.Fatalf("initially active: %+v", h)
	}
	if _, err := ActivateProfile(ctx, stateDir, pack, ActivateProfileOptions{Profile: "developer"}); err != nil {
		t.Fatal(err)
	}
	first := head()
	if !first.Active || first.Pin.Revision == 0 {
		t.Fatalf("after activation: %+v", first)
	}
	if _, err := DeactivateProfile(ctx, stateDir, "", ""); err != nil {
		t.Fatal(err)
	}
	if h := head(); h.Active {
		t.Fatalf("after deactivation: %+v", h)
	}
	if _, err := ActivateProfile(ctx, stateDir, pack, ActivateProfileOptions{Profile: "developer"}); err != nil {
		t.Fatal(err)
	}
	second := head()
	if second.PackRevision != first.PackRevision || second.CompiledPolicyDigest != first.CompiledPolicyDigest {
		t.Fatalf("same pack changed content identity: %+v vs %+v", first, second)
	}
	if second.Pin == first.Pin {
		t.Fatalf("reactivation kept the head pin %+v", second.Pin)
	}
}

// TestStorePlanReusesImmutablePlanRecord: storing the same bytes again (a
// second approval of the same plan, or a retry after a failed consume)
// returns the existing pin instead of a conflicting append.
func TestStorePlanReusesImmutablePlanRecord(t *testing.T) {
	c := openApprovalCoordinator(t)
	ctx := context.Background()
	a, err := c.StorePlan(ctx, []byte(`{"p":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.StorePlan(ctx, []byte(`{"p":1}`))
	if err != nil || a != b {
		t.Fatalf("second store: %+v vs %+v (%v)", a, b, err)
	}
	other, err := c.StorePlan(ctx, []byte(`{"p":2}`))
	if err != nil || other == a {
		t.Fatalf("different plan: %+v %v", other, err)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			out[p] = "err:" + err.Error()
			return nil
		}
		sum := ""
		if info.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			sum = fmt.Sprintf("%x", sha256.Sum256(b))
		}
		out[p] = fmt.Sprintf("%v %d %d %s", info.Mode(), info.Size(), info.ModTime().UnixNano(), sum)
		return nil
	})
	return out
}

// TestInspectionIsReadOnly: inspecting creates nothing when there is no
// state and changes nothing (bytes, sizes, modes, mtimes) when there is.
func TestInspectionIsReadOnly(t *testing.T) {
	ctx := context.Background()
	base := privateDir(t)
	absent := filepath.Join(base, "state")
	if _, err := InspectApproval(ctx, absent, "x"); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("absent: %v", err)
	}
	if _, err := InspectOperation(ctx, absent, "op"); err == nil {
		t.Fatal("absent operation reported")
	}
	if _, err := os.Lstat(absent); !os.IsNotExist(err) {
		t.Fatalf("inspection created %s: %v", absent, err)
	}

	dir := privateDir(t)
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RequestApproval(ctx, "a-1", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := c.AppendExecution(ctx, ExecutionRecord{OperationID: "op", Kind: "started"}); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	before := snapshotTree(t, dir)
	insp, err := InspectApproval(ctx, dir, "a-1")
	if err != nil || insp.State != ApprovalRequested {
		t.Fatalf("inspect: %+v %v", insp, err)
	}
	if _, err := InspectApproval(ctx, dir, "missing"); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if rep, err := InspectOperation(ctx, dir, "op"); err != nil || !rep.Started {
		t.Fatalf("operation: %+v %v", rep, err)
	}
	after := snapshotTree(t, dir)
	for p, v := range before {
		if after[p] != v {
			t.Errorf("changed %s:\n  %s\n  %s", p, v, after[p])
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("created %s", p)
		}
	}
}

// TestInspectionWhileCoordinatorRuns refuses cleanly rather than racing.
func TestInspectionWhileCoordinatorRuns(t *testing.T) {
	dir := privateDir(t)
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.RequestApproval(context.Background(), "a-1", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectApproval(context.Background(), dir, "a-1"); err != nil && !errors.Is(err, ErrStoreHeld) {
		t.Fatalf("while held: %v", err)
	}
}
