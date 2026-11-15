//go:build darwin || linux

package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/3leaps/spanwit/internal/canonjson"
	"github.com/3leaps/spanwit/internal/daemon"
	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/serviceexec"
	"github.com/google/uuid"
)

// DomainRequest is the digest domain of the execution request fingerprint.
const DomainRequest = "spanwit/approval-execution-request/v0"

// RequestInput describes a new approval request.
type RequestInput struct {
	Plan        []byte // exact plan bytes, read once by the caller
	MinAge      *string
	Expires     time.Duration
	BindProfile bool // bind the active profile (profile-derived plan)
	Now         time.Time
}

// Request computes every binding from the plan bytes, chooses the approval
// id, journals the request (no authority) and returns the canonical payload
// to be shown and signed.
func Request(ctx context.Context, c *daemon.Coordinator, in RequestInput) (Payload, []byte, error) {
	id, err := IdentifyPlan(in.Plan)
	if err != nil {
		return Payload{}, nil, err
	}
	limits, err := planLimits(in.Plan)
	if err != nil {
		return Payload{}, nil, err
	}
	catalog, err := SignatureCatalogDigest()
	if err != nil {
		return Payload{}, nil, err
	}
	journalID, err := c.JournalID(ctx)
	if err != nil {
		return Payload{}, nil, refuse(CodePersistenceUncertain, err, "journal identity")
	}
	var policy *PolicyRef
	if in.BindProfile {
		head, err := c.ProfileHead(ctx)
		if err != nil {
			return Payload{}, nil, err
		}
		if !head.Active {
			return Payload{}, nil, fmt.Errorf("no active profile to bind")
		}
		hp, err := headPin(head.Pin.Revision, head.Pin.Digest)
		if err != nil {
			return Payload{}, nil, err
		}
		policy = &PolicyRef{PackRevision: head.PackRevision, CompiledPolicyDigest: head.CompiledPolicyDigest, ProfileHead: hp}
	}
	issued := in.Now.UTC().Truncate(time.Second)
	p := Payload{
		Mode: ModeOneShotPlan, Action: ActionPruneExec, ApprovalID: uuid.NewString(), JournalID: journalID,
		PlanDigest: id.PlanDigest, CandidateSetDigest: id.CandidateSetDigest, AnalysisRoot: id.AnalysisRoot,
		PolicyRef: policy, SignatureCatalogDigest: catalog, EffectiveOptions: EffectiveOptions{MinAge: in.MinAge},
		Limits: limits, IssuedAt: issued, ExpiresAt: issued.Add(in.Expires),
	}
	raw, err := p.Canonical()
	if err != nil {
		return Payload{}, nil, err
	}
	if _, err := c.RequestApproval(ctx, p.ApprovalID, raw); err != nil {
		return Payload{}, nil, refuse(CodePersistenceUncertain, err, "journal the request")
	}
	return p, raw, nil
}

// planLimits sums the plan's own candidate count and size_bytes.
func planLimits(plan []byte) (Limits, error) {
	v, err := canonjson.Parse(plan)
	if err != nil {
		return Limits{}, err
	}
	cands, _ := v.(canonjson.Object)["candidates"].([]canonjson.Value)
	var l Limits
	for _, c := range cands {
		obj, _ := c.(canonjson.Object)
		size, ok := obj["size_bytes"].(int64)
		if !ok || size < 0 {
			return Limits{}, fmt.Errorf("exact plan: candidate without a whole size_bytes")
		}
		l.MaxCandidates++
		l.MaxReclaimBytes += size
	}
	return l, nil
}

// Show checks that payload binds exactly this plan (digests, candidate set,
// root) and the current signature catalog, and returns both for display. It
// is read-only; the caller prints the summary before anything is signed.
func Show(plan, payload []byte) (Payload, PlanIdentity, error) {
	p, err := ParsePayload(payload)
	if err != nil {
		return Payload{}, PlanIdentity{}, err
	}
	id, err := IdentifyPlan(plan)
	if err != nil {
		return Payload{}, PlanIdentity{}, err
	}
	if p.PlanDigest != id.PlanDigest || p.CandidateSetDigest != id.CandidateSetDigest || p.AnalysisRoot != id.AnalysisRoot {
		return Payload{}, PlanIdentity{}, refuse(CodeStalePlanOrDrift, nil, "payload does not bind this plan")
	}
	catalog, err := SignatureCatalogDigest()
	if err != nil {
		return Payload{}, PlanIdentity{}, err
	}
	if p.SignatureCatalogDigest != catalog {
		return Payload{}, PlanIdentity{}, refuse(CodeStalePlanOrDrift, nil, "payload binds a different signature catalog")
	}
	return p, id, nil
}

// Sign runs Show's checks and then has ssh-keygen sign the same in-memory
// payload bytes (stdin), so the signed bytes are the bytes that were checked
// and displayed. confirm is called with the parsed payload before signing.
func Sign(ctx context.Context, plan, payload []byte, keyPath string, confirm func(Payload, PlanIdentity) bool) ([]byte, error) {
	p, id, err := Show(plan, payload)
	if err != nil {
		return nil, err
	}
	if !confirm(p, id) {
		return nil, errors.New("not confirmed; nothing signed")
	}
	if _, err := TrustedFile(sshKeygenPath, 64<<20); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVerifierUnavailable, err)
	}
	res := serviceexec.ExecRunner{}.Run(ctx, serviceexec.Request{
		Program: sshKeygenPath, Args: []string{"-Y", "sign", "-n", SignatureNamespace, "-f", keyPath},
		Dir: "/", Timeout: 30 * time.Second, Stdin: payload,
	})
	if res.Outcome != serviceexec.Success {
		return nil, fmt.Errorf("ssh-keygen sign: %s: %s", res.Outcome, firstLine(res.Stderr))
	}
	if _, err := ParseSignature(res.Stdout); err != nil {
		return nil, fmt.Errorf("produced signature is not acceptable: %w", err)
	}
	return res.Stdout, nil
}

func firstLine(b []byte) string {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i])
		}
	}
	return string(b)
}

// Issue verifies sig over the journaled request payload and records the
// decision. The payload bytes given must equal the journaled request bytes.
func Issue(ctx context.Context, c *daemon.Coordinator, payload, sig []byte, allowedSigners string, now time.Time) (daemon.ApprovalHead, Verified, error) {
	p, err := ParsePayload(payload)
	if err != nil {
		return daemon.ApprovalHead{}, Verified{}, err
	}
	head, err := c.LoadApproval(ctx, p.ApprovalID)
	if err != nil {
		return daemon.ApprovalHead{}, Verified{}, refuse(CodeUnauthorizedPrincipal, err, "no journaled request %s", p.ApprovalID)
	}
	if head.Record.State != daemon.ApprovalRequested {
		return daemon.ApprovalHead{}, Verified{}, refuse(CodeGrantExpiredOrRevoked, nil, "approval is %s, not requested", head.Record.State)
	}
	if string(head.Record.Payload) != string(payload) {
		return daemon.ApprovalHead{}, Verified{}, refuse(CodeStalePlanOrDrift, nil, "payload differs from the journaled request")
	}
	jid, err := c.JournalID(ctx)
	if err != nil || jid != p.JournalID {
		return daemon.ApprovalHead{}, Verified{}, refuse(CodeStalePlanOrDrift, err, "payload belongs to another store")
	}
	if !now.Before(p.ExpiresAt) {
		return daemon.ApprovalHead{}, Verified{}, refuse(CodeGrantExpiredOrRevoked, nil, "approval window closed at %s", p.ExpiresAt.Format(time.RFC3339))
	}
	v, err := Verify(ctx, payload, sig, allowedSigners)
	if err != nil {
		return daemon.ApprovalHead{}, Verified{}, verifyRefusal(err)
	}
	issued, err := c.IssueApproval(ctx, head, string(sig), daemon.ApprovalDecision{
		Principal: v.Principal, KeyType: v.KeyType, Assurance: v.Assurance, DecidedAt: now.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return daemon.ApprovalHead{}, Verified{}, journalRefusal(err)
	}
	return issued, v, nil
}

func verifyRefusal(err error) error {
	if errors.Is(err, ErrAllowedSignersUntrusted) {
		return refuse(CodeAllowedSignersUntrusted, err, "%v", err)
	}
	return refuse(CodeUnauthorizedPrincipal, err, "%v", err)
}

func journalRefusal(err error) error {
	if errors.Is(err, daemon.ErrPersistenceUncertain) {
		return refuse(CodePersistenceUncertain, err, "%v", err)
	}
	if errors.Is(err, daemon.ErrApprovalTransition) || errors.Is(err, daemon.ErrApprovalConflict) {
		return refuse(CodeStalePlanOrDrift, err, "%v", err)
	}
	return err
}

// Revoke revokes a requested or issued approval.
func Revoke(ctx context.Context, c *daemon.Coordinator, approvalID string) (daemon.ApprovalHead, error) {
	head, err := c.LoadApproval(ctx, approvalID)
	if err != nil {
		return daemon.ApprovalHead{}, err
	}
	if head.Record.State == daemon.ApprovalConsumed {
		return daemon.ApprovalHead{}, refuse(CodeGrantAlreadyConsumed, nil, "approval already consumed; nothing to revoke")
	}
	out, err := c.RevokeApproval(ctx, head)
	if err != nil {
		return daemon.ApprovalHead{}, journalRefusal(err)
	}
	return out, nil
}

// Candidate outcomes recorded per candidate (E4/E5).
const (
	OutcomeApplied     = "applied"
	OutcomeWithheld    = "withheld"
	OutcomeFailed      = "failed"
	OutcomePartial     = "partial"
	OutcomeUnattempted = "unattempted"
	OutcomeUnknown     = "unknown"
)

// Deleter removes one prunable candidate through the destructive guard and
// reports what happened: applied, failed (guard refused; nothing removed) or
// partial (an error after removal may have started).
type Deleter func(ctx context.Context, cand *engine.PruneCandidate) (outcome, detail string)

// Planner revalidates the exact plan bytes into a plan without deleting.
type Planner func(ctx context.Context, plan []byte, minAge string) (engine.PrunePlan, error)

// Executor runs journaled executions under the coordinator lock.
type Executor struct {
	Coord          *daemon.Coordinator
	AllowedSigners string
	Now            func() time.Time
	Plan           Planner
	Delete         Deleter
}

// Result is what an execution did, by candidate.
type Result struct {
	OperationID string
	Replayed    bool // an exact retry: the original operation, no new effect
	Plan        engine.PrunePlan
	Outcomes    []daemon.ExecutionRecord
	// Report projects every candidate of the operation: its recorded
	// outcome, or unknown when none was recorded (set on replay).
	Report *daemon.OperationReport
}

// ExecuteInput is one approved execution request.
type ExecuteInput struct {
	ApprovalID  string
	Plan        []byte // read once by the caller
	MinAge      *string
	OperationID string // set only to retry a previous operation exactly
}

// ExecuteApproved implements N6.
func (e Executor) ExecuteApproved(ctx context.Context, in ExecuteInput) (Result, error) {
	now := e.Now()
	id, err := IdentifyPlan(in.Plan)
	if err != nil {
		return Result{}, refuse(CodeStalePlanOrDrift, err, "%v", err)
	}
	catalog, err := SignatureCatalogDigest()
	if err != nil {
		return Result{}, err
	}
	jid, err := e.Coord.JournalID(ctx)
	if err != nil {
		return Result{}, refuse(CodePersistenceUncertain, err, "journal identity")
	}
	head, err := e.Coord.LoadApproval(ctx, in.ApprovalID)
	if err != nil {
		if errors.Is(err, daemon.ErrApprovalNotFound) {
			return Result{}, refuse(CodeUnauthorizedPrincipal, err, "no approval %s in this store", in.ApprovalID)
		}
		return Result{}, refuse(CodePersistenceUncertain, err, "read approval")
	}
	p, err := ParsePayload(head.Record.Payload)
	if err != nil {
		return Result{}, refuse(CodeUnauthorizedPrincipal, err, "stored payload invalid")
	}
	var currentPolicy *PolicyRef
	if p.PolicyRef != nil {
		ph, err := e.Coord.ProfileHead(ctx)
		if err != nil {
			return Result{}, refuse(CodePersistenceUncertain, err, "read profile head")
		}
		if ph.Active {
			hp, err := headPin(ph.Pin.Revision, ph.Pin.Digest)
			if err != nil {
				return Result{}, refuse(CodeStalePlanOrDrift, err, "profile head")
			}
			currentPolicy = &PolicyRef{PackRevision: ph.PackRevision, CompiledPolicyDigest: ph.CompiledPolicyDigest, ProfileHead: hp}
		}
	}
	fp, err := requestFingerprint(in.ApprovalID, jid, id.PlanDigest, in.MinAge, currentPolicy, catalog)
	if err != nil {
		return Result{}, err
	}

	// Replay classification comes first (entarch E2 carried literally).
	if head.Record.State == daemon.ApprovalConsumed {
		cons := head.Record.Consumption
		switch {
		case in.OperationID != "" && cons != nil && in.OperationID == cons.OperationID && fp == cons.RequestFingerprint:
			recs, err := e.Coord.ExecutionRecords(ctx, cons.OperationID)
			if err != nil {
				return Result{}, refuse(CodePersistenceUncertain, err, "read original outcomes")
			}
			report, err := e.Coord.OperationReport(ctx, cons.OperationID, in.ApprovalID)
			if err != nil {
				return Result{}, refuse(CodePersistenceUncertain, err, "project original outcomes")
			}
			return Result{OperationID: cons.OperationID, Replayed: true, Outcomes: recs, Report: &report}, nil
		case in.OperationID != "" && cons != nil && in.OperationID == cons.OperationID:
			return Result{}, refuse(CodeIdempotencyPayloadMismatch, nil, "operation %s was consumed with a different request", in.OperationID)
		default:
			return Result{}, refuse(CodeGrantAlreadyConsumed, nil, "approval %s already consumed by operation %s", in.ApprovalID, consumedOp(cons))
		}
	}
	switch head.Record.State {
	case daemon.ApprovalRevoked:
		return Result{}, refuse(CodeGrantExpiredOrRevoked, nil, "approval %s is revoked", in.ApprovalID)
	case daemon.ApprovalRequested:
		return Result{}, refuse(CodeUnauthorizedPrincipal, nil, "approval %s was requested but never issued", in.ApprovalID)
	case daemon.ApprovalIssued:
	default:
		return Result{}, refuse(CodeUnauthorizedPrincipal, nil, "approval %s in unknown state %q", in.ApprovalID, head.Record.State)
	}
	if in.OperationID != "" {
		return Result{}, refuse(CodeIdempotencyPayloadMismatch, nil, "operation %s does not exist for this approval", in.OperationID)
	}

	// Authority: re-verify the stored signature now; never trust the label.
	if _, err := Verify(ctx, head.Record.Payload, []byte(head.Record.Signature), e.AllowedSigners); err != nil {
		return Result{}, verifyRefusal(err)
	}
	if !now.Before(p.ExpiresAt) {
		return Result{}, refuse(CodeGrantExpiredOrRevoked, nil, "approval expired at %s", p.ExpiresAt.Format(time.RFC3339))
	}
	switch {
	case p.JournalID != jid:
		return Result{}, refuse(CodeStalePlanOrDrift, nil, "approval belongs to another store")
	case p.PlanDigest != id.PlanDigest || p.CandidateSetDigest != id.CandidateSetDigest || p.AnalysisRoot != id.AnalysisRoot:
		return Result{}, refuse(CodeStalePlanOrDrift, nil, "plan differs from the approved plan")
	case p.SignatureCatalogDigest != catalog:
		return Result{}, refuse(CodeStalePlanOrDrift, nil, "signature catalog changed since approval")
	case !sameOptional(p.EffectiveOptions.MinAge, in.MinAge):
		return Result{}, refuse(CodeStalePlanOrDrift, nil, "--min-age differs from the approved value")
	case !samePolicy(p.PolicyRef, currentPolicy):
		return Result{}, refuse(CodeStalePlanOrDrift, nil, "active profile changed since approval")
	}

	// Durable intent before any effect: plan body, then consumption.
	planPin, err := e.Coord.StorePlan(ctx, in.Plan)
	if err != nil {
		return Result{}, journalRefusal(err)
	}
	op := daemon.NewOperationID()
	if _, err := e.Coord.ConsumeApproval(ctx, head, daemon.ApprovalConsumption{
		OperationID: op, RequestFingerprint: fp, Plan: planPin, ConsumedAt: now.UTC().Format(time.RFC3339),
	}); err != nil {
		return Result{}, journalRefusal(err)
	}
	minAge := ""
	if in.MinAge != nil {
		minAge = *in.MinAge
	}
	return e.run(ctx, op, in.ApprovalID, in.Plan, minAge, &p)
}

// ExecuteUnapproved is the journaled explicit --execute (X1): the same
// coordinator, plan and outcome records, without an approval.
func (e Executor) ExecuteUnapproved(ctx context.Context, plan engine.PrunePlan) (Result, error) {
	return e.runPlanned(ctx, daemon.NewOperationID(), "", plan, nil)
}

func (e Executor) run(ctx context.Context, op, approvalID string, raw []byte, minAge string, p *Payload) (Result, error) {
	// Journal writes from here on never use the cancellable context: a
	// cancellation stops new deletions, not the record of what happened.
	jctx := context.WithoutCancel(ctx)
	if err := e.Coord.AppendExecution(jctx, daemon.ExecutionRecord{OperationID: op, Kind: "started", ApprovalID: approvalID}); err != nil {
		return Result{OperationID: op}, journalRefusal(err)
	}
	plan, err := e.Plan(ctx, raw, minAge)
	if err != nil {
		if jerr := e.Coord.AppendExecution(jctx, daemon.ExecutionRecord{OperationID: op, Kind: "finished", Detail: "plan failed: " + err.Error()}); jerr != nil {
			return Result{OperationID: op}, journalRefusal(jerr)
		}
		return Result{OperationID: op}, refuse(CodeStalePlanOrDrift, err, "revalidate plan: %v", err)
	}
	// Under an approval, unknown drift withholds too.
	for i := range plan.Candidates {
		c := &plan.Candidates[i]
		if c.State == engine.CandidateStatePrunable && c.Drift != nil && (c.Drift.Size == engine.DriftUnknown || c.Drift.Activity == engine.DriftUnknown) {
			c.State, c.WithheldReason = engine.CandidateStateWithheld, engine.WithheldReasonIndeterminate
		}
	}
	if err := e.recordManifest(jctx, op, plan); err != nil {
		return Result{OperationID: op, Plan: plan}, err
	}
	var count, bytes int64
	for _, c := range plan.Candidates {
		if c.State == engine.CandidateStatePrunable {
			count++
			bytes += c.Size
		}
	}
	if count > p.Limits.MaxCandidates || bytes > p.Limits.MaxReclaimBytes {
		res := Result{OperationID: op, Plan: plan}
		for i, c := range plan.Candidates {
			rec := daemon.ExecutionRecord{OperationID: op, Kind: "outcome", Index: i, Path: c.Path, Outcome: OutcomeUnattempted, Reason: CodeLimitsExceeded}
			if err := e.Coord.AppendExecution(jctx, rec); err != nil {
				return res, journalRefusal(err)
			}
			res.Outcomes = append(res.Outcomes, rec)
		}
		if err := e.Coord.AppendExecution(jctx, daemon.ExecutionRecord{OperationID: op, Kind: "finished", Detail: CodeLimitsExceeded}); err != nil {
			return res, journalRefusal(err)
		}
		return res, refuse(CodeLimitsExceeded, nil, "%d candidates / %d bytes exceed the approved %d / %d", count, bytes, p.Limits.MaxCandidates, p.Limits.MaxReclaimBytes)
	}
	return e.loop(ctx, op, plan, p.ExpiresAt)
}

func (e Executor) runPlanned(ctx context.Context, op, approvalID string, plan engine.PrunePlan, _ *Payload) (Result, error) {
	jctx := context.WithoutCancel(ctx)
	if err := e.Coord.AppendExecution(jctx, daemon.ExecutionRecord{OperationID: op, Kind: "started", ApprovalID: approvalID}); err != nil {
		return Result{OperationID: op, Plan: plan}, journalRefusal(err)
	}
	if err := e.recordManifest(jctx, op, plan); err != nil {
		return Result{OperationID: op, Plan: plan}, err
	}
	return e.loop(ctx, op, plan, time.Time{})
}

// recordManifest stores the candidates the loop will attempt, in loop
// order, and references them from the operation before the first effect,
// so an interrupted run can be projected candidate by candidate.
func (e Executor) recordManifest(ctx context.Context, op string, plan engine.PrunePlan) error {
	m := daemon.ExecutionManifest{Schema: daemon.ExecutionManifestSchema, OperationID: op, Candidates: []daemon.ManifestCandidate{}}
	for i, c := range plan.Candidates {
		m.Candidates = append(m.Candidates, daemon.ManifestCandidate{Index: i, Path: c.Path, State: c.State, SizeBytes: c.Size})
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	pin, err := e.Coord.StoreManifest(ctx, raw)
	if err != nil {
		return journalRefusal(err)
	}
	if err := e.Coord.AppendExecution(ctx, daemon.ExecutionRecord{OperationID: op, Kind: "manifest", Manifest: &pin}); err != nil {
		return journalRefusal(err)
	}
	return nil
}

// loop deletes prunable candidates one at a time, recording each outcome
// after its effect. A journal failure stops further deletions. Expiry or
// cancellation records the remaining candidates as unattempted and stops.
func (e Executor) loop(ctx context.Context, op string, plan engine.PrunePlan, expires time.Time) (Result, error) {
	jctx := context.WithoutCancel(ctx)
	res := Result{OperationID: op, Plan: plan}
	stopReason := ""
	for i := range res.Plan.Candidates {
		c := &res.Plan.Candidates[i]
		rec := daemon.ExecutionRecord{OperationID: op, Kind: "outcome", Index: i, Path: c.Path}
		switch {
		case stopReason != "":
			rec.Outcome, rec.Reason = OutcomeUnattempted, stopReason
		case c.State != engine.CandidateStatePrunable:
			rec.Outcome, rec.Reason = OutcomeWithheld, c.WithheldReason
		case ctx.Err() != nil:
			stopReason = "cancelled"
			rec.Outcome, rec.Reason = OutcomeUnattempted, stopReason
		case !expires.IsZero() && !e.Now().Before(expires):
			stopReason = CodeGrantExpiredOrRevoked
			rec.Outcome, rec.Reason = OutcomeUnattempted, stopReason
		default:
			rec.Outcome, rec.Detail = e.Delete(ctx, c)
			rec.Bytes = c.Size
			c.Deleted = rec.Outcome == OutcomeApplied
		}
		if err := e.Coord.AppendExecution(jctx, rec); err != nil {
			return res, refuse(CodePersistenceUncertain, err, "outcome for %s not recorded; stopped before further deletions", c.Path)
		}
		res.Outcomes = append(res.Outcomes, rec)
	}
	if err := e.Coord.AppendExecution(jctx, daemon.ExecutionRecord{OperationID: op, Kind: "finished", Detail: stopReason}); err != nil {
		return res, refuse(CodePersistenceUncertain, err, "finish record not written")
	}
	if stopReason != "" {
		return res, refuse(stopCode(stopReason), nil, "stopped: %s", stopReason)
	}
	return res, nil
}

func stopCode(reason string) string {
	if reason == CodeGrantExpiredOrRevoked {
		return CodeGrantExpiredOrRevoked
	}
	return "cancelled"
}

func consumedOp(c *daemon.ApprovalConsumption) string {
	if c == nil {
		return "unknown"
	}
	return c.OperationID
}

func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func samePolicy(approved, current *PolicyRef) bool {
	if approved == nil {
		return true // hand-built plan: binds the signature catalog only
	}
	return current != nil && *approved == *current
}

func requestFingerprint(approvalID, journalID, planDigest string, minAge *string, policy *PolicyRef, catalog string) (string, error) {
	var ma, pol canonjson.Value
	if minAge != nil {
		ma = *minAge
	}
	if policy != nil {
		pol = canonjson.Object{"pack_revision": policy.PackRevision, "compiled_policy_digest": policy.CompiledPolicyDigest,
			"profile_head": canonjson.Object{"revision": policy.ProfileHead.Revision, "digest": policy.ProfileHead.Digest}}
	}
	d, err := canonjson.Digest(DomainRequest, canonjson.Object{
		"approval_id": approvalID, "journal_id": journalID, "plan_digest": planDigest,
		"effective_options": canonjson.Object{"min_age": ma}, "policy_ref": pol, "signature_catalog_digest": catalog,
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + d, nil
}

// pathExists reports whether p still exists (used to classify a delete error).
func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil || !os.IsNotExist(err)
}

// GuardedDelete is the production Deleter: the shared destructive guard
// (AuthorizeDeletion then RemoveGuarded). A guard refusal removes nothing
// (failed); an error during removal is partial while anything remains, and
// applied (with the error as detail) when the target is gone.
func GuardedDelete(_ context.Context, c *engine.PruneCandidate) (string, string) {
	tok, err := engine.AuthorizeDeletion(engine.DeletionRequest{
		Target: c.Path, Boundary: c.AuthorizationBoundary, Relation: c.ContainmentRelation, Provenance: c.ProvenanceKind,
	})
	if err != nil {
		return OutcomeFailed, "guard refused: " + err.Error()
	}
	if err := engine.RemoveGuarded(tok); err != nil {
		if pathExists(c.Path) {
			return OutcomePartial, err.Error()
		}
		return OutcomeApplied, err.Error()
	}
	return OutcomeApplied, ""
}
