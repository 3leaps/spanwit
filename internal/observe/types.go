// Package observe implements observe+propose primitives: registered volume
// identity, pressure samples, hysteresis, growth/runway (or indeterminate),
// loss→reconcile, and proposal emission. First-ship posture is observation and
// proposals only — no delete, elevation, or config mutation.
package observe

import (
	"time"
)

// Coverage status for a sample or volume baseline.
const (
	CoverageComplete      = "complete"
	CoveragePartial       = "partial"
	CoverageDegraded      = "degraded"
	CoverageIncomparable  = "incomparable"
	CoverageUnavailable   = "unavailable"
	CoverageIndeterminate = "indeterminate"
)

// Pressure levels align with space diagnostics (ok|warn|critical).
const (
	LevelOK       = "ok"
	LevelWarn     = "warn"
	LevelCritical = "critical"
)

// SampleBasis identifies the measurement basis used for compare-safety.
const (
	BasisStatfs = "statfs_primary"
)

// Loss reasons that force degraded scope + reconciliatory full sample.
const (
	LossOverflow      = "overflow"
	LossSequenceGap   = "sequence_gap"
	LossWatcherError  = "watcher_error"
	LossDroppedWork   = "dropped_work"
	LossCorruptState  = "corrupt_state"
	LossIdentityShift = "identity_mismatch"
)

// VolumeID is a stable opaque filesystem identity (not a mount path).
type VolumeID string

// Sample is one time-stamped observation of a registered volume.
type Sample struct {
	CapturedAt   time.Time `json:"captured_at"`
	VolumeID     VolumeID  `json:"volume_id"`
	MountPath    string    `json:"mount_path,omitempty"` // alias at capture; not identity
	Basis        string    `json:"basis"`
	AvailBytes   int64     `json:"avail_bytes"`
	TotalBytes   int64     `json:"total_bytes"`
	UsedBytes    int64     `json:"used_bytes"`
	UsedPercent  float64   `json:"used_percent"`
	Level        string    `json:"level"`
	Coverage     string    `json:"coverage"`
	Seq          uint64    `json:"seq"`
	Source       string    `json:"source,omitempty"`
	SizeComplete bool      `json:"size_complete"` // false → lower-bound / incomplete → no rate
}

// RegisteredVolume is one watched volume under opaque identity.
type RegisteredVolume struct {
	ID           VolumeID `json:"id"`
	RegisterPath string   `json:"register_path"` // operator path used to (re)resolve
	Label        string   `json:"label,omitempty"`
}

// GrowthResult is rate/runway or an unavailable/indeterminate conclusion.
type GrowthResult struct {
	Status       string   `json:"status"` // measured|unavailable|indeterminate
	RateBps      *float64 `json:"rate_bps,omitempty"`
	RunwaySec    *float64 `json:"runway_sec,omitempty"`
	DeltaBytes   *int64   `json:"delta_bytes,omitempty"`
	ElapsedSec   *float64 `json:"elapsed_sec,omitempty"`
	Detail       string   `json:"detail,omitempty"`
	FromSeq      uint64   `json:"from_seq,omitempty"`
	ToSeq        uint64   `json:"to_seq,omitempty"`
	SameVolumeID VolumeID `json:"volume_id,omitempty"`
}

// Proposal is dry-run-safe guidance (recipe id or note). Never delete authority.
type Proposal struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"` // recipe|note
	Title    string   `json:"title"`
	Evidence []string `json:"evidence,omitempty"`
	RecipeID string   `json:"recipe_id,omitempty"`
}

// VolumeRuntime is per-volume observe state.
type VolumeRuntime struct {
	Volume            RegisteredVolume `json:"volume"`
	LastSuccess       *Sample          `json:"last_success,omitempty"`
	LastAttempt       *Sample          `json:"last_attempt,omitempty"`
	Level             string           `json:"level"`
	Coverage          string           `json:"coverage"`
	LastNotifiedLevel string           `json:"last_notified_level,omitempty"`
	LossReason        string           `json:"loss_reason,omitempty"`
	ReconcileNeeded   bool             `json:"reconcile_needed"`
	NextSeq           uint64           `json:"next_seq"`
	Foreground        bool             `json:"foreground"`
	ScanInFlight      bool             `json:"scan_in_flight"`
}

// Health is the bounded, path-safe status surface for operators/daemon.
type Health struct {
	Schema            string         `json:"$schema"`
	Version           int            `json:"version"`
	GeneratedAt       time.Time      `json:"generated_at"`
	Volumes           []VolumeHealth `json:"volumes"`
	Proposals         []Proposal     `json:"proposals,omitempty"`
	Known             []string       `json:"known,omitempty"`
	Unavailable       []string       `json:"unavailable,omitempty"`
	Unexplained       []string       `json:"unexplained,omitempty"`
	MutationContract  string         `json:"mutation_contract"` // open|read_only; never execute
	ForegroundActive  bool           `json:"foreground_active"`
	BacklogReconciles int            `json:"backlog_reconciles"`
}

// VolumeHealth is one volume's health row (no raw process lists).
type VolumeHealth struct {
	VolumeID          VolumeID      `json:"volume_id"`
	Label             string        `json:"label,omitempty"`
	Level             string        `json:"level"`
	Coverage          string        `json:"coverage"`
	LastSuccessAt     string        `json:"last_success_at,omitempty"`
	LastAttemptAt     string        `json:"last_attempt_at,omitempty"`
	LossReason        string        `json:"loss_reason,omitempty"`
	ReconcileNeeded   bool          `json:"reconcile_needed"`
	LastNotifiedLevel string        `json:"last_notified_level,omitempty"`
	Growth            *GrowthResult `json:"growth,omitempty"`
	Foreground        bool          `json:"foreground"`
}

// StateFileVersion is the persisted observe-state document version.
const StateFileVersion = 1

// HealthSchemaID is the JSON Schema $id for observe health reports.
const HealthSchemaID = "https://schemas.3leaps.dev/spanwit/observe-health/v1.json"

// StateSchemaID is the JSON Schema $id for persisted observe state.
const StateSchemaID = "https://schemas.3leaps.dev/spanwit/observe-state/v1.json"
