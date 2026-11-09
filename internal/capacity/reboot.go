package capacity

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strconv"
	"strings"
)

// darwinVMMount is where macOS mounts the volume that holds this boot's swap
// files (dynamic_pager). A VM-role volume anywhere else, such as one in an
// external macOS install, is not the live swap store.
const darwinVMMount = "/System/Volumes/VM"

// SwapUsageFunc returns the kernel's current swap reservation in bytes
// (darwin: vm.swapusage total). Any failure, including a malformed,
// negative or overflowing value, is an error; the caller never sees a
// partial figure.
type SwapUsageFunc func(ctx context.Context) (int64, error)

// MountEntry is one row of the OS mount table: the source device node
// (e.g. /dev/disk3s6), the mount point, and the filesystem id from the same
// row (f_fsid; equal to st_dev of files on that volume). No path is statted:
// a path can follow a symlink, race an unmount, or block on a network mount.
type MountEntry struct {
	From string
	On   string
	Dev  uint64
}

// MountTableFunc returns every mounted filesystem. An error means the table
// is unknown or may be incomplete, and no conclusion may be drawn from a
// device's absence.
type MountTableFunc func() ([]MountEntry, error)

var errSwapUsageUnsupported = errors.New("swap usage probe unsupported on this platform")

// targetDevices is the set of st_dev values of the target container's
// mounted volumes. complete is false when the volumes plane is not measured
// or any mounted volume could not be statted; then no "other container"
// conclusion may be drawn from absence.
type targetDevices struct {
	devs     map[uint64]struct{}
	complete bool
}

// mountedFromVolume reports whether a mount source node belongs to an APFS
// volume id: the volume itself (/dev/disk3s6) or a snapshot of it mounted
// in its place (/dev/disk3s1s1 for the sealed system volume disk3s1).
func mountedFromVolume(from, volumeID string) bool {
	if !isAPFSVolumeID(volumeID) {
		return false // a container id (disk3) must never match its volumes
	}
	node := "/dev/" + volumeID
	if from == node {
		return true
	}
	rest, ok := strings.CutPrefix(from, node+"s")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isAPFSVolumeID accepts disk<N>s<M> only (ASCII digits).
func isAPFSVolumeID(id string) bool {
	rest, ok := strings.CutPrefix(id, "disk")
	if !ok {
		return false
	}
	n, m, ok := strings.Cut(rest, "s")
	return ok && allDigits(n) && allDigits(m)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// collectTargetDevices maps the target container's volumes to the devices
// they are mounted as, using the OS mount table (the volumes plane carries
// membership but not mount points). The set is complete only when the
// volumes plane is measured and the whole mount table was read: a volume
// with no mount row is then known to be unmounted.
// An APFS sealed system snapshot mounted at / carries its own fsid; the
// volume group's Data row carries the device its files report, so matching
// is "any row of a target volume", never one device per volume.
func collectTargetDevices(volumes VolumesPlane, mounts []MountEntry, mountsOK bool) targetDevices {
	out := targetDevices{
		devs:     map[uint64]struct{}{},
		complete: volumes.CollectionStatus == StatusMeasured && mountsOK,
	}
	if !mountsOK {
		return out
	}
	for _, v := range volumes.Entries {
		if !isAPFSVolumeID(v.ID) {
			out.complete = false // cannot be matched, so cannot be shown unmounted
			continue
		}
		for _, m := range mounts {
			if mountedFromVolume(m.From, v.ID) {
				out.devs[m.Dev] = struct{}{}
			}
		}
	}
	return out
}

// swapScope binds swap to the live VM mount and compares its device with
// the target container's mounted devices. The live mount is the single
// mount-table row whose mount point is /System/Volumes/VM; that row's
// existence is the mount-point proof. Role presence is never used.
func swapScope(target targetDevices, mounts []MountEntry, mountsOK bool) string {
	if !mountsOK {
		return RebootScopeUnknown
	}
	var vm uint64
	found := 0
	for _, m := range mounts {
		if m.On == darwinVMMount {
			vm = m.Dev
			found++
		}
	}
	if found != 1 {
		return RebootScopeUnknown // not mounted, or ambiguous
	}
	if _, ok := target.devs[vm]; ok {
		return RebootScopeTargetContainer
	}
	if target.complete {
		// The live store is mounted; were it in the target container, one
		// of the (all statted) mounted target volumes would carry its device.
		return RebootScopeOtherContainer
	}
	return RebootScopeUnknown
}

func deriveSwap(ctx context.Context, platform string, probe SwapUsageFunc, scope string) RebootSwap {
	switch platform {
	case "linux":
		return RebootSwap{Status: RebootSwapNotReleasable, Reason: RebootReasonStaticSwap, Scope: RebootScopeUnknown}
	case "darwin":
	default:
		return RebootSwap{Status: StatusUnsupported, Scope: RebootScopeUnknown}
	}
	if probe == nil {
		return RebootSwap{Status: StatusUnavailable, Reason: RebootReasonProbeFailed, Scope: scope}
	}
	total, err := probe(ctx)
	if err != nil || total < 0 {
		return RebootSwap{Status: StatusUnavailable, Reason: RebootReasonProbeFailed, Scope: scope}
	}
	switch scope {
	case RebootScopeTargetContainer:
		claim := ByteClaim{
			Status: StatusMeasured,
			Bytes:  &total,
			Human:  HumanBytes(total),
			Basis:  BasisVMSwapUsageTotal,
			Bound:  BoundExact,
			Source: "sysctl_vm_swapusage",
		}
		return RebootSwap{Status: StatusMeasured, Scope: scope, Bytes: &claim}
	case RebootScopeOtherContainer:
		return RebootSwap{Status: RebootSwapNotReleasable, Reason: RebootReasonNotBootContainer, Scope: scope}
	default:
		// Measured, but the system-wide reservation is not attached to the
		// target without a binding.
		return RebootSwap{Status: StatusMeasured, Scope: RebootScopeUnknown}
	}
}

// parseXswUsageTotal decodes xsu_total, the first uint64 of xsw_usage, in
// host byte order (darwin targets are little-endian).
func parseXswUsageTotal(raw []byte) (int64, error) {
	if len(raw) < 24 { // xsu_total, xsu_avail, xsu_used
		return 0, errors.New("vm.swapusage: short buffer")
	}
	total := binary.LittleEndian.Uint64(raw[0:8])
	if total > math.MaxInt64 {
		return 0, errors.New("vm.swapusage: total overflows int64")
	}
	return int64(total), nil // #nosec G115 -- bounded by the MaxInt64 check above.
}

// heldOpenScoped is the held-open logical sum restricted to the target
// container, computed from every deduplicated object before any detail cap.
type heldOpenScoped struct {
	bytes      int64
	unmapped   int
	overflow   bool
	incomplete bool // target device set incomplete: no scoped figure
}

// lsofDevice parses lsof's D field (hex, e.g. 0x1000012) into st_dev.
func lsofDevice(v string) (uint64, bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "0x") && !strings.HasPrefix(v, "0X") {
		return 0, false
	}
	n, err := strconv.ParseUint(v[2:], 16, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func scopeHeldOpen(parsed heldOpenParseResult, target targetDevices, mounted map[uint64]struct{}) heldOpenScoped {
	// mounted holds every mount row's device; an object on a device in
	// neither set is unmappable.
	if !target.complete {
		return heldOpenScoped{incomplete: true}
	}
	// Deduplicate by (device, inode) with the largest size seen, exactly as
	// the plane does, before anything is filtered, summed or capped.
	type obj struct {
		device string
		size   int64
	}
	objects := make(map[string]obj, len(parsed.files))
	for _, f := range parsed.files {
		key := f.device + "\x00" + f.inode
		if o, ok := objects[key]; !ok || f.size > o.size {
			objects[key] = obj{device: f.device, size: f.size}
		}
	}
	var out heldOpenScoped
	for _, o := range objects {
		dev, ok := lsofDevice(o.device)
		if !ok {
			out.unmapped++
			continue
		}
		if _, in := target.devs[dev]; in {
			if o.size < 0 || out.bytes > math.MaxInt64-o.size {
				out.overflow = true
				continue
			}
			out.bytes += o.size
			continue
		}
		if _, known := mounted[dev]; known {
			continue // on another known filesystem: out of scope, not a gap
		}
		out.unmapped++
	}
	return out
}

func deriveHeldOpen(requested bool, plane HeldOpenPlane, scoped *heldOpenScoped) RebootHeldOpen {
	out := RebootHeldOpen{ScopeFilter: RebootScopeTargetContainer}
	switch {
	case !requested || plane.CollectionStatus == StatusNotRequested:
		out.Status = StatusNotRequested
		return out
	case plane.CollectionStatus == StatusUnsupported:
		out.Status = StatusUnsupported
		return out
	case scoped == nil || scoped.incomplete || scoped.overflow || !planeCarriesSum(plane):
		// Collection failure with no measured sum, an incomplete device map,
		// or an overflow: partial, and never a fabricated measured zero.
		out.Status = StatusPartial
		return out
	}
	b := scoped.bytes
	out.LogicalBytes = &ByteClaim{
		Status: StatusMeasured,
		Bytes:  &b,
		Human:  HumanBytes(b),
		Basis:  "lsof_file_size_dedup_device_inode",
		Bound:  BoundLower,
		Source: heldOpenSource,
	}
	out.Status = StatusMeasured
	if scoped.unmapped > 0 || plane.CollectionStatus != StatusMeasured {
		out.Status = StatusPartial
		out.LogicalBytes.Status = StatusPartial
	}
	return out
}

// planeCarriesSum: the held-open plane produced a measured or partial
// lower-bound sum. A partial plane (for example truncated detail, or some
// processes not visible) still yields a valid lower bound over what was
// observed; an unavailable plane yields none.
func planeCarriesSum(plane HeldOpenPlane) bool {
	if plane.CollectionStatus != StatusMeasured && plane.CollectionStatus != StatusPartial {
		return false
	}
	return plane.LogicalBytes != nil && plane.LogicalBytes.Bytes != nil
}

// deriveRebootReleasable assembles the block. Block status: unsupported off
// darwin and linux; else partial when any input is partial (swap probe
// failure, swap measured but unattributed, held-open partial); else measured.
func deriveRebootReleasable(swap RebootSwap, held RebootHeldOpen, platform string) *RebootReleasable {
	r := &RebootReleasable{
		Semantics:  RebootSemanticsObservedGross,
		Swap:       swap,
		HeldOpen:   held,
		TempPlanes: RebootTempPlanes{Disposition: RebootTempMayBeCleared},
		Purgeable:  RebootPurgeable{Status: StatusUnavailable, Reason: RebootReasonNoUnprivilegedSrc},
	}
	switch {
	case platform != "darwin" && platform != "linux":
		r.Status = StatusUnsupported
	case swap.Status == StatusUnavailable,
		swap.Status == StatusMeasured && swap.Scope == RebootScopeUnknown,
		held.Status == StatusPartial:
		r.Status = StatusPartial
	default:
		r.Status = StatusMeasured
	}
	return r
}

// rebootReleasable runs the bounded probes for the block. Only darwin binds
// swap scope and scopes held-open objects; linux and other platforms are
// decided without probing.
func (c Collector) rebootReleasable(
	ctx context.Context,
	platform string,
	planes Planes,
	heldOpenRequested bool,
	heldParsed *heldOpenParseResult,
) *RebootReleasable {
	live := c.Platform == ""
	swapUsage, mountTable := c.SwapUsage, c.MountTable
	if live {
		if swapUsage == nil {
			swapUsage = defaultSwapUsage()
		}
		if mountTable == nil {
			mountTable = defaultMountTable()
		}
	}
	scope := RebootScopeUnknown
	var scoped *heldOpenScoped
	if platform == "darwin" {
		var mounts []MountEntry
		mountsOK := false
		if mountTable != nil {
			if m, err := mountTable(); err == nil {
				mounts, mountsOK = m, true
			}
		}
		target := collectTargetDevices(planes.Volumes, mounts, mountsOK)
		scope = swapScope(target, mounts, mountsOK)
		if heldOpenRequested && heldParsed != nil {
			mounted := map[uint64]struct{}{}
			for _, m := range mounts {
				mounted[m.Dev] = struct{}{}
			}
			s := scopeHeldOpen(*heldParsed, target, mounted)
			scoped = &s
		}
	}
	swap := deriveSwap(ctx, platform, swapUsage, scope)
	held := deriveHeldOpen(heldOpenRequested, planes.HeldOpen, scoped)
	return deriveRebootReleasable(swap, held, platform)
}
