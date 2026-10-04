package space

import (
	"context"
	"fmt"
	"time"

	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/engine"
)

const (
	SchemaV2ID              = "https://schemas.3leaps.dev/spanwit/space-report/v2.json"
	CaptureModeFull         = "full"
	CaptureModeCapacityOnly = "capacity_only"
)

type CapacityOnlyReport struct {
	Schema             string              `json:"$schema"`
	Version            int                 `json:"version"`
	GeneratedAt        string              `json:"generated_at"`
	Root               string              `json:"root"`
	CaptureMode        string              `json:"capture_mode"`
	Pressure           Pressure            `json:"pressure"`
	AnalysisPressure   *Pressure           `json:"analysis_pressure,omitempty"`
	CapacityAccounting capacity.Accounting `json:"capacity_accounting"`
	// MutationContract is the effective invocation no-mutation assertion.
	MutationContract string `json:"mutation_contract,omitempty"`
}

type CapacityOptions struct {
	Path             string
	Collector        capacity.Collector
	Tool             capacity.ToolIdentity
	HeldOpen         bool
	Disclosure       string
	SessionBoundary  string
	MutationContract string
}

func AnalyzeCapacityOnly(ctx context.Context, opts CapacityOptions) (CapacityOnlyReport, error) {
	path := opts.Path
	if path == "" {
		path = "."
	}
	root, err := engine.CleanConfiguredPath(path)
	if err != nil {
		return CapacityOnlyReport{}, err
	}
	primaryPath := primaryWritePath(root)
	result, err := opts.Collector.Collect(ctx, capacity.Request{
		TargetPath:      primaryPath,
		AnalysisPath:    root,
		Tool:            opts.Tool,
		HeldOpen:        opts.HeldOpen,
		Disclosure:      opts.Disclosure,
		SessionBoundary: opts.SessionBoundary,
	})
	if err != nil {
		return CapacityOnlyReport{}, err
	}
	pressure := pressureFromSample(result.Filesystem)
	pressure.Role = PressureRolePrimaryWrite

	var analysisPressure *Pressure
	if result.AnalysisFilesystem != nil {
		candidate := pressureFromSample(*result.AnalysisFilesystem)
		if !sameVolume(pressure, candidate) {
			candidate.Role = PressureRoleAnalysisRoot
			analysisPressure = &candidate
		}
	}

	mutationContract, err := normalizeMutationContract(opts.MutationContract)
	if err != nil {
		return CapacityOnlyReport{}, err
	}
	return CapacityOnlyReport{
		Schema:             SchemaV2ID,
		Version:            2,
		GeneratedAt:        result.Accounting.Capture.CapturedAt,
		Root:               root,
		CaptureMode:        CaptureModeCapacityOnly,
		Pressure:           pressure,
		AnalysisPressure:   analysisPressure,
		CapacityAccounting: result.Accounting,
		MutationContract:   mutationContract,
	}, nil
}

func ValidateCapacityInvariants(report CapacityOnlyReport) error {
	if report.Schema != SchemaV2ID || report.Version != 2 {
		return fmt.Errorf("space report v2 schema/version identity mismatch")
	}
	if report.CaptureMode != CaptureModeCapacityOnly {
		return fmt.Errorf("capacity-only report has capture_mode %q", report.CaptureMode)
	}
	return validateCapacityCarrier(report.GeneratedAt, report.Pressure, report.CapacityAccounting)
}

func validateCapacityCarrier(generatedAt string, pressure Pressure, accounting capacity.Accounting) error {
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
	target capacity.Target,
	plane capacity.FilesystemPlane,
) error {
	if plane.PressureRelation != "same_observation" {
		return fmt.Errorf("capacity filesystem pressure_relation must be same_observation")
	}
	if target.Path != pressure.Path || target.Mount != pressure.Mount || target.VolumeID != pressure.VolumeID {
		return fmt.Errorf("capacity target identity does not match carrier pressure")
	}
	checks := []struct {
		name  string
		claim capacity.ByteClaim
		want  int64
	}{
		{"total", plane.Total, pressure.TotalBytes},
		{"used", plane.Used, pressure.UsedBytes},
		{"available", plane.Available, pressure.AvailBytes},
	}
	for _, check := range checks {
		if check.claim.Status != capacity.StatusMeasured || check.claim.Bytes == nil || *check.claim.Bytes != check.want {
			return fmt.Errorf("capacity filesystem %s does not match carrier pressure", check.name)
		}
	}
	return nil
}
