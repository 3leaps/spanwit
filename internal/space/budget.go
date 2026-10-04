package space

import "fmt"

// WorkBudget is the pressure-sensitive discovery budget applied under crisis.
type WorkBudget struct {
	// DeepMaxDepth is the effective max depth for analysis-root walks (-1 = unlimited).
	DeepMaxDepth int
	// SkipUnknown omits the unknown-directory section.
	SkipUnknown bool
	// Notes are always disclosed when any crisis reduction applies.
	Notes []string
}

// ApplyCriticalWorkBudget returns the discovery budget for a pressure level and
// requested max_depth. Pure function for deterministic tests.
//
// Under critical pressure:
//   - unknown is always skipped (always noted)
//   - max_depth is capped at CriticalDeepMaxDepth when requested is unlimited or higher
//     (noted when the cap binds)
func ApplyCriticalWorkBudget(pressureLevel string, requestedMaxDepth int) WorkBudget {
	if pressureLevel != PressureCritical {
		return WorkBudget{DeepMaxDepth: requestedMaxDepth}
	}
	b := WorkBudget{
		SkipUnknown:  true,
		DeepMaxDepth: requestedMaxDepth,
		Notes: []string{
			"critical pressure work budget: unknown section omitted; re-run when free space allows full triage",
		},
	}
	if requestedMaxDepth < 0 || requestedMaxDepth > CriticalDeepMaxDepth {
		b.DeepMaxDepth = CriticalDeepMaxDepth
		b.Notes = append(b.Notes, fmt.Sprintf(
			"critical pressure work budget: deep discovery max_depth capped at %d (requested %s)",
			CriticalDeepMaxDepth, formatRequestedDepth(requestedMaxDepth)))
	}
	return b
}

func formatRequestedDepth(d int) string {
	if d < 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", d)
}
