package reportmodel

// Container-runtime headroom reports how full a container engine's own storage
// is and what fills it, so a VM data disk that fills from the inside is
// visible before every build fails. It is diagnostic-only and propose-only:
// it adds no prune candidates and spanwit never runs its recipe steps.
//
// The default document aggregates by bucket kind and never names images,
// volumes, containers, VM profiles or socket paths. Image refs appear only
// under RefDisclosureLocal, for a local operator view; refs can carry private
// registry or org names and must stay off publish and commit surfaces.
const (
	EngineColima        = "colima"
	EngineDockerDesktop = "docker-desktop"
	EngineOrbStack      = "orbstack"
	EnginePodman        = "podman"
	EngineNative        = "native"
	EngineUnknown       = "unknown"
	EngineNone          = "none"

	DetectionIdentified   = "identified"
	DetectionUnidentified = "unidentified"
	DetectionUnavailable  = "unavailable"

	DetectionBasisEndpointPath = "endpoint_path"
	DetectionBasisEngineInfo   = "engine_info"

	ProbeMeasured    = "measured"
	ProbeUnavailable = "unavailable"
	ProbeNotProbed   = "not_probed"

	GuestBasisGuestStatfs = "guest_statfs"
	GuestBasisHostStatfs  = "host_statfs"
	GuestBasisNone        = "none"

	RefDisclosureAggregate = "aggregate"
	RefDisclosureLocal     = "local"

	BucketImages              = "images"
	BucketBuildCache          = "build_cache"
	BucketDanglingImages      = "dangling_images"
	BucketForeignPlatform     = "foreign_platform"
	BucketSupersededTags      = "superseded_tags"
	BucketVolumesUnreferenced = "volumes_unreferenced"

	CostNone         = "none"
	CostRebuild      = "rebuild"
	CostRePull       = "re_pull"
	CostDataLossRisk = "data_loss_risk"

	StepBuildCachePrune       = "build_cache_prune"
	StepDanglingImagePrune    = "dangling_image_prune"
	StepForeignPlatformRemove = "foreign_platform_remove"
	StepSupersededTagRemove   = "superseded_tag_remove"
	StepVolumePrune           = "volume_prune"
	StepImagePruneAll         = "image_prune_all"
	StepGuestTrim             = "guest_trim"
	StepDiskGrow              = "disk_grow"

	ReasonEndpointUnresolved = "endpoint_unresolved"
	ReasonEngineUnreachable  = "engine_unreachable"
	ReasonEngineDisagreement = "engine_disagreement"
	ReasonProbeFailed        = "probe_failed"
	ReasonTimeout            = "timeout"
	ReasonParseFailed        = "parse_failed"
	ReasonUnsupportedEngine  = "unsupported_engine"
	ReasonProbeNotRequested  = "probe_not_requested"

	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
	ArchOther = "other"
)

// ContainerRuntime is the container_runtime section of the space report.
type ContainerRuntime struct {
	Engine          string              `json:"engine"`
	Detection       EngineDetection     `json:"detection"`
	EngineArch      string              `json:"engine_arch,omitempty"`
	APIVersion      string              `json:"api_version,omitempty"`
	RefDisclosure   string              `json:"ref_disclosure"`
	GuestFilesystem GuestFilesystem     `json:"guest_filesystem"`
	Buckets         []RuntimeBucket     `json:"buckets"`
	Recipe          []RuntimeRecipeStep `json:"recipe"`
	ImageRefs       []RuntimeImageRef   `json:"image_refs,omitempty"`
}

// EngineDetection records how the engine was identified. Basis names the
// evidence (endpoint path, engine info), never the path or info itself.
type EngineDetection struct {
	Status string   `json:"status"`
	Basis  []string `json:"basis"`
	Reason string   `json:"reason,omitempty"`
}

// GuestFilesystem is the statfs of the engine data root as the engine sees
// it. Missing data is never zero: unmeasured figures are absent with a reason.
type GuestFilesystem struct {
	Status     string `json:"status"`
	Basis      string `json:"basis"`
	TotalBytes *int64 `json:"total_bytes,omitempty"`
	UsedBytes  *int64 `json:"used_bytes,omitempty"`
	AvailBytes *int64 `json:"avail_bytes,omitempty"`
	Level      string `json:"level,omitempty"`
	// Bound is exact when measured, absent otherwise; derived on marshal.
	Bound  string `json:"bound,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// RuntimeBucket aggregates one kind of engine storage. For images, Bytes sums
// per-image sizes and double-counts shared layers; UniqueBytes is the honest
// reclaim bound.
type RuntimeBucket struct {
	Kind             string `json:"kind"`
	Status           string `json:"status"`
	Count            *int64 `json:"count,omitempty"`
	Bytes            *int64 `json:"bytes,omitempty"`
	UniqueBytes      *int64 `json:"unique_bytes,omitempty"`
	SharedBytes      *int64 `json:"shared_bytes,omitempty"`
	ReclaimableBytes *int64 `json:"reclaimable_bytes,omitempty"`
	InUse            *int64 `json:"in_use,omitempty"`
	// Bound is exact when measured, absent when unavailable; derived on marshal.
	Bound  string `json:"bound,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// RuntimeRecipeStep is one cost-classed relief suggestion. Command is generic
// argv and never carries an image ref, volume name, profile or host path.
type RuntimeRecipeStep struct {
	Step      string         `json:"step"`
	CostClass string         `json:"cost_class"`
	Estimate  RuntimeMeasure `json:"estimate"`
	Command   []string       `json:"command,omitempty"`
	Warning   string         `json:"warning,omitempty"`
}

// RuntimeMeasure is a byte figure present only when measured. Bound states
// its direction: lower (at least), upper (at most) or exact.
type RuntimeMeasure struct {
	Status string `json:"status"`
	Bytes  *int64 `json:"bytes,omitempty"`
	Bound  string `json:"bound,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// RuntimeImageRef is a local-only image reference (RefDisclosureLocal).
type RuntimeImageRef struct {
	Ref         string `json:"ref"`
	Bucket      string `json:"bucket"`
	Platform    string `json:"platform,omitempty"`
	UniqueBytes int64  `json:"unique_bytes"`
	// Bound is always exact (engine-reported); set on marshal.
	Bound string `json:"bound,omitempty"`
}

// RuntimeCompareClaims reduces a container_runtime section to named byte
// claims for space --compare. Only measured figures carry bytes; anything
// else becomes an unavailable claim so the delta is indeterminate, never 0.
func RuntimeCompareClaims(rt *ContainerRuntime) *RuntimeClaims {
	if rt == nil {
		return nil
	}
	out := &RuntimeClaims{Engine: rt.Engine, Identified: rt.Detection.Status == DetectionIdentified}
	claim := func(field, status, basis string, b *int64) {
		c := ByteClaim{Status: StatusUnavailable}
		if status == ProbeMeasured && b != nil {
			c = ByteClaim{Status: StatusMeasured, Bytes: b, Basis: basis, Bound: "exact", Source: "container_runtime"}
		}
		out.Claims = append(out.Claims, NamedClaim{Field: field, Claim: c})
	}
	g := rt.GuestFilesystem
	claim("guest.used", g.Status, g.Basis, g.UsedBytes)
	claim("guest.available", g.Status, g.Basis, g.AvailBytes)
	for _, b := range rt.Buckets {
		claim(b.Kind+".bytes", b.Status, "engine_api", b.Bytes)
		if b.UniqueBytes != nil || b.Kind == BucketImages {
			claim(b.Kind+".unique_bytes", b.Status, "engine_api", b.UniqueBytes)
		}
		if b.Kind == BucketBuildCache {
			claim(b.Kind+".reclaimable_bytes", b.Status, "engine_api", b.ReclaimableBytes)
		}
	}
	return out
}
