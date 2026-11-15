// Package approval holds the identities a one-shot plan approval binds:
// domain-separated digests over Canonical JSON v0 (internal/canonjson).
package approval

import (
	"fmt"
	"sort"

	"github.com/3leaps/spanwit/internal/canonjson"
)

// Digest domains. Each identity has its own domain so a digest of one kind
// can never stand in for another.
const (
	DomainPlan         = "spanwit/exact-prune-plan/v1"
	DomainCandidateSet = "spanwit/candidate-set/v1"
)

// PlanIdentity is what an approval binds about an exact plan document.
type PlanIdentity struct {
	PlanDigest         string // over the whole document
	CandidateSetDigest string // over the sorted (path, kind, id) set
	AnalysisRoot       string
	Candidates         int
}

// IdentifyPlan computes the identity of an exact-prune-plan v1 document from
// its raw bytes. The bytes must be the same buffer the caller then plans
// from (read once): the digest covers every member, so any field the
// planner consumes is bound. The document is parsed strictly (duplicate
// members, invalid Unicode, fractions and unsafe integers refuse).
func IdentifyPlan(raw []byte) (PlanIdentity, error) {
	v, err := canonjson.Parse(raw)
	if err != nil {
		return PlanIdentity{}, fmt.Errorf("exact plan: %w", err)
	}
	doc, ok := v.(canonjson.Object)
	if !ok {
		return PlanIdentity{}, fmt.Errorf("exact plan: document is not an object")
	}
	root, ok := doc["analysis_root"].(string)
	if !ok || root == "" {
		return PlanIdentity{}, fmt.Errorf("exact plan: analysis_root missing")
	}
	cands, ok := doc["candidates"].([]canonjson.Value)
	if !ok {
		return PlanIdentity{}, fmt.Errorf("exact plan: candidates missing")
	}
	set := make([]canonjson.Value, 0, len(cands))
	seen := make(map[string]bool, len(cands))
	keys := make([]string, 0, len(cands))
	byPath := make(map[string]canonjson.Value, len(cands))
	for i, c := range cands {
		obj, ok := c.(canonjson.Object)
		if !ok {
			return PlanIdentity{}, fmt.Errorf("exact plan: candidate %d is not an object", i)
		}
		path, ok := obj["path"].(string)
		if !ok || path == "" {
			return PlanIdentity{}, fmt.Errorf("exact plan: candidate %d has no path", i)
		}
		if seen[path] {
			return PlanIdentity{}, fmt.Errorf("exact plan: duplicate candidate path %q", path)
		}
		seen[path] = true
		sig, _ := obj["signature"].(string)
		pat, _ := obj["pattern"].(string)
		var kind, id string
		switch {
		case sig != "" && pat == "":
			kind, id = "signature", sig
		case pat != "" && sig == "":
			kind, id = "pattern", pat
		default:
			return PlanIdentity{}, fmt.Errorf("exact plan: candidate %q needs exactly one of signature or pattern", path)
		}
		keys = append(keys, path)
		byPath[path] = []canonjson.Value{path, kind, id}
	}
	// Byte order of valid UTF-8 paths equals code point order.
	sort.Strings(keys)
	for _, k := range keys {
		set = append(set, byPath[k])
	}
	planDigest, err := canonjson.Digest(DomainPlan, doc)
	if err != nil {
		return PlanIdentity{}, fmt.Errorf("exact plan: %w", err)
	}
	setDigest, err := canonjson.Digest(DomainCandidateSet, set)
	if err != nil {
		return PlanIdentity{}, fmt.Errorf("exact plan: %w", err)
	}
	return PlanIdentity{
		PlanDigest:         "sha256:" + planDigest,
		CandidateSetDigest: "sha256:" + setDigest,
		AnalysisRoot:       root,
		Candidates:         len(cands),
	}, nil
}
