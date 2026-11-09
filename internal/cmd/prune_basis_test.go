package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/engine"
)

// A report replays its primary and temp-root plans separately and sums them,
// so one basis is enforced across all of them, not per plan. A legacy carrier
// (no size_basis) is apparent, so it cannot sit beside an allocated plan.
func TestPruneFromSpaceReport_MixedBasesAcrossRootsRefused(t *testing.T) {
	e2eSkip(t)
	if !engine.AllocatedSizesSupported {
		t.Skip("reports from this build are apparent-only")
	}
	for _, tc := range []struct {
		name  string
		basis string
	}{
		{"explicit apparent temp plan", engine.SizeBasisApparent},
		{"legacy temp plan without a basis", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			analysis, temp := t.TempDir(), e2eTempRoot(t)
			e2eCargo(t, filepath.Join(analysis, "app"))
			e2eCargo(t, filepath.Join(temp, "checkout"))
			e2eAge(t, analysis)
			e2eAge(t, temp)
			path, r := e2eReport(t, analysis, temp)
			h := r.PruneHandoff
			if h == nil || h.ExactPlan == nil || len(h.ExactPlan.Candidates) == 0 || len(h.TempExactPlans) != 1 {
				t.Fatalf("fixture needs a primary and one temp plan: %+v", h)
			}
			if h.ExactPlan.Candidates[0].SizeBasis != engine.SizeBasisAllocated {
				t.Fatalf("primary basis=%q", h.ExactPlan.Candidates[0].SizeBasis)
			}
			for i := range h.TempExactPlans[0].Candidates {
				c := &h.TempExactPlans[0].Candidates[i]
				c.SizeBasis = tc.basis
				c.SizeBytes = *c.LogicalBytes
				if tc.basis == "" {
					c.LogicalBytes = nil
				}
			}
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			p, err := buildPrunePlanFromSpaceReport(context.Background(), path, "")
			if err == nil || !strings.Contains(err.Error(), "mixes size bases") {
				t.Fatalf("mixed bases across primary/temp plans accepted: err=%v summary=%s rows=%d", err, p.SizeBasis, len(p.Candidates))
			}
		})
	}
}

// An unedited report with a primary and a temp plan replays on one basis.
func TestPruneFromSpaceReport_PrimaryAndTempShareBasis(t *testing.T) {
	e2eSkip(t)
	analysis, temp := t.TempDir(), e2eTempRoot(t)
	e2eCargo(t, filepath.Join(analysis, "app"))
	e2eCargo(t, filepath.Join(temp, "checkout"))
	e2eAge(t, analysis)
	e2eAge(t, temp)
	path, _ := e2eReport(t, analysis, temp)
	p, err := buildPrunePlanFromSpaceReport(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.SizeBasis != engine.DefaultSizeBasis() || len(p.Candidates) < 2 {
		t.Fatalf("summary=%q rows=%d", p.SizeBasis, len(p.Candidates))
	}
	for _, c := range p.Candidates {
		if c.SizeBasis != p.SizeBasis {
			t.Fatalf("row %s on %s, plan on %s", c.Path, c.SizeBasis, p.SizeBasis)
		}
	}
}
