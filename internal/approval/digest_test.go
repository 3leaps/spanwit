package approval

import (
	"strings"
	"testing"
)

const planA = `{"$schema":"x","version":1,"analysis_root":"/r","min_size":0,"sort_mode":"size","reclaim_scope":"whole",
"candidates":[{"path":"/r/b","signature":"s1","size_bytes":10},{"path":"/r/a","pattern":"**/a","size_bytes":5}]}`

func TestIdentifyPlanStableAcrossFormatting(t *testing.T) {
	a, err := IdentifyPlan([]byte(planA))
	if err != nil {
		t.Fatal(err)
	}
	reordered := `{ "candidates" : [{"size_bytes":10,"signature":"s1","path":"/r/b"},{"pattern":"**/a","path":"/r/a","size_bytes":5}],
	"reclaim_scope":"whole","sort_mode":"size","min_size":0,"analysis_root":"/r","version":1,"$schema":"x" }`
	b, err := IdentifyPlan([]byte(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("member order/whitespace changed identity: %+v vs %+v", a, b)
	}
	if !strings.HasPrefix(a.PlanDigest, "sha256:") || a.Candidates != 2 || a.AnalysisRoot != "/r" {
		t.Fatalf("identity %+v", a)
	}
}

func TestIdentifyPlanBindsEveryField(t *testing.T) {
	base, _ := IdentifyPlan([]byte(planA))
	for name, mut := range map[string]string{
		"size":           strings.Replace(planA, `"size_bytes":10`, `"size_bytes":11`, 1),
		"min_size":       strings.Replace(planA, `"min_size":0`, `"min_size":1`, 1),
		"root":           strings.Replace(planA, `"analysis_root":"/r"`, `"analysis_root":"/q"`, 1),
		"extra member":   strings.Replace(planA, `"version":1`, `"version":1,"x":null`, 1),
		"candidate swap": strings.Replace(planA, `"signature":"s1"`, `"signature":"s2"`, 1),
	} {
		got, err := IdentifyPlan([]byte(mut))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.PlanDigest == base.PlanDigest {
			t.Errorf("%s: plan digest unchanged", name)
		}
	}
	// The candidate set ignores sizes but not identity.
	sz, _ := IdentifyPlan([]byte(strings.Replace(planA, `"size_bytes":10`, `"size_bytes":11`, 1)))
	if sz.CandidateSetDigest != base.CandidateSetDigest {
		t.Error("candidate set digest changed with size")
	}
	sw, _ := IdentifyPlan([]byte(strings.Replace(planA, `"signature":"s1"`, `"signature":"s2"`, 1)))
	if sw.CandidateSetDigest == base.CandidateSetDigest {
		t.Error("candidate set digest unchanged with signature swap")
	}
	// Candidate order does not matter for the set, but does for the plan.
	rev := `{"$schema":"x","version":1,"analysis_root":"/r","min_size":0,"sort_mode":"size","reclaim_scope":"whole",
"candidates":[{"path":"/r/a","pattern":"**/a","size_bytes":5},{"path":"/r/b","signature":"s1","size_bytes":10}]}`
	r, _ := IdentifyPlan([]byte(rev))
	if r.CandidateSetDigest != base.CandidateSetDigest || r.PlanDigest == base.PlanDigest {
		t.Errorf("order: set %v plan %v", r.CandidateSetDigest == base.CandidateSetDigest, r.PlanDigest == base.PlanDigest)
	}
}

func TestIdentifyPlanDomainsDiffer(t *testing.T) {
	if DomainPlan == DomainCandidateSet {
		t.Fatal("domains must differ")
	}
}

func TestIdentifyPlanRejects(t *testing.T) {
	for name, in := range map[string]string{
		"duplicate member": `{"analysis_root":"/r","analysis_root":"/q","candidates":[]}`,
		"no root":          `{"candidates":[]}`,
		"no candidates":    `{"analysis_root":"/r"}`,
		"dup path":         `{"analysis_root":"/r","candidates":[{"path":"/r/a","pattern":"p"},{"path":"/r/a","pattern":"p"}]}`,
		"neither kind":     `{"analysis_root":"/r","candidates":[{"path":"/r/a"}]}`,
		"fraction":         `{"analysis_root":"/r","candidates":[],"min_size":1.5}`,
		"not object":       `[]`,
	} {
		if _, err := IdentifyPlan([]byte(in)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A space or profile handoff carries a signature's display pattern next to
// the signature; the set entry is the signature, as the planner uses it.
func TestIdentifyPlanSignatureWithDisplayPattern(t *testing.T) {
	both, err := IdentifyPlan([]byte(`{"analysis_root":"/r","candidates":[{"path":"/r/a","signature":"s","pattern":"**/a"}]}`))
	if err != nil {
		t.Fatalf("handoff candidate refused: %v", err)
	}
	sigOnly, _ := IdentifyPlan([]byte(`{"analysis_root":"/r","candidates":[{"path":"/r/a","signature":"s"}]}`))
	if both.CandidateSetDigest != sigOnly.CandidateSetDigest || both.PlanDigest == sigOnly.PlanDigest {
		t.Errorf("set equal %v, plan differs %v", both.CandidateSetDigest == sigOnly.CandidateSetDigest, both.PlanDigest != sigOnly.PlanDigest)
	}
}
