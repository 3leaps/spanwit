package capacity

import "time"

const (
	StatusMeasured     = "measured"
	StatusPartial      = "partial"
	StatusUnsupported  = "unsupported"
	StatusUnavailable  = "unavailable"
	StatusNotRequested = "not_requested"

	ManagedPolicy = "diagnostic_only_never_prune_handoff"
)

// FilesystemSample is one portable filesystem observation. Callers may project
// it into other carriers, but must not resample when claiming same_observation.
type FilesystemSample struct {
	Path      string
	Mount     string
	VolumeID  string
	FSType    string
	Total     int64
	Used      int64
	Available int64
}

type ByteClaim struct {
	Status string `json:"status"`
	Bytes  *int64 `json:"bytes,omitempty"`
	Human  string `json:"human,omitempty"`
	Basis  string `json:"basis,omitempty"`
	Bound  string `json:"bound,omitempty"`
	Detail string `json:"detail,omitempty"`
	Source string `json:"source,omitempty"`
}

type CoverageGap struct {
	Code           string   `json:"code"`
	Scope          string   `json:"scope"`
	Detail         string   `json:"detail,omitempty"`
	Paths          []string `json:"paths,omitempty"`
	OmittedObjects *int     `json:"omitted_objects,omitempty"`
	OmittedHolders *int     `json:"omitted_holders,omitempty"`
}

type Coverage struct {
	Gaps []CoverageGap `json:"gaps"`
}

type ToolIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
}

type OSIdentity struct {
	GOOS            string `json:"goos"`
	GOARCH          string `json:"goarch"`
	PlatformVersion string `json:"platform_version,omitempty"`
}

type Capture struct {
	CapturedAt      string       `json:"captured_at"`
	Tool            ToolIdentity `json:"tool"`
	OS              OSIdentity   `json:"os"`
	BootID          string       `json:"boot_id,omitempty"`
	BootTime        string       `json:"boot_time,omitempty"`
	SessionBoundary string       `json:"session_boundary,omitempty"`
}

type Target struct {
	Path         string `json:"path"`
	ResolvedPath string `json:"resolved_path,omitempty"`
	Mount        string `json:"mount,omitempty"`
	FSType       string `json:"fs_type,omitempty"`
	VolumeID     string `json:"volume_id,omitempty"`
	ContainerID  string `json:"container_id,omitempty"`
	Platform     string `json:"platform"`
}

type FilesystemPlane struct {
	CollectionStatus string    `json:"collection_status"`
	Source           string    `json:"source,omitempty"`
	Coverage         Coverage  `json:"coverage"`
	PressureRelation string    `json:"pressure_relation,omitempty"`
	Total            ByteClaim `json:"total,omitempty"`
	Used             ByteClaim `json:"used,omitempty"`
	Available        ByteClaim `json:"available,omitempty"`
}

type ContainerPlane struct {
	CollectionStatus string     `json:"collection_status"`
	Source           string     `json:"source,omitempty"`
	Coverage         Coverage   `json:"coverage"`
	ID               string     `json:"id,omitempty"`
	Capacity         *ByteClaim `json:"capacity,omitempty"`
	Free             *ByteClaim `json:"free,omitempty"`
}

type VolumeEntry struct {
	ID            string     `json:"id"`
	Name          string     `json:"name,omitempty"`
	Role          string     `json:"role,omitempty"`
	Roles         []string   `json:"roles,omitempty"`
	Mount         string     `json:"mount,omitempty"`
	CapacityInUse *ByteClaim `json:"capacity_in_use,omitempty"`
	IsTarget      bool       `json:"is_target"`
}

type VolumesPlane struct {
	CollectionStatus string        `json:"collection_status"`
	Source           string        `json:"source,omitempty"`
	Coverage         Coverage      `json:"coverage"`
	Entries          []VolumeEntry `json:"entries,omitempty"`
}

type SnapshotEntry struct {
	ID                    string     `json:"id"`
	Name                  string     `json:"name,omitempty"`
	Purgeable             *bool      `json:"purgeable,omitempty"`
	LimitsContainerShrink *bool      `json:"limits_container_shrink,omitempty"`
	Bytes                 *ByteClaim `json:"bytes,omitempty"`
}

type SnapshotsPlane struct {
	CollectionStatus string          `json:"collection_status"`
	Source           string          `json:"source,omitempty"`
	Coverage         Coverage        `json:"coverage"`
	Count            *int            `json:"count,omitempty"`
	Bytes            *ByteClaim      `json:"bytes,omitempty"`
	Entries          []SnapshotEntry `json:"entries,omitempty"`
}

type SystemManagedEntry struct {
	ID          string     `json:"id"`
	Path        string     `json:"path"`
	TrustState  string     `json:"trust_state"`
	Allocated   *ByteClaim `json:"allocated,omitempty"`
	Remediation string     `json:"remediation"`
}

type SystemManagedPlane struct {
	CollectionStatus string               `json:"collection_status"`
	Source           string               `json:"source,omitempty"`
	Coverage         Coverage             `json:"coverage"`
	Policy           string               `json:"policy"`
	Entries          []SystemManagedEntry `json:"entries,omitempty"`
}

type HeldOpenHolder struct {
	PID            int    `json:"pid"`
	UID            *int   `json:"uid,omitempty"`
	Executable     string `json:"executable"`
	InterfaceGuess string `json:"interface_guess,omitempty"`
}

type HeldOpenObject struct {
	Device       string           `json:"device"`
	Inode        string           `json:"inode"`
	PathPresent  bool             `json:"path_present"`
	RootClass    string           `json:"root_class"`
	LogicalBytes ByteClaim        `json:"logical_bytes"`
	Path         string           `json:"path,omitempty"`
	Holders      []HeldOpenHolder `json:"holders,omitempty"`
}

type HeldOpenGroup struct {
	Key           string     `json:"key"`
	UniqueObjects int        `json:"unique_objects"`
	LogicalBytes  *ByteClaim `json:"logical_bytes,omitempty"`
}

type HeldOpenPlane struct {
	CollectionStatus  string           `json:"collection_status"`
	Source            string           `json:"source,omitempty"`
	Coverage          Coverage         `json:"coverage"`
	Disclosure        string           `json:"disclosure,omitempty"`
	Privilege         string           `json:"privilege,omitempty"`
	ProcessesExamined *int             `json:"processes_examined,omitempty"`
	ProcessesDenied   *int             `json:"processes_denied,omitempty"`
	UniqueObjects     *int             `json:"unique_objects,omitempty"`
	HolderProcesses   *int             `json:"holder_processes,omitempty"`
	Relationships     *int             `json:"relationships,omitempty"`
	LogicalBytes      *ByteClaim       `json:"logical_bytes,omitempty"`
	Entries           []HeldOpenObject `json:"entries,omitempty"`
	Groups            []HeldOpenGroup  `json:"groups,omitempty"`
}

type Planes struct {
	Filesystem    FilesystemPlane    `json:"filesystem"`
	Container     ContainerPlane     `json:"container"`
	Volumes       VolumesPlane       `json:"volumes"`
	Snapshots     SnapshotsPlane     `json:"snapshots"`
	SystemManaged SystemManagedPlane `json:"system_managed"`
	HeldOpen      HeldOpenPlane      `json:"held_open"`
}

type Narrative struct {
	Summary          string   `json:"summary,omitempty"`
	Known            []string `json:"known,omitempty"`
	Unavailable      []string `json:"unavailable,omitempty"`
	UnexplainedHints []string `json:"unexplained_hints"`
}

type Accounting struct {
	Capture   Capture    `json:"capture"`
	Target    Target     `json:"target"`
	Planes    Planes     `json:"planes"`
	Narrative *Narrative `json:"narrative,omitempty"`
}

type BootMetadata struct {
	ID   string
	Time time.Time
}

type ManagedRoot struct {
	ID          string
	Path        string
	Remediation string
}
