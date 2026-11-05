package engine

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/3leaps/spanwit/internal/config"
)

// HandoffAgeFloor is the minimum age every exact-plan candidate must meet at
// revalidation (prune --from-space-report / --from-exact-plan), raised by any
// carried, signature, temp or requested floor and never lowered by the plan
// file. A plan carrying effective_min_age "none" still gets this floor, so a
// build that starts writing between the scan and execute is withheld. The
// only way to remove it is RequestedMinAgeNone on the prune invocation.
const HandoffAgeFloor = "1h"

// RequestedMinAgeNone is the operator opt-out that removes only the handoff
// floor for one invocation. Carried, signature and temp floors, and drift
// withholding, still apply.
const RequestedMinAgeNone = "none"

// Withheld reasons added by exact-plan drift. A drift reason is the primary
// withheld_reason only when no other gate withheld the row; the drift facts
// are always reported in CandidateDrift.
const (
	// WithheldReasonActivityAfterPlan: activity newer than the reviewed
	// plan's last_activity_at was observed beneath the candidate.
	WithheldReasonActivityAfterPlan = "activity_after_plan"
	// WithheldReasonSizeDrift: the complete remeasured size differs from the
	// complete planned size (growth or shrink, zero tolerance).
	WithheldReasonSizeDrift = "size_drift"
)

// Drift comparison outcomes.
const (
	DriftChanged   = "changed"
	DriftUnchanged = "unchanged"
	// DriftUnknown: the planned or remeasured side is incomplete, missing or
	// unparseable, so no comparison is claimed in either direction.
	DriftUnknown = "unknown"
)

// ExactBaseline is the planned observation carried by an exact-plan row. It
// is what the operator reviewed; revalidation compares against it. It is not
// authenticated: a forged baseline can suppress drift withholding, but it
// cannot lower any age floor.
type ExactBaseline struct {
	SizeBytes      int64
	SizeIncomplete bool
	// LastActivityAt is the carrier text verbatim (RFC3339 or RFC3339Nano).
	// Its precision decides the comparison precision.
	LastActivityAt     string
	ActivityBasis      string
	ActivityIncomplete bool
}

// CandidateDrift is the per-candidate comparison of remeasured state against
// the carried baseline. It is set on every exact-plan candidate, including
// rows withheld for another reason.
type CandidateDrift struct {
	Size string
	// SizeDeltaBytes is remeasured minus planned bytes, set only when both
	// sides are complete (never a fabricated delta).
	SizeDeltaBytes *int64
	Activity       string
}

// Freshness records the execute-time age policy and drift totals for an
// exact-plan revalidation. It is present on every exact-plan prune output,
// including zero-candidate replay.
type Freshness struct {
	HandoffFloor        string
	HandoffFloorApplied bool
	// RequestedMinAge is the --min-age value as given ("" when not given).
	RequestedMinAge string
	// Count of candidates whose requested floor was below an already
	// higher floor (kept: raise-only).
	RequestedBelowFloor int
	SizeDrift           int
	ActivityAfterPlan   int
	DriftUnknown        int
}

// NewFreshness returns the policy block for a request with zero drift counts.
func NewFreshness(requested string) *Freshness {
	return &Freshness{
		HandoffFloor:        HandoffAgeFloor,
		HandoffFloorApplied: requested != RequestedMinAgeNone,
		RequestedMinAge:     requested,
	}
}

// Merge adds another revalidation's counts (temp plans) into f.
func (f *Freshness) Merge(o *Freshness) {
	if f == nil || o == nil {
		return
	}
	f.RequestedBelowFloor += o.RequestedBelowFloor
	f.SizeDrift += o.SizeDrift
	f.ActivityAfterPlan += o.ActivityAfterPlan
	f.DriftUnknown += o.DriftUnknown
}

var requestedMinAgeRe = regexp.MustCompile(`^([1-9][0-9]*)([smhdw])$`)

// ValidateRequestedMinAge checks a prune --min-age value. It accepts "none"
// or a positive whole duration with unit s|m|h|d|w. Zero, negative,
// fractional, unit-less and overflowing values are rejected, so nothing but
// the named opt-out can remove the handoff floor.
func ValidateRequestedMinAge(s string) error {
	if s == RequestedMinAgeNone {
		return nil
	}
	m := requestedMinAgeRe.FindStringSubmatch(s)
	if m == nil {
		return fmt.Errorf("invalid --min-age %q (want none or a positive whole duration like 90m, 48h, 7d, 2w)", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return fmt.Errorf("invalid --min-age %q: %w", s, err)
	}
	unit := map[string]time.Duration{
		"s": time.Second, "m": time.Minute, "h": time.Hour,
		"d": 24 * time.Hour, "w": 7 * 24 * time.Hour,
	}[m[2]]
	if n > math.MaxInt64/int64(unit) {
		return fmt.Errorf("invalid --min-age %q: duration too large", s)
	}
	d, err := config.ParseAge(s)
	if err != nil || d <= 0 {
		return fmt.Errorf("invalid --min-age %q", s)
	}
	return nil
}

// minAgeBelow reports whether requested is shorter than the effective floor
// already in force (so raise-only kept the higher floor).
func minAgeBelow(requested, effective string) bool {
	r, err := config.ParseAge(requested)
	if err != nil {
		return false
	}
	e, err := config.ParseAge(displayFilterRaw(effective))
	if err != nil {
		return false
	}
	return r < e
}

// compareDrift compares a sized candidate against its carried baseline.
func compareDrift(c PruneCandidate, b *ExactBaseline) CandidateDrift {
	d := CandidateDrift{Size: DriftUnknown, Activity: DriftUnknown}
	if b == nil {
		return d
	}
	if !b.SizeIncomplete && !c.SizeIncomplete {
		delta := c.Size - b.SizeBytes
		d.SizeDeltaBytes = &delta
		if delta != 0 {
			d.Size = DriftChanged
		} else {
			d.Size = DriftUnchanged
		}
	}
	d.Activity = compareActivity(c, b)
	return d
}

// compareActivity compares at the precision the carrier retained. Legacy
// carriers write whole-second RFC3339, so the remeasured mtime is truncated
// to the second before comparing; a fractional baseline compares exactly. A
// write in the same second as a whole-second baseline cannot be detected.
func compareActivity(c PruneCandidate, b *ExactBaseline) string {
	if b.ActivityIncomplete || b.ActivityBasis != ActivityBasisDescendantMtime || strings.TrimSpace(b.LastActivityAt) == "" {
		return DriftUnknown
	}
	if c.ActivityIncomplete || c.ActivityBasis != ActivityBasisDescendantMtime || c.LastActivityAt.IsZero() {
		return DriftUnknown
	}
	base, err := time.Parse(time.RFC3339Nano, b.LastActivityAt)
	if err != nil {
		return DriftUnknown
	}
	measured := c.LastActivityAt
	if !strings.Contains(b.LastActivityAt, ".") {
		measured = measured.Truncate(time.Second)
	}
	if measured.After(base) {
		return DriftChanged
	}
	return DriftUnchanged
}

// applyDrift sets Drift on every candidate, withholds drifted rows that no
// other gate withheld (activity_after_plan before size_drift), and counts
// the facts into f.
func applyDrift(candidates []PruneCandidate, baselines map[string]*ExactBaseline, f *Freshness) {
	for i := range candidates {
		c := &candidates[i]
		d := compareDrift(*c, baselines[candidatePathKey(c.Path)])
		c.Drift = &d
		if d.Activity == DriftChanged {
			f.ActivityAfterPlan++
		}
		if d.Size == DriftChanged {
			f.SizeDrift++
		}
		if d.Activity == DriftUnknown || d.Size == DriftUnknown {
			f.DriftUnknown++
		}
		if c.State == CandidateStateWithheld {
			continue
		}
		switch {
		case d.Activity == DriftChanged:
			c.State = CandidateStateWithheld
			c.WithheldReason = WithheldReasonActivityAfterPlan
		case d.Size == DriftChanged:
			c.State = CandidateStateWithheld
			c.WithheldReason = WithheldReasonSizeDrift
		}
	}
}
