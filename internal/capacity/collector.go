package capacity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	DefaultDeadline         = 5 * time.Second
	DefaultHeldOpenDeadline = 15 * time.Second
	DefaultCommandOutput    = 4 << 20
	DefaultManagedObjects   = 1_000_000
)

type FilesystemSampler func(context.Context, string) (FilesystemSample, error)
type ManagedRootMeasurer func(context.Context, string, int) ManagedMeasurement
type PlatformVersionFunc func(context.Context) string
type BootMetadataFunc func(context.Context) BootMetadata

type Collector struct {
	Now                 func() time.Time
	Platform            string
	Architecture        string
	Deadline            time.Duration
	HeldOpenDeadline    time.Duration
	FilesystemSampler   FilesystemSampler
	DarwinRunner        CommandRunner
	HeldOpenRunner      CommandRunner
	ManagedRootMeasurer ManagedRootMeasurer
	ManagedRoots        []ManagedRoot
	ManagedObjectLimit  int
	PlatformVersion     PlatformVersionFunc
	BootMetadata        BootMetadataFunc
}

type Request struct {
	TargetPath      string
	AnalysisPath    string
	Tool            ToolIdentity
	SessionBoundary string
	HeldOpen        bool
	Disclosure      string
}

type Result struct {
	Accounting         Accounting
	Filesystem         FilesystemSample
	AnalysisFilesystem *FilesystemSample
}

func (c Collector) SampleFilesystem(ctx context.Context, path string) (FilesystemSample, error) {
	if err := ctx.Err(); err != nil {
		return FilesystemSample{}, err
	}
	sampler := c.FilesystemSampler
	if sampler == nil {
		sampler = SampleFilesystemContext
	}
	sample, err := sampler(ctx, path)
	if err != nil {
		return FilesystemSample{}, err
	}
	if err := ctx.Err(); err != nil {
		return FilesystemSample{}, err
	}
	return sample, nil
}

func (c Collector) Collect(ctx context.Context, req Request) (Result, error) {
	if req.TargetPath == "" {
		return Result{}, fmt.Errorf("capacity target path is required")
	}
	if req.Tool.Name == "" {
		return Result{}, fmt.Errorf("capacity tool name is required")
	}
	if req.Tool.Version == "" {
		req.Tool.Version = "dev"
	}
	if err := validateSessionBoundary(req.SessionBoundary); err != nil {
		return Result{}, err
	}
	if err := validateHeldOpenDisclosure(req.Disclosure); err != nil {
		return Result{}, err
	}
	if !req.HeldOpen && req.Disclosure != "" && req.Disclosure != "none" {
		return Result{}, fmt.Errorf("held-open disclosure requires held-open collection")
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	capturedAt := now().UTC()

	deadline := c.Deadline
	if deadline <= 0 {
		deadline = DefaultDeadline
	}
	collectCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	if err := collectCtx.Err(); err != nil {
		return Result{}, fmt.Errorf("mandatory filesystem observation: %w", err)
	}
	sample, err := c.SampleFilesystem(collectCtx, req.TargetPath)
	if err != nil {
		return Result{}, fmt.Errorf("mandatory filesystem observation: %w", err)
	}
	var analysisSample *FilesystemSample
	var analysisGap *CoverageGap
	if req.AnalysisPath != "" && req.AnalysisPath != req.TargetPath {
		observed, sampleErr := c.SampleFilesystem(collectCtx, req.AnalysisPath)
		if sampleErr != nil {
			gap := gapForFilesystemError(sampleErr, "analysis_pressure")
			analysisGap = &gap
		} else {
			analysisSample = &observed
		}
	}

	platform := c.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	architecture := c.Architecture
	if architecture == "" {
		architecture = runtime.GOARCH
	}

	accounting := Accounting{
		Capture: Capture{
			CapturedAt: capturedAt.Format(time.RFC3339),
			Tool:       req.Tool,
			OS: OSIdentity{
				GOOS:   platform,
				GOARCH: architecture,
			},
			SessionBoundary: req.SessionBoundary,
		},
		Target: Target{
			Path:     sample.Path,
			Mount:    sample.Mount,
			FSType:   sample.FSType,
			VolumeID: sample.VolumeID,
			Platform: platform,
		},
		Planes: Planes{
			Filesystem: filesystemPlane(sample),
			Container:  unsupportedContainer(),
			Volumes:    unsupportedVolumes(),
			Snapshots:  unsupportedSnapshots(),
			SystemManaged: SystemManagedPlane{
				CollectionStatus: StatusUnsupported,
				Coverage:         emptyCoverage(),
				Policy:           ManagedPolicy,
			},
			HeldOpen: HeldOpenPlane{
				CollectionStatus: StatusNotRequested,
				Coverage:         emptyCoverage(),
			},
		},
	}
	if c.PlatformVersion != nil && collectCtx.Err() == nil {
		accounting.Capture.OS.PlatformVersion = strings.TrimSpace(c.PlatformVersion(collectCtx))
	}
	bootMetadata := c.BootMetadata
	if bootMetadata == nil && c.Platform == "" {
		bootMetadata = defaultBootMetadata
	}
	if bootMetadata != nil && collectCtx.Err() == nil {
		boot := bootMetadata(collectCtx)
		accounting.Capture.BootID = boot.ID
		if !boot.Time.IsZero() {
			accounting.Capture.BootTime = boot.Time.UTC().Format(time.RFC3339Nano)
		}
	}

	if platform == "darwin" {
		runner := c.DarwinRunner
		if runner == nil {
			runner = ExecRunner{MaxOutput: DefaultCommandOutput}
		}
		collectDarwin(collectCtx, runner, &sample, &accounting)
		accounting.Planes.Filesystem = filesystemPlane(sample)
		accounting.Target.Path = sample.Path
		accounting.Target.Mount = sample.Mount
		accounting.Target.FSType = sample.FSType
		accounting.Target.VolumeID = sample.VolumeID

		roots := c.ManagedRoots
		if roots == nil {
			roots = []ManagedRoot{{
				ID:          "apple_assets_v2",
				Path:        "/System/Library/AssetsV2",
				Remediation: "os_or_update_mechanism",
			}}
		}
		measurer := c.ManagedRootMeasurer
		if measurer == nil {
			measurer = MeasureManagedRoot
		}
		limit := c.ManagedObjectLimit
		if limit <= 0 {
			limit = DefaultManagedObjects
		}
		accounting.Planes.SystemManaged = collectManagedRoots(collectCtx, roots, limit, measurer)
	}

	if req.HeldOpen {
		if platform != "darwin" {
			accounting.Planes.HeldOpen = HeldOpenPlane{
				CollectionStatus: StatusUnsupported,
				Coverage:         emptyCoverage(),
			}
		} else {
			heldDeadline := c.HeldOpenDeadline
			if heldDeadline <= 0 {
				heldDeadline = DefaultHeldOpenDeadline
			}
			heldCtx, heldCancel := context.WithTimeout(ctx, heldDeadline)
			runner := c.HeldOpenRunner
			if runner == nil {
				runner = ExecRunner{
					MaxOutput:          defaultHeldOpenOutput,
					NoMatchExitCode:    1,
					PermissionWarnings: true,
				}
			}
			accounting.Planes.HeldOpen = collectHeldOpen(
				heldCtx,
				runner,
				req.Disclosure,
				req.AnalysisPath,
			)
			heldCancel()
		}
	}

	accounting.Narrative = buildNarrative(accounting.Planes)
	if analysisGap != nil {
		accounting.Narrative.Unavailable = append(
			accounting.Narrative.Unavailable,
			describeCoverageGap("analysis_pressure", *analysisGap),
		)
	}
	return Result{
		Accounting:         accounting,
		Filesystem:         sample,
		AnalysisFilesystem: analysisSample,
	}, nil
}

func validateSessionBoundary(value string) error {
	switch value {
	case "", "before-running", "before-quiesced", "after-reboot", "after-update":
		return nil
	default:
		return fmt.Errorf("unsupported session boundary %q", value)
	}
}

func gapForFilesystemError(err error, scope string) CoverageGap {
	code := "command_failed"
	detail := "filesystem observation failed"
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code = "timeout"
		detail = "filesystem observation did not complete within the capacity context budget"
	case errors.Is(err, os.ErrPermission):
		code = "permission_denied"
		detail = "filesystem observation was denied by current privileges"
	}
	return CoverageGap{Code: code, Scope: scope, Detail: detail}
}

func deadlineGap(scope, detail string) CoverageGap {
	return CoverageGap{
		Code:   "timeout",
		Scope:  scope,
		Detail: detail,
	}
}

func filesystemPlane(sample FilesystemSample) FilesystemPlane {
	return FilesystemPlane{
		CollectionStatus: StatusMeasured,
		Source:           "statfs",
		Coverage:         emptyCoverage(),
		PressureRelation: "same_observation",
		Total:            measuredClaim(sample.Total, "statfs_blocks", "exact"),
		Used:             measuredClaim(sample.Used, "statfs_used", "exact"),
		Available:        measuredClaim(sample.Available, "statfs_bavail", "exact"),
	}
}

func measuredClaim(bytes int64, basis, bound string) ByteClaim {
	value := bytes
	return ByteClaim{
		Status: StatusMeasured,
		Bytes:  &value,
		Human:  HumanBytes(bytes),
		Basis:  basis,
		Bound:  bound,
	}
}

func partialClaim(bytes int64, basis, bound string) ByteClaim {
	claim := measuredClaim(bytes, basis, bound)
	claim.Status = StatusPartial
	return claim
}

func unsupportedClaim(detail string) ByteClaim {
	return ByteClaim{Status: StatusUnsupported, Detail: detail}
}

func emptyCoverage() Coverage {
	return Coverage{Gaps: []CoverageGap{}}
}

func unsupportedContainer() ContainerPlane {
	return ContainerPlane{CollectionStatus: StatusUnsupported, Coverage: emptyCoverage()}
}

func unsupportedVolumes() VolumesPlane {
	return VolumesPlane{CollectionStatus: StatusUnsupported, Coverage: emptyCoverage()}
}

func unsupportedSnapshots() SnapshotsPlane {
	return SnapshotsPlane{CollectionStatus: StatusUnsupported, Coverage: emptyCoverage()}
}

func buildNarrative(planes Planes) *Narrative {
	known := make([]string, 0)
	unavailable := make([]string, 0)

	appendPlaneState(&known, &unavailable, "filesystem", planes.Filesystem.CollectionStatus, "")
	appendClaimStory(&known, &unavailable, "filesystem.total", &planes.Filesystem.Total)
	appendClaimStory(&known, &unavailable, "filesystem.used", &planes.Filesystem.Used)
	appendClaimStory(&known, &unavailable, "filesystem.available", &planes.Filesystem.Available)
	appendCoverageStory(&unavailable, "filesystem", planes.Filesystem.Coverage)

	appendPlaneState(&known, &unavailable, "container", planes.Container.CollectionStatus, planes.Container.ID)
	appendClaimStory(&known, &unavailable, "container.capacity", planes.Container.Capacity)
	appendClaimStory(&known, &unavailable, "container.free", planes.Container.Free)
	appendCoverageStory(&unavailable, "container", planes.Container.Coverage)

	appendPlaneState(
		&known,
		&unavailable,
		"volumes",
		planes.Volumes.CollectionStatus,
		fmt.Sprintf("%d entries", len(planes.Volumes.Entries)),
	)
	for _, volume := range planes.Volumes.Entries {
		appendClaimStory(
			&known,
			&unavailable,
			"volumes."+volume.ID+".capacity_in_use",
			volume.CapacityInUse,
		)
	}
	appendCoverageStory(&unavailable, "volumes", planes.Volumes.Coverage)

	snapshotDetail := ""
	if planes.Snapshots.Count != nil {
		snapshotDetail = fmt.Sprintf("%d entries", *planes.Snapshots.Count)
	}
	appendPlaneState(&known, &unavailable, "snapshots", planes.Snapshots.CollectionStatus, snapshotDetail)
	appendClaimStory(&known, &unavailable, "snapshots.bytes", planes.Snapshots.Bytes)
	for _, snapshot := range planes.Snapshots.Entries {
		appendClaimStory(&known, &unavailable, "snapshots."+snapshot.ID+".bytes", snapshot.Bytes)
	}
	appendCoverageStory(&unavailable, "snapshots", planes.Snapshots.Coverage)

	appendPlaneState(
		&known,
		&unavailable,
		"system_managed",
		planes.SystemManaged.CollectionStatus,
		fmt.Sprintf("%d diagnostic roots", len(planes.SystemManaged.Entries)),
	)
	for _, entry := range planes.SystemManaged.Entries {
		appendClaimStory(
			&known,
			&unavailable,
			"system_managed."+entry.ID+".allocated",
			entry.Allocated,
		)
	}
	appendCoverageStory(&unavailable, "system_managed", planes.SystemManaged.Coverage)

	if planes.HeldOpen.CollectionStatus != StatusNotRequested {
		appendPlaneState(&known, &unavailable, "held_open", planes.HeldOpen.CollectionStatus, "")
		appendClaimStory(&known, &unavailable, "held_open.logical_bytes", planes.HeldOpen.LogicalBytes)
		appendCoverageStory(&unavailable, "held_open", planes.HeldOpen.Coverage)
	}
	summary := "Capacity planes are independent; values are not reconciled or summed."
	if planes.HeldOpen.CollectionStatus == StatusNotRequested {
		summary += " Held-open collection is not_requested."
	} else {
		summary += " Held-open collection status is " + planes.HeldOpen.CollectionStatus + "."
	}
	return &Narrative{
		Summary:          summary,
		Known:            known,
		Unavailable:      unavailable,
		UnexplainedHints: []string{},
	}
}

func appendPlaneState(known, unavailable *[]string, name, status, detail string) {
	text := name + ": " + status
	if detail != "" {
		text += " (" + detail + ")"
	}
	switch status {
	case StatusMeasured, StatusPartial:
		*known = append(*known, text)
	case StatusUnavailable, StatusUnsupported:
		*unavailable = append(*unavailable, text)
	}
}

func appendClaimStory(known, unavailable *[]string, name string, claim *ByteClaim) {
	if claim == nil || claim.Status == "" {
		return
	}
	switch claim.Status {
	case StatusMeasured, StatusPartial:
		if claim.Bytes == nil {
			*unavailable = append(*unavailable, name+": "+claim.Status+" without a numeric observation")
			return
		}
		human := claim.Human
		if human == "" {
			human = HumanBytes(*claim.Bytes)
		}
		*known = append(*known, fmt.Sprintf(
			"%s: %s (%d bytes; status=%s; basis=%s; bound=%s)",
			name,
			human,
			*claim.Bytes,
			claim.Status,
			claim.Basis,
			claim.Bound,
		))
	case StatusUnavailable, StatusUnsupported:
		text := name + ": " + claim.Status
		if claim.Detail != "" {
			text += " (" + claim.Detail + ")"
		}
		*unavailable = append(*unavailable, text)
	}
}

func appendCoverageStory(unavailable *[]string, plane string, coverage Coverage) {
	for _, gap := range coverage.Gaps {
		*unavailable = append(*unavailable, describeCoverageGap(plane, gap))
	}
}

func describeCoverageGap(plane string, gap CoverageGap) string {
	parts := []string{
		plane + " coverage gap: " + gap.Code,
		"scope=" + gap.Scope,
	}
	if gap.Detail != "" {
		parts = append(parts, "detail="+gap.Detail)
	}
	if gap.OmittedObjects != nil {
		parts = append(parts, fmt.Sprintf("omitted_objects=%d", *gap.OmittedObjects))
	}
	if gap.OmittedHolders != nil {
		parts = append(parts, fmt.Sprintf("omitted_holders=%d", *gap.OmittedHolders))
	}
	return strings.Join(parts, "; ")
}
