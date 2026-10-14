package reportmodel

const (
	TempPlaneSizeMeasured = "measured"
	TempPlaneSizePartial  = "partial"
)

// TempPlaneCoverage is emitted only under warn/critical primary pressure.
type TempPlaneCoverage struct {
	// Trigger is the primary pressure level that caused the probe.
	Trigger string      `json:"trigger"`
	Planes  []TempPlane `json:"planes"`
	// Residue aggregates the planes' direct children by fixed family label.
	// It carries counts and bytes only, never a child name (see tempfamilies.go).
	Residue *TempResidueSummary `json:"residue,omitempty"`
}

// TempPlane discloses one canonical temp root and a bounded size. It never
// names children: no entries, session ids, or snapshot names. Per-family
// aggregates of the children live in TempPlaneCoverage.Residue, under the
// fixed-vocabulary invariant in tempfamilies.go.
type TempPlane struct {
	Root string `json:"root"`
	Kind string `json:"kind"`
	// SizeStatus is measured (exact apparent bytes), partial (SizeBytes is a
	// lower bound), or unavailable (no bytes reported).
	SizeStatus string `json:"size_status"`
	SizeBytes  *int64 `json:"size_bytes,omitempty"`
	// SizeBound is derived from SizeStatus on marshal (absent when unavailable).
	SizeBound string `json:"size_bound,omitempty"`
	SizeBasis string `json:"size_basis"`
	FollowUp  string `json:"follow_up"`
}

// TempRoot reports one candidate temp root for space --include-temp: whether
// it was admitted as a verified walk root, and if not, why. Roots only; never
// child names.
type TempRoot struct {
	Root     string `json:"root"`
	Admitted bool   `json:"admitted"`
	Reason   string `json:"reason,omitempty"`
}
