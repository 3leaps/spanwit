package approval

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validPayload() Payload {
	d := "sha256:" + strings.Repeat("a", 64)
	issued := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	return Payload{
		Mode: ModeOneShotPlan, Action: ActionPruneExec, ApprovalID: "a-1", JournalID: "j-1",
		PlanDigest: d, CandidateSetDigest: d, AnalysisRoot: "/r", SignatureCatalogDigest: d,
		Limits:   Limits{MaxCandidates: 2, MaxReclaimBytes: 100},
		IssuedAt: issued, ExpiresAt: issued.Add(time.Hour),
	}
}

func TestPayloadRoundTrip(t *testing.T) {
	p := validPayload()
	age := "48h"
	p.EffectiveOptions.MinAge = &age
	p.PolicyRef = &PolicyRef{PackRevision: p.PlanDigest, CompiledPolicyDigest: p.PlanDigest, ProfileHead: HeadPin{Revision: 3, Digest: "sha256:x"}}
	raw, err := p.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParsePayload(raw)
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	again, _ := back.Canonical()
	if string(again) != string(raw) {
		t.Fatalf("round trip changed bytes:\n%s\n%s", raw, again)
	}
	if !strings.Contains(string(raw), `"executor":null`) || !strings.Contains(string(raw), `"min_age":"48h"`) {
		t.Fatalf("payload %s", raw)
	}
}

func TestPayloadRefusals(t *testing.T) {
	raw, _ := validPayload().Canonical()
	s := string(raw)
	for name, mut := range map[string]string{
		"non-canonical whitespace": strings.Replace(s, `{"action"`, `{ "action"`, 1),
		"unknown member":           strings.Replace(s, `"executor":null`, `"executor":null,"extra":1`, 1),
		"executor set":             strings.Replace(s, `"executor":null`, `"executor":"agent"`, 1),
		"unknown option":           strings.Replace(s, `{"min_age":null}`, `{"min_age":null,"reclaim_scope":"whole"}`, 1),
		"missing option":           strings.Replace(s, `{"min_age":null}`, `{}`, 1),
		"wrong mode":               strings.Replace(s, ModeOneShotPlan, "standing_grant", 1),
		"wrong action":             strings.Replace(s, ActionPruneExec, "prune.all", 1),
		"reversed window":          strings.Replace(s, `"expires_at":"2026-11-15T13:00:00Z"`, `"expires_at":"2026-11-15T11:00:00Z"`, 1),
		"window too long":          strings.Replace(s, `"expires_at":"2026-11-15T13:00:00Z"`, `"expires_at":"2026-11-16T12:00:01Z"`, 1),
		"empty window":             strings.Replace(s, `"expires_at":"2026-11-15T13:00:00Z"`, `"expires_at":"2026-11-15T12:00:00Z"`, 1),
		"fractional limit":         strings.Replace(s, `"max_candidates":2`, `"max_candidates":2.0`, 1),
		"relative root":            strings.Replace(s, `"analysis_root":"/r"`, `"analysis_root":"r"`, 1),
		"bad digest":               strings.Replace(s, `"plan_digest":"sha256:`, `"plan_digest":"md5:`, 1),
		"policy_ref partial":       strings.Replace(s, `"policy_ref":null`, `"policy_ref":{"pack_revision":"x"}`, 1),
	} {
		if mut == s {
			t.Fatalf("%s: mutation did not apply", name)
		}
		if _, err := ParsePayload([]byte(mut)); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("%s accepted (err=%v)", name, err)
		}
	}
}

func TestValidateWindowExactly24h(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := ValidateWindow(now, now.Add(24*time.Hour)); err != nil {
		t.Fatalf("24h window refused: %v", err)
	}
	if err := ValidateWindow(now, now.Add(24*time.Hour+time.Second)); err == nil {
		t.Fatal("24h+1s accepted")
	}
	if err := ValidateWindow(now.Add(time.Millisecond), now.Add(time.Hour)); err == nil {
		t.Fatal("sub-second issued_at accepted")
	}
}
