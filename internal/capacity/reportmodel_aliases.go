package capacity

import "github.com/3leaps/spanwit/internal/reportmodel"

// Compatibility aliases: these declarations live in internal/reportmodel.

type (
	Accounting           = reportmodel.Accounting
	BootMetadata         = reportmodel.BootMetadata
	ByteClaim            = reportmodel.ByteClaim
	Capture              = reportmodel.Capture
	CompareCompatibility = reportmodel.CompareCompatibility
	CompareCoverage      = reportmodel.CompareCoverage
	CompareInput         = reportmodel.CompareInput
	CompareNarrative     = reportmodel.CompareNarrative
	CompareReport        = reportmodel.CompareReport
	CompareSide          = reportmodel.CompareSide
	CompatibilityCheck   = reportmodel.CompatibilityCheck
	ContainerPlane       = reportmodel.ContainerPlane
	Coverage             = reportmodel.Coverage
	CoverageGap          = reportmodel.CoverageGap
	FilesystemPlane      = reportmodel.FilesystemPlane
	FilesystemSample     = reportmodel.FilesystemSample
	HeldOpenGroup        = reportmodel.HeldOpenGroup
	HeldOpenHolder       = reportmodel.HeldOpenHolder
	HeldOpenObject       = reportmodel.HeldOpenObject
	HeldOpenPlane        = reportmodel.HeldOpenPlane
	ManagedRoot          = reportmodel.ManagedRoot
	NamedClaim           = reportmodel.NamedClaim
	Narrative            = reportmodel.Narrative
	OSIdentity           = reportmodel.OSIdentity
	PlaneDelta           = reportmodel.PlaneDelta
	Planes               = reportmodel.Planes
	RuntimeClaims        = reportmodel.RuntimeClaims
	SnapshotEntry        = reportmodel.SnapshotEntry
	SnapshotsPlane       = reportmodel.SnapshotsPlane
	SystemManagedEntry   = reportmodel.SystemManagedEntry
	SystemManagedPlane   = reportmodel.SystemManagedPlane
	Target               = reportmodel.Target
	ToolIdentity         = reportmodel.ToolIdentity
	VolumeEntry          = reportmodel.VolumeEntry
	VolumesPlane         = reportmodel.VolumesPlane
)

const (
	BoundExact                     = reportmodel.BoundExact
	BoundLower                     = reportmodel.BoundLower
	BoundUnknown                   = reportmodel.BoundUnknown
	BoundUpper                     = reportmodel.BoundUpper
	CheckBootID                    = reportmodel.CheckBootID
	CheckCaptureMode               = reportmodel.CheckCaptureMode
	CheckContainerID               = reportmodel.CheckContainerID
	CheckMeasurementSemantics      = reportmodel.CheckMeasurementSemantics
	CheckPlatform                  = reportmodel.CheckPlatform
	CheckReportVersion             = reportmodel.CheckReportVersion
	CheckVolumeID                  = reportmodel.CheckVolumeID
	CompareSchemaID                = reportmodel.CompareSchemaID
	CompatCompatible               = reportmodel.CompatCompatible
	CompatIncompatible             = reportmodel.CompatIncompatible
	CompatPartial                  = reportmodel.CompatPartial
	DeltaExact                     = reportmodel.DeltaExact
	DeltaIncomparable              = reportmodel.DeltaIncomparable
	DeltaIndeterminate             = reportmodel.DeltaIndeterminate
	DisclosureFull                 = reportmodel.DisclosureFull
	DisclosureNone                 = reportmodel.DisclosureNone
	GapCommandFailed               = reportmodel.GapCommandFailed
	GapCommandMissing              = reportmodel.GapCommandMissing
	GapParseFailed                 = reportmodel.GapParseFailed
	GapPermissionDenied            = reportmodel.GapPermissionDenied
	GapTimeout                     = reportmodel.GapTimeout
	GapTruncated                   = reportmodel.GapTruncated
	ManagedPolicy                  = reportmodel.ManagedPolicy
	PlaneContainer                 = reportmodel.PlaneContainer
	PlaneContainerRuntime          = reportmodel.PlaneContainerRuntime
	PlaneFilesystem                = reportmodel.PlaneFilesystem
	PlaneHeldOpen                  = reportmodel.PlaneHeldOpen
	PlaneSnapshots                 = reportmodel.PlaneSnapshots
	PlaneSystemManaged             = reportmodel.PlaneSystemManaged
	PlaneVolumes                   = reportmodel.PlaneVolumes
	PrivilegeElevated              = reportmodel.PrivilegeElevated
	PrivilegeUnprivileged          = reportmodel.PrivilegeUnprivileged
	RemediationNoneSupported       = reportmodel.RemediationNoneSupported
	RemediationOSOrUpdateMechanism = reportmodel.RemediationOSOrUpdateMechanism
	RemediationUnknown             = reportmodel.RemediationUnknown
	RootClassOther                 = reportmodel.RootClassOther
	RootClassSystem                = reportmodel.RootClassSystem
	RootClassSystemTemp            = reportmodel.RootClassSystemTemp
	RootClassTargetTree            = reportmodel.RootClassTargetTree
	RootClassUserHome              = reportmodel.RootClassUserHome
	StatusMeasured                 = reportmodel.StatusMeasured
	StatusNotRequested             = reportmodel.StatusNotRequested
	StatusPartial                  = reportmodel.StatusPartial
	StatusUnavailable              = reportmodel.StatusUnavailable
	StatusUnsupported              = reportmodel.StatusUnsupported
)

var (
	Compare    = reportmodel.Compare
	HumanBytes = reportmodel.HumanBytes
)
