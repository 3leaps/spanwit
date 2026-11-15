package approval

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/3leaps/spanwit/internal/canonjson"
)

// Payload is the signed approval payload spanwit-approval/v0 (N3). Its
// canonical JSON v0 encoding is exactly the bytes the maintainer signs.
type Payload struct {
	Mode                   string
	Action                 string
	ApprovalID             string
	JournalID              string
	PlanDigest             string
	CandidateSetDigest     string
	AnalysisRoot           string
	PolicyRef              *PolicyRef
	SignatureCatalogDigest string
	EffectiveOptions       EffectiveOptions
	Limits                 Limits
	IssuedAt               time.Time
	ExpiresAt              time.Time
}

// PolicyRef binds a profile-derived plan to the policy in force; nil for a
// hand-built plan.
type PolicyRef struct {
	PackRevision         string
	CompiledPolicyDigest string
	ProfileHead          HeadPin
}

// HeadPin is the authoritative profile stream head at request time.
type HeadPin struct {
	Revision int64
	Digest   string
}

// headPin converts a journal pin revision, refusing one beyond the safe
// integer range (a journal never reaches it; a corrupt one might).
func headPin(revision uint64, digest string) (HeadPin, error) {
	if revision == 0 || revision > canonjson.MaxSafeInteger {
		return HeadPin{}, fmt.Errorf("%w: profile head revision %d out of range", ErrInvalidPayload, revision)
	}
	return HeadPin{Revision: int64(revision), Digest: digest}, nil
}

// EffectiveOptions is the closed set of behavior-changing execute options an
// approval binds. v0: min_age only (nil = not given).
type EffectiveOptions struct {
	MinAge *string
}

// Limits bound what an approved execution may delete.
type Limits struct {
	MaxCandidates   int64
	MaxReclaimBytes int64
}

// Fixed values and bounds.
const (
	ModeOneShotPlan   = "one_shot_plan"
	ActionPruneExec   = "prune.execute"
	MaxApprovalWindow = 24 * time.Hour
	instantLayout     = "2006-01-02T15:04:05Z"
)

// ErrInvalidPayload wraps every payload refusal.
var ErrInvalidPayload = errors.New("invalid approval payload")

var payloadMembers = []string{
	"action", "analysis_root", "approval_id", "candidate_set_digest", "effective_options",
	"executor", "expires_at", "issued_at", "journal_id", "limits", "mode", "plan_digest",
	"policy_ref", "signature_catalog_digest",
}

// Canonical returns the canonical JSON v0 bytes of p (the signed bytes).
func (p Payload) Canonical() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var policy canonjson.Value
	if p.PolicyRef != nil {
		policy = canonjson.Object{
			"pack_revision":          p.PolicyRef.PackRevision,
			"compiled_policy_digest": p.PolicyRef.CompiledPolicyDigest,
			"profile_head": canonjson.Object{
				"revision": p.PolicyRef.ProfileHead.Revision,
				"digest":   p.PolicyRef.ProfileHead.Digest,
			},
		}
	}
	var minAge canonjson.Value
	if p.EffectiveOptions.MinAge != nil {
		minAge = *p.EffectiveOptions.MinAge
	}
	return canonjson.Encode(canonjson.Object{
		"mode":                     p.Mode,
		"action":                   p.Action,
		"approval_id":              p.ApprovalID,
		"journal_id":               p.JournalID,
		"plan_digest":              p.PlanDigest,
		"candidate_set_digest":     p.CandidateSetDigest,
		"analysis_root":            p.AnalysisRoot,
		"policy_ref":               policy,
		"signature_catalog_digest": p.SignatureCatalogDigest,
		"effective_options":        canonjson.Object{"min_age": minAge},
		"limits": canonjson.Object{
			"max_candidates":    p.Limits.MaxCandidates,
			"max_reclaim_bytes": p.Limits.MaxReclaimBytes,
		},
		"executor":   nil,
		"issued_at":  p.IssuedAt.UTC().Format(instantLayout),
		"expires_at": p.ExpiresAt.UTC().Format(instantLayout),
	})
}

// Validate checks the payload's own invariants (not authority).
func (p Payload) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidPayload, fmt.Sprintf(format, a...))
	}
	switch {
	case p.Mode != ModeOneShotPlan:
		return bad("mode %q", p.Mode)
	case p.Action != ActionPruneExec:
		return bad("action %q", p.Action)
	case p.ApprovalID == "" || p.JournalID == "":
		return bad("approval_id and journal_id are required")
	case !isDigest(p.PlanDigest) || !isDigest(p.CandidateSetDigest) || !isDigest(p.SignatureCatalogDigest):
		return bad("malformed digest")
	case !strings.HasPrefix(p.AnalysisRoot, "/"):
		return bad("analysis_root must be absolute")
	case p.Limits.MaxCandidates < 0 || p.Limits.MaxReclaimBytes < 0:
		return bad("negative limit")
	}
	if p.PolicyRef != nil && (!isDigest(p.PolicyRef.PackRevision) || !isDigest(p.PolicyRef.CompiledPolicyDigest) ||
		p.PolicyRef.ProfileHead.Revision <= 0 || p.PolicyRef.ProfileHead.Digest == "") {
		return bad("incomplete policy_ref")
	}
	return ValidateWindow(p.IssuedAt, p.ExpiresAt)
}

// ValidateWindow refuses an expiry window that is reversed, empty or longer
// than MaxApprovalWindow, or not on whole seconds.
func ValidateWindow(issued, expires time.Time) error {
	if issued.IsZero() || expires.IsZero() || !issued.Equal(issued.Truncate(time.Second)) || !expires.Equal(expires.Truncate(time.Second)) {
		return fmt.Errorf("%w: issued_at and expires_at must be whole-second instants", ErrInvalidPayload)
	}
	if !expires.After(issued) {
		return fmt.Errorf("%w: expires_at is not after issued_at", ErrInvalidPayload)
	}
	if expires.Sub(issued) > MaxApprovalWindow {
		return fmt.Errorf("%w: approval window longer than %s", ErrInvalidPayload, MaxApprovalWindow)
	}
	return nil
}

func isDigest(s string) bool {
	if !strings.HasPrefix(s, "sha256:") || len(s) != len("sha256:")+64 {
		return false
	}
	for _, c := range s[len("sha256:"):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ParsePayload decodes signed payload bytes strictly: they must be canonical
// JSON v0 (re-encoding gives the same bytes), carry exactly the v0 member set
// with the right types, executor null, and effective_options exactly
// {min_age}. Any unknown member or option refuses.
func ParsePayload(raw []byte) (Payload, error) {
	bad := func(format string, a ...any) (Payload, error) {
		return Payload{}, fmt.Errorf("%w: %s", ErrInvalidPayload, fmt.Sprintf(format, a...))
	}
	canon, err := canonjson.Canonicalize(raw)
	if err != nil {
		return bad("%v", err)
	}
	if string(canon) != string(raw) {
		return bad("payload is not in canonical form")
	}
	v, _ := canonjson.Parse(raw)
	obj, ok := v.(canonjson.Object)
	if !ok {
		return bad("not an object")
	}
	if err := exactMembers(obj, payloadMembers); err != nil {
		return bad("%v", err)
	}
	str := func(o canonjson.Object, k string) (string, bool) { s, ok := o[k].(string); return s, ok }
	var p Payload
	var okAll = true
	get := func(o canonjson.Object, k string) string {
		s, ok := str(o, k)
		okAll = okAll && ok
		return s
	}
	p.Mode, p.Action, p.ApprovalID, p.JournalID = get(obj, "mode"), get(obj, "action"), get(obj, "approval_id"), get(obj, "journal_id")
	p.PlanDigest, p.CandidateSetDigest, p.AnalysisRoot = get(obj, "plan_digest"), get(obj, "candidate_set_digest"), get(obj, "analysis_root")
	p.SignatureCatalogDigest = get(obj, "signature_catalog_digest")
	issued, expires := get(obj, "issued_at"), get(obj, "expires_at")
	if !okAll {
		return bad("member of the wrong type")
	}
	if obj["executor"] != nil {
		return bad("executor must be null in v0")
	}
	if p.IssuedAt, err = time.Parse(instantLayout, issued); err != nil {
		return bad("issued_at %q", issued)
	}
	if p.ExpiresAt, err = time.Parse(instantLayout, expires); err != nil {
		return bad("expires_at %q", expires)
	}
	eo, ok := obj["effective_options"].(canonjson.Object)
	if !ok {
		return bad("effective_options must be an object")
	}
	if err := exactMembers(eo, []string{"min_age"}); err != nil {
		return bad("effective_options: %v", err)
	}
	switch m := eo["min_age"].(type) {
	case nil:
	case string:
		p.EffectiveOptions.MinAge = &m
	default:
		return bad("effective_options.min_age must be a string or null")
	}
	lim, ok := obj["limits"].(canonjson.Object)
	if !ok || exactMembers(lim, []string{"max_candidates", "max_reclaim_bytes"}) != nil {
		return bad("limits must be {max_candidates, max_reclaim_bytes}")
	}
	mc, ok1 := lim["max_candidates"].(int64)
	mb, ok2 := lim["max_reclaim_bytes"].(int64)
	if !ok1 || !ok2 {
		return bad("limits must be integers")
	}
	p.Limits = Limits{MaxCandidates: mc, MaxReclaimBytes: mb}
	switch pr := obj["policy_ref"].(type) {
	case nil:
	case canonjson.Object:
		if err := exactMembers(pr, []string{"compiled_policy_digest", "pack_revision", "profile_head"}); err != nil {
			return bad("policy_ref: %v", err)
		}
		head, ok := pr["profile_head"].(canonjson.Object)
		if !ok || exactMembers(head, []string{"digest", "revision"}) != nil {
			return bad("policy_ref.profile_head must be {revision, digest}")
		}
		rev, ok1 := head["revision"].(int64)
		dig, ok2 := head["digest"].(string)
		pack, ok3 := pr["pack_revision"].(string)
		comp, ok4 := pr["compiled_policy_digest"].(string)
		if !ok1 || !ok2 || !ok3 || !ok4 || rev <= 0 {
			return bad("policy_ref members of the wrong type")
		}
		p.PolicyRef = &PolicyRef{PackRevision: pack, CompiledPolicyDigest: comp, ProfileHead: HeadPin{Revision: rev, Digest: dig}}
	default:
		return bad("policy_ref must be an object or null")
	}
	if err := p.Validate(); err != nil {
		return Payload{}, err
	}
	return p, nil
}

func exactMembers(o canonjson.Object, want []string) error {
	if len(o) != len(want) {
		var got []string
		for k := range o {
			got = append(got, k)
		}
		sort.Strings(got)
		return fmt.Errorf("members %v, want %v", got, want)
	}
	for _, k := range want {
		if _, ok := o[k]; !ok {
			return fmt.Errorf("missing member %q", k)
		}
	}
	return nil
}
