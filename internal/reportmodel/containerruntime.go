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
	// BucketOlderSibling labels image refs only (an older build in an image
	// family); it is never a bucket kind.
	BucketOlderSibling = "older_sibling"

	CostNone         = "none"
	CostRebuild      = "rebuild"
	CostRePull       = "re_pull"
	CostDataLossRisk = "data_loss_risk"

	StepBuildCachePrune       = "build_cache_prune"
	StepDanglingImagePrune    = "dangling_image_prune"
	StepForeignPlatformRemove = "foreign_platform_remove"
	StepSupersededTagRemove   = "superseded_tag_remove"
	StepContainerPrune        = "container_prune"
	StepOlderSiblingRemove    = "older_sibling_remove"
	StepVolumePrune           = "volume_prune"
	StepImagePruneAll         = "image_prune_all"
	StepGuestTrim             = "guest_trim"
	StepDiskGrow              = "disk_grow"

	ReasonEndpointUnresolved = "endpoint_unresolved"
	ReasonEngineUnreachable  = "engine_unreachable"
	ReasonEngineError        = "engine_error"
	ReasonEngineStopped      = "engine_stopped"
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
	HostDisk        *RuntimeHostDisk    `json:"host_disk,omitempty"`
	ImageFamilies   *ImageFamilies      `json:"image_families,omitempty"`
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
	// HTTPStatus is the engine's status code when Reason is engine_error and
	// the code is plausible (100..599). The response body is never kept.
	HTTPStatus *int `json:"http_status,omitempty"`
}

// ImageFamilies groups version-tagged images into lines (one repository,
// variant and major version) and counts older builds in each (older
// siblings). Tags that are not versions are never ranked. Aggregate only: names appear only in local image
// refs. In use is judged from containers alone; compose files and Dockerfile
// pins are not seen, so an older sibling is informational, never "safe".
type ImageFamilies struct {
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	HTTPStatus *int   `json:"http_status,omitempty"`
	// Families counts version lines with two or more native images.
	Families *int64 `json:"families,omitempty"`
	// OrderAmbiguous counts families whose version and build-time orders
	// disagree, or with a version or build-time tie; none are ranked.
	OrderAmbiguous *int64 `json:"order_ambiguous,omitempty"`
	// InUseStatus is unavailable when containers could not be listed, or a
	// container's image matches no listed image (engine_disagreement); then
	// no image is counted as an older sibling.
	InUseStatus string `json:"in_use_status,omitempty"`
	InUseReason string `json:"in_use_reason,omitempty"`
	// OlderSiblings excludes images used by any container.
	OlderSiblings     *int64 `json:"older_siblings,omitempty"`
	OlderSiblingBytes *int64 `json:"older_sibling_bytes,omitempty"`
	BytesBound        string `json:"bytes_bound,omitempty"`
	// LocallyBuilt counts older siblings that would need a rebuild, not a
	// re-pull (a :local tag or no registry digest).
	LocallyBuilt *int64 `json:"locally_built,omitempty"`
	// InUseByExitedOnly counts older builds used only by stopped containers:
	// removable once those containers are removed.
	InUseByExitedOnly *int64 `json:"in_use_by_exited_only,omitempty"`
}

// RuntimeHostDisk is the VM disk as the host sees it: logical size and the
// bytes actually allocated (the disk is sparse). Measured from the host, so
// it is present even when the engine is stopped. Never names the VM or path.
type RuntimeHostDisk struct {
	Status         string `json:"status"`
	Files          *int64 `json:"files,omitempty"`
	LogicalBytes   *int64 `json:"logical_bytes,omitempty"`
	AllocatedBytes *int64 `json:"allocated_bytes,omitempty"`
	// Bound is exact when measured, absent otherwise; derived on marshal.
	Bound  string `json:"bound,omitempty"`
	Reason string `json:"reason,omitempty"`
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
	// HTTPStatus accompanies reason engine_error (100..599 only).
	HTTPStatus *int `json:"http_status,omitempty"`
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
	Ref string `json:"ref"`
	// ID is the short image ID (sha256: plus 12 hex), validated; the form to
	// paste into a removal command instead of a name.
	ID          string `json:"id,omitempty"`
	Bucket      string `json:"bucket"`
	Platform    string `json:"platform,omitempty"`
	UniqueBytes int64  `json:"unique_bytes"`
	// Bound is exact (engine-reported) unless set to upper, for an image
	// size standing in for its unique bytes; set on marshal.
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
