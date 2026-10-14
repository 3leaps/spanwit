package space

import "github.com/3leaps/spanwit/internal/reportmodel"

// Compatibility aliases: these declarations live in internal/reportmodel.

type (
	AppliedFilters     = reportmodel.AppliedFilters
	CapacityOnlyReport = reportmodel.CapacityOnlyReport
	Completion         = reportmodel.Completion
	ConfigSnippet      = reportmodel.ConfigSnippet
	ContainerRuntime   = reportmodel.ContainerRuntime
	EngineDetection    = reportmodel.EngineDetection
	Entry              = reportmodel.Entry
	ExactCandidate     = reportmodel.ExactCandidate
	ExactPlan          = reportmodel.ExactPlan
	GuestFilesystem    = reportmodel.GuestFilesystem
	InventoryFilters   = reportmodel.InventoryFilters
	PartialUpdate      = reportmodel.PartialUpdate
	Pressure           = reportmodel.Pressure
	PruneHandoff       = reportmodel.PruneHandoff
	Recipe             = reportmodel.Recipe
	RecipeStep         = reportmodel.RecipeStep
	Report             = reportmodel.Report
	RuntimeBucket      = reportmodel.RuntimeBucket
	RuntimeImageRef    = reportmodel.RuntimeImageRef
	RuntimeMeasure     = reportmodel.RuntimeMeasure
	RuntimeRecipeStep  = reportmodel.RuntimeRecipeStep
	SuggestedCommand   = reportmodel.SuggestedCommand
	TempFamily         = reportmodel.TempFamily
	TempPlane          = reportmodel.TempPlane
	TempPlaneCoverage  = reportmodel.TempPlaneCoverage
	TempResidueSummary = reportmodel.TempResidueSummary
	TempRoot           = reportmodel.TempRoot
	UnverifiedSection  = reportmodel.UnverifiedSection
	VerifiedSection    = reportmodel.VerifiedSection
)

const (
	ArchAMD64                    = reportmodel.ArchAMD64
	ArchARM64                    = reportmodel.ArchARM64
	ArchOther                    = reportmodel.ArchOther
	BoundExact                   = reportmodel.BoundExact
	BoundLower                   = reportmodel.BoundLower
	BoundUpper                   = reportmodel.BoundUpper
	BucketBuildCache             = reportmodel.BucketBuildCache
	BucketDanglingImages         = reportmodel.BucketDanglingImages
	BucketForeignPlatform        = reportmodel.BucketForeignPlatform
	BucketImages                 = reportmodel.BucketImages
	BucketSupersededTags         = reportmodel.BucketSupersededTags
	BucketVolumesUnreferenced    = reportmodel.BucketVolumesUnreferenced
	CaptureModeCapacityOnly      = reportmodel.CaptureModeCapacityOnly
	CaptureModeFull              = reportmodel.CaptureModeFull
	CompletionComplete           = reportmodel.CompletionComplete
	CompletionPartial            = reportmodel.CompletionPartial
	CostDataLossRisk             = reportmodel.CostDataLossRisk
	CostNone                     = reportmodel.CostNone
	CostRePull                   = reportmodel.CostRePull
	CostRebuild                  = reportmodel.CostRebuild
	CriticalDeepMaxDepth         = reportmodel.CriticalDeepMaxDepth
	DefaultMaxDepth              = reportmodel.DefaultMaxDepth
	DetectionBasisEndpointPath   = reportmodel.DetectionBasisEndpointPath
	DetectionBasisEngineInfo     = reportmodel.DetectionBasisEngineInfo
	DetectionIdentified          = reportmodel.DetectionIdentified
	DetectionUnavailable         = reportmodel.DetectionUnavailable
	DetectionUnidentified        = reportmodel.DetectionUnidentified
	EngineColima                 = reportmodel.EngineColima
	EngineDockerDesktop          = reportmodel.EngineDockerDesktop
	EngineNative                 = reportmodel.EngineNative
	EngineNone                   = reportmodel.EngineNone
	EngineOrbStack               = reportmodel.EngineOrbStack
	EnginePodman                 = reportmodel.EnginePodman
	EngineUnknown                = reportmodel.EngineUnknown
	ExactPlanSchemaID            = reportmodel.ExactPlanSchemaID
	GuestBasisGuestStatfs        = reportmodel.GuestBasisGuestStatfs
	GuestBasisHostStatfs         = reportmodel.GuestBasisHostStatfs
	GuestBasisNone               = reportmodel.GuestBasisNone
	MaxCompareArrayItems         = reportmodel.MaxCompareArrayItems
	MaxCompareInputBytes         = reportmodel.MaxCompareInputBytes
	MaxCompareJSONDepth          = reportmodel.MaxCompareJSONDepth
	PressureCritical             = reportmodel.PressureCritical
	PressureOK                   = reportmodel.PressureOK
	PressureRoleAnalysisRoot     = reportmodel.PressureRoleAnalysisRoot
	PressureRolePrimaryWrite     = reportmodel.PressureRolePrimaryWrite
	PressureWarn                 = reportmodel.PressureWarn
	ProbeMeasured                = reportmodel.ProbeMeasured
	ProbeNotProbed               = reportmodel.ProbeNotProbed
	ProbeUnavailable             = reportmodel.ProbeUnavailable
	ReasonEndpointUnresolved     = reportmodel.ReasonEndpointUnresolved
	ReasonEngineDisagreement     = reportmodel.ReasonEngineDisagreement
	ReasonEngineUnreachable      = reportmodel.ReasonEngineUnreachable
	ReasonParseFailed            = reportmodel.ReasonParseFailed
	ReasonProbeFailed            = reportmodel.ReasonProbeFailed
	ReasonProbeNotRequested      = reportmodel.ReasonProbeNotRequested
	ReasonTimeout                = reportmodel.ReasonTimeout
	ReasonUnsupportedEngine      = reportmodel.ReasonUnsupportedEngine
	RebuildHigh                  = reportmodel.RebuildHigh
	RebuildLow                   = reportmodel.RebuildLow
	RebuildMedium                = reportmodel.RebuildMedium
	RecipeKindArgv               = reportmodel.RecipeKindArgv
	RecipeKindStructural         = reportmodel.RecipeKindStructural
	RefDisclosureAggregate       = reportmodel.RefDisclosureAggregate
	RefDisclosureLocal           = reportmodel.RefDisclosureLocal
	SchemaID                     = reportmodel.SchemaID
	SchemaV2ID                   = reportmodel.SchemaV2ID
	StateDiagnosticOnly          = reportmodel.StateDiagnosticOnly
	StatePrunable                = reportmodel.StatePrunable
	StateUnknown                 = reportmodel.StateUnknown
	StateUnverified              = reportmodel.StateUnverified
	StateWithheld                = reportmodel.StateWithheld
	StepBuildCachePrune          = reportmodel.StepBuildCachePrune
	StepCommand                  = reportmodel.StepCommand
	StepConfigSnippet            = reportmodel.StepConfigSnippet
	StepDanglingImagePrune       = reportmodel.StepDanglingImagePrune
	StepDiskGrow                 = reportmodel.StepDiskGrow
	StepForeignPlatformRemove    = reportmodel.StepForeignPlatformRemove
	StepGuestTrim                = reportmodel.StepGuestTrim
	StepImagePruneAll            = reportmodel.StepImagePruneAll
	StepObserve                  = reportmodel.StepObserve
	StepPurgeHint                = reportmodel.StepPurgeHint
	StepSupersededTagRemove      = reportmodel.StepSupersededTagRemove
	StepVerify                   = reportmodel.StepVerify
	StepVolumePrune              = reportmodel.StepVolumePrune
	TempPlaneSizeMeasured        = reportmodel.TempPlaneSizeMeasured
	TempPlaneSizePartial         = reportmodel.TempPlaneSizePartial
	WithheldReasonDomainDisabled = reportmodel.WithheldReasonDomainDisabled
)

var (
	AnnotateEntryTaxonomy          = reportmodel.AnnotateEntryTaxonomy
	AnnotateRecipeTaxonomy         = reportmodel.AnnotateRecipeTaxonomy
	ClassesIncludePrunable         = reportmodel.ClassesIncludePrunable
	ContainsUnsafeShellMeta        = reportmodel.ContainsUnsafeShellMeta
	DomainEcosystemFromSpine       = reportmodel.DomainEcosystemFromSpine
	EntryMatchesFilters            = reportmodel.EntryMatchesFilters
	EntryTaxonomySpine             = reportmodel.EntryTaxonomySpine
	ErrCapacityOnlyNotPruneCarrier = reportmodel.ErrCapacityOnlyNotPruneCarrier
	FormatCommand                  = reportmodel.FormatCommand
	IsSafeToken                    = reportmodel.IsSafeToken
	LegacySpaceDisclosure          = reportmodel.LegacySpaceDisclosure
	LoadCompareInputJSON           = reportmodel.LoadCompareInputJSON
	LoadSpaceReportJSON            = reportmodel.LoadSpaceReportJSON
	NewCompletion                  = reportmodel.NewCompletion
	NormalizeInventoryFilters      = reportmodel.NormalizeInventoryFilters
	NormalizeReclaimScope          = reportmodel.NormalizeReclaimScope
	ParseAndValidateExactPlanJSON  = reportmodel.ParseAndValidateExactPlanJSON
	ProjectEntries                 = reportmodel.ProjectEntries
	ProjectRecipes                 = reportmodel.ProjectRecipes
	ProjectVerified                = reportmodel.ProjectVerified
	RecipeMatchesFilters           = reportmodel.RecipeMatchesFilters
	RuntimeCompareClaims           = reportmodel.RuntimeCompareClaims
	ShellQuote                     = reportmodel.ShellQuote
	TaxonomyMatches                = reportmodel.TaxonomyMatches
	ValidateDomainSyntax           = reportmodel.ValidateDomainSyntax
	ValidateStandaloneExactPlan    = reportmodel.ValidateStandaloneExactPlan
)

var (
	AnnotateVerifiedBoundState      = reportmodel.AnnotateVerifiedBoundState
	BoundFor                        = reportmodel.BoundFor
	ProjectCommandsFromSteps        = reportmodel.ProjectCommandsFromSteps
	ValidateCapacityCarrier         = reportmodel.ValidateCapacityCarrier
	ValidateCompareCapacityTopology = reportmodel.ValidateCompareCapacityTopology
)
