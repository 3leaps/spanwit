package reportmodel

import (
	"fmt"
	"time"

	"github.com/3leaps/spanwit/internal/envelope"
)

const (
	SchemaV2ID              = "https://schemas.3leaps.dev/spanwit/space-report/v2.json"
	CaptureModeFull         = "full"
	CaptureModeCapacityOnly = "capacity_only"
)

type CapacityOnlyReport struct {
	Schema             string     `json:"$schema"`
	Version            int        `json:"version"`
	GeneratedAt        string     `json:"generated_at"`
	Root               string     `json:"root"`
	CaptureMode        string     `json:"capture_mode"`
	Pressure           Pressure   `json:"pressure"`
	AnalysisPressure   *Pressure  `json:"analysis_pressure,omitempty"`
	CapacityAccounting Accounting `json:"capacity_accounting"`
	// MutationContract is the effective invocation no-mutation assertion.
	MutationContract string `json:"mutation_contract,omitempty"`
	// ContainerRuntime reports container-engine storage headroom (opt-in).
	ContainerRuntime *ContainerRuntime `json:"container_runtime,omitempty"`
	// Completion is partial iff any capacity plane reports a coverage gap.
	Completion *Completion          `json:"completion,omitempty"`
	Tool       *envelope.Tool       `json:"tool,omitempty"`
	Disclosure *envelope.Disclosure `json:"disclosure,omitempty"`
}

func ValidateCapacityCarrier(generatedAt string, pressure Pressure, accounting Accounting) error {
	captured, err := time.Parse(time.RFC3339, accounting.Capture.CapturedAt)
	if err != nil {
		return fmt.Errorf("capacity capture timestamp: %w", err)
	}
	generated, err := time.Parse(time.RFC3339, generatedAt)
	if err != nil {
		return fmt.Errorf("space report generated_at: %w", err)
	}
	if !generated.Equal(captured) {
		return fmt.Errorf("generated_at must equal capacity capture captured_at")
	}
	return validateSameObservation(
		pressure,
		accounting.Target,
		accounting.Planes.Filesystem,
	)
}

func validateSameObservation(
	pressure Pressure,
	target Target,
	plane FilesystemPlane,
) error {
	if plane.PressureRelation != "same_observation" {
		return fmt.Errorf("capacity filesystem pressure_relation must be same_observation")
	}
	if target.Path != pressure.Path || target.Mount != pressure.Mount || target.VolumeID != pressure.VolumeID {
		return fmt.Errorf("capacity target identity does not match carrier pressure")
	}
	checks := []struct {
		name  string
		claim ByteClaim
		want  int64
	}{
		{"total", plane.Total, pressure.TotalBytes},
		{"used", plane.Used, pressure.UsedBytes},
		{"available", plane.Available, pressure.AvailBytes},
	}
	for _, check := range checks {
		if check.claim.Status != StatusMeasured || check.claim.Bytes == nil || *check.claim.Bytes != check.want {
			return fmt.Errorf("capacity filesystem %s does not match carrier pressure", check.name)
		}
	}
	return nil
}
