package space

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/3leaps/spanwit/internal/engine"
)

// ValidateStandaloneExactPlan fail-closes a closed-set exact plan document
// (recipes[].exact_plan or prune --from-exact-plan FILE). Same identity and
// candidate invariants as report-carried exact plans, without requiring a
// paired prune_handoff listing.
func ValidateStandaloneExactPlan(ep *ExactPlan) error {
	if ep == nil {
		return fmt.Errorf("exact plan is nil")
	}
	if ep.Schema != ExactPlanSchemaID {
		return fmt.Errorf("exact_plan.$schema must be %q (got %q)", ExactPlanSchemaID, ep.Schema)
	}
	if ep.Version != 1 {
		return fmt.Errorf("exact_plan.version must be 1 (got %d)", ep.Version)
	}
	if strings.TrimSpace(ep.AnalysisRoot) == "" {
		return fmt.Errorf("exact_plan.analysis_root is required")
	}
	if strings.TrimSpace(ep.MinSize) == "" {
		return fmt.Errorf("exact_plan.min_size is required (use %q when none)", engine.FilterDisplayNone)
	}
	if err := requireSortMode("exact_plan", ep.SortMode); err != nil {
		return err
	}
	if err := requireReclaimScope("exact_plan", ep.ReclaimScope); err != nil {
		return err
	}
	if len(ep.Candidates) == 0 {
		return fmt.Errorf("exact_plan.candidates must be non-empty")
	}
	seen := map[string]bool{}
	for _, ec := range ep.Candidates {
		if ec.Path == "" {
			return fmt.Errorf("exact_plan candidate missing path")
		}
		if seen[ec.Path] {
			return fmt.Errorf("exact_plan duplicate candidate path %s", ec.Path)
		}
		seen[ec.Path] = true
		if ec.Signature == "" && ec.Pattern == "" {
			return fmt.Errorf("exact_plan candidate %s missing signature and pattern", ec.Path)
		}
		if err := requireFilterField(ec.Path, "effective_min_size", ec.EffectiveMinSize); err != nil {
			return err
		}
		if err := requireFilterField(ec.Path, "effective_min_age", ec.EffectiveMinAge); err != nil {
			return err
		}
		if err := requireFilterField(ec.Path, "effective_max_age", ec.EffectiveMaxAge); err != nil {
			return err
		}
		if err := validateExactCandidateScope(ec, ep.ReclaimScope); err != nil {
			return err
		}
	}
	return nil
}

// ParseAndValidateExactPlanJSON decodes a standalone exact_plan document with
// unknown-field rejection, then applies ValidateStandaloneExactPlan.
func ParseAndValidateExactPlanJSON(raw []byte) (ExactPlan, error) {
	if len(raw) == 0 {
		return ExactPlan{}, fmt.Errorf("exact plan is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var ep ExactPlan
	if err := dec.Decode(&ep); err != nil {
		return ExactPlan{}, fmt.Errorf("parse exact plan: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return ExactPlan{}, fmt.Errorf("parse exact plan: trailing data after object")
		}
		return ExactPlan{}, fmt.Errorf("parse exact plan: %w", err)
	}
	if err := ValidateStandaloneExactPlan(&ep); err != nil {
		return ExactPlan{}, err
	}
	return ep, nil
}
