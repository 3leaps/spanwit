package space

import (
	"context"
	"fmt"

	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/envelope"
)

// CapacityGapCount counts coverage gaps across every capacity plane: the
// warnings a capacity-only capture carries.
func CapacityGapCount(p capacity.Planes) int {
	return len(p.Filesystem.Coverage.Gaps) + len(p.Container.Coverage.Gaps) +
		len(p.Volumes.Coverage.Gaps) + len(p.Snapshots.Coverage.Gaps) +
		len(p.SystemManaged.Coverage.Gaps) + len(p.HeldOpen.Coverage.Gaps)
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
		Completion:         envelope.NewCompletion(CapacityGapCount(result.Accounting.Planes)),
	}, nil
}

func ValidateCapacityInvariants(report CapacityOnlyReport) error {
	if report.Schema != SchemaV2ID || report.Version != 2 {
		return fmt.Errorf("space report v2 schema/version identity mismatch")
	}
	if report.CaptureMode != CaptureModeCapacityOnly {
		return fmt.Errorf("capacity-only report has capture_mode %q", report.CaptureMode)
	}
	return ValidateCapacityCarrier(report.GeneratedAt, report.Pressure, report.CapacityAccounting)
}
