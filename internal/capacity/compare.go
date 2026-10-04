package capacity

import (
	"fmt"
	"sort"
	"time"
)

const CompareSchemaID = "https://schemas.3leaps.dev/spanwit/space-compare/v1.json"

type CompareInput struct {
	Path          string
	ReportSchema  string
	ReportVersion int
	CaptureMode   string
	CapturedAt    string
	Accounting    Accounting
}

type CompareSide struct {
	Path            string `json:"path,omitempty"`
	CapturedAt      string `json:"captured_at"`
	CaptureMode     string `json:"capture_mode"`
	ReportSchema    string `json:"report_schema"`
	Platform        string `json:"platform,omitempty"`
	VolumeID        string `json:"volume_id,omitempty"`
	ContainerID     string `json:"container_id,omitempty"`
	SessionBoundary string `json:"session_boundary,omitempty"`
	BootID          string `json:"boot_id,omitempty"`
}

type CompatibilityCheck struct {
	Name          string `json:"name"`
	OK            bool   `json:"ok"`
	Detail        string `json:"detail,omitempty"`
	Informational bool   `json:"informational,omitempty"`
}

type CompareCompatibility struct {
	Status string               `json:"status"`
	Checks []CompatibilityCheck `json:"checks"`
	Detail string               `json:"detail,omitempty"`
}

type PlaneDelta struct {
	Plane       string  `json:"plane"`
	Field       string  `json:"field"`
	LeftBytes   *int64  `json:"left_bytes,omitempty"`
	RightBytes  *int64  `json:"right_bytes,omitempty"`
	DeltaBytes  *int64  `json:"delta_bytes,omitempty"`
	DeltaStatus string  `json:"delta_status"`
	LeftBound   string  `json:"left_bound,omitempty"`
	RightBound  string  `json:"right_bound,omitempty"`
	LeftBasis   string  `json:"left_basis,omitempty"`
	RightBasis  string  `json:"right_basis,omitempty"`
	LeftSource  string  `json:"left_source,omitempty"`
	RightSource string  `json:"right_source,omitempty"`
	Detail      *string `json:"detail,omitempty"`
}

type CompareCoverage struct {
	LeftPlaneNotes   []string `json:"left_plane_notes"`
	RightPlaneNotes  []string `json:"right_plane_notes"`
	AsymmetricPlanes []string `json:"asymmetric_planes"`
}

type CompareNarrative struct {
	Known       []string `json:"known"`
	Unavailable []string `json:"unavailable"`
	Unexplained []string `json:"unexplained"`
}

type CompareReport struct {
	Schema        string               `json:"$schema"`
	Version       int                  `json:"version"`
	GeneratedAt   string               `json:"generated_at"`
	Left          CompareSide          `json:"left"`
	Right         CompareSide          `json:"right"`
	Compatibility CompareCompatibility `json:"compatibility"`
	PlaneDeltas   []PlaneDelta         `json:"plane_deltas"`
	Coverage      CompareCoverage      `json:"coverage"`
	Narrative     *CompareNarrative    `json:"narrative,omitempty"`
	Warnings      []string             `json:"warnings,omitempty"`
}

type claimRef struct {
	claim  *ByteClaim
	source string
}

func Compare(now time.Time, left, right CompareInput) CompareReport {
	platformOK, platformKnown := sameKnown(left.Accounting.Target.Platform, right.Accounting.Target.Platform)
	volumeOK, volumeKnown := sameKnown(left.Accounting.Target.VolumeID, right.Accounting.Target.VolumeID)
	containerOK, containerKnown := sameKnown(left.Accounting.Target.ContainerID, right.Accounting.Target.ContainerID)

	checks := []CompatibilityCheck{
		identityCheck("platform", platformOK, platformKnown),
		identityCheck("volume_id", volumeOK, volumeKnown),
		identityCheck("container_id", containerOK, containerKnown),
		{
			Name:   "measurement_semantics",
			OK:     true,
			Detail: "source and basis compatibility is evaluated per claim",
		},
		informationalCheck("capture_mode", left.CaptureMode, right.CaptureMode),
		informationalCheck("boot_id", left.Accounting.Capture.BootID, right.Accounting.Capture.BootID),
		{
			Name:   "report_version",
			OK:     left.ReportVersion == 2 && right.ReportVersion == 2,
			Detail: fmt.Sprintf("left=%d right=%d", left.ReportVersion, right.ReportVersion),
		},
	}
	compatibilityStatus := "compatible"
	if !platformKnown || !volumeKnown || !containerKnown || !volumeOK || !containerOK {
		compatibilityStatus = "partial"
	}
	if platformKnown && !platformOK {
		compatibilityStatus = "incompatible"
	}

	leftPlanes := left.Accounting.Planes
	rightPlanes := right.Accounting.Planes
	filesystemGate := platformOK && platformKnown && volumeOK && volumeKnown
	containerGate := platformOK && platformKnown && containerOK && containerKnown
	platformGate := platformOK && platformKnown

	deltas := []PlaneDelta{
		compareClaim("filesystem", "total",
			claimRef{&leftPlanes.Filesystem.Total, leftPlanes.Filesystem.Source},
			claimRef{&rightPlanes.Filesystem.Total, rightPlanes.Filesystem.Source},
			filesystemGate, identityGateDetail("platform and volume_id", filesystemGate)),
		compareClaim("filesystem", "used",
			claimRef{&leftPlanes.Filesystem.Used, leftPlanes.Filesystem.Source},
			claimRef{&rightPlanes.Filesystem.Used, rightPlanes.Filesystem.Source},
			filesystemGate, identityGateDetail("platform and volume_id", filesystemGate)),
		compareClaim("filesystem", "available",
			claimRef{&leftPlanes.Filesystem.Available, leftPlanes.Filesystem.Source},
			claimRef{&rightPlanes.Filesystem.Available, rightPlanes.Filesystem.Source},
			filesystemGate, identityGateDetail("platform and volume_id", filesystemGate)),
		compareClaim("container", "capacity",
			claimRef{leftPlanes.Container.Capacity, leftPlanes.Container.Source},
			claimRef{rightPlanes.Container.Capacity, rightPlanes.Container.Source},
			containerGate, identityGateDetail("platform and container_id", containerGate)),
		compareClaim("container", "free",
			claimRef{leftPlanes.Container.Free, leftPlanes.Container.Source},
			claimRef{rightPlanes.Container.Free, rightPlanes.Container.Source},
			containerGate, identityGateDetail("platform and container_id", containerGate)),
		compareClaim("volumes", "target.capacity_in_use",
			targetVolumeClaim(leftPlanes.Volumes),
			targetVolumeClaim(rightPlanes.Volumes),
			containerGate,
			identityGateDetail("platform and container_id", containerGate)),
		compareClaim("snapshots", "bytes",
			claimRef{leftPlanes.Snapshots.Bytes, leftPlanes.Snapshots.Source},
			claimRef{rightPlanes.Snapshots.Bytes, rightPlanes.Snapshots.Source},
			containerGate, identityGateDetail("platform and container_id", containerGate)),
	}

	managedIDs := unionManagedIDs(leftPlanes.SystemManaged, rightPlanes.SystemManaged)
	for _, id := range managedIDs {
		deltas = append(deltas, compareClaim(
			"system_managed",
			id+".allocated",
			managedClaim(leftPlanes.SystemManaged, id),
			managedClaim(rightPlanes.SystemManaged, id),
			platformGate,
			identityGateDetail("platform", platformGate),
		))
	}

	deltas = append(deltas, compareHeldOpenClaim(
		leftPlanes.HeldOpen,
		rightPlanes.HeldOpen,
		platformGate,
	))
	semanticsOK := true
	for _, delta := range deltas {
		if delta.Detail != nil && *delta.Detail == "claim source or basis differs" {
			semanticsOK = false
			break
		}
	}
	if !semanticsOK {
		checks[3].OK = false
		checks[3].Detail = "one or more comparable claims use different source or basis semantics"
		if compatibilityStatus == "compatible" {
			compatibilityStatus = "partial"
		}
	}

	asymmetric := asymmetricPlanes(leftPlanes, rightPlanes)
	coverage := CompareCoverage{
		LeftPlaneNotes:   planeNotes(leftPlanes),
		RightPlaneNotes:  planeNotes(rightPlanes),
		AsymmetricPlanes: asymmetric,
	}
	known := make([]string, 0)
	unavailable := make([]string, 0)
	for _, delta := range deltas {
		name := delta.Plane + "." + delta.Field
		if delta.DeltaStatus == "exact" && delta.DeltaBytes != nil {
			known = append(known, fmt.Sprintf(
				"%s changed by %+d bytes (right minus left)",
				name,
				*delta.DeltaBytes,
			))
			continue
		}
		detail := delta.DeltaStatus
		if delta.Detail != nil && *delta.Detail != "" {
			detail += ": " + *delta.Detail
		}
		unavailable = append(unavailable, name+" delta "+detail)
	}

	return CompareReport{
		Schema:      CompareSchemaID,
		Version:     1,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Left:        compareSide(left),
		Right:       compareSide(right),
		Compatibility: CompareCompatibility{
			Status: compatibilityStatus,
			Checks: checks,
			Detail: "compatibility is evaluated per plane; capture mode and boot identity are informational",
		},
		PlaneDeltas: deltas,
		Coverage:    coverage,
		Narrative: &CompareNarrative{
			Known:       known,
			Unavailable: unavailable,
			Unexplained: []string{},
		},
	}
}

func compareSide(input CompareInput) CompareSide {
	accounting := input.Accounting
	return CompareSide{
		Path:            input.Path,
		CapturedAt:      input.CapturedAt,
		CaptureMode:     input.CaptureMode,
		ReportSchema:    input.ReportSchema,
		Platform:        accounting.Target.Platform,
		VolumeID:        accounting.Target.VolumeID,
		ContainerID:     accounting.Target.ContainerID,
		SessionBoundary: accounting.Capture.SessionBoundary,
		BootID:          accounting.Capture.BootID,
	}
}

func identityCheck(name string, ok, known bool) CompatibilityCheck {
	check := CompatibilityCheck{Name: name, OK: ok && known}
	switch {
	case !known:
		check.Detail = name + " is missing on one or both inputs"
	case ok:
		check.Detail = name + " matches"
	default:
		check.Detail = name + " differs"
	}
	return check
}

func informationalCheck(name, left, right string) CompatibilityCheck {
	known := left != "" && right != ""
	ok := known && left == right
	detail := name + " differs; this is informational"
	if !known {
		detail = name + " is missing on one or both inputs; this is informational"
	} else if ok {
		detail = name + " matches"
	}
	return CompatibilityCheck{
		Name:          name,
		OK:            ok,
		Detail:        detail,
		Informational: true,
	}
}

func sameKnown(left, right string) (bool, bool) {
	if left == "" || right == "" {
		return false, false
	}
	return left == right, true
}

func identityGateDetail(identity string, ok bool) string {
	if ok {
		return ""
	}
	return identity + " does not provide a compatible identity gate"
}

func compareClaim(
	plane, field string,
	left, right claimRef,
	gate bool,
	gateDetail string,
) PlaneDelta {
	delta := PlaneDelta{
		Plane:       plane,
		Field:       field,
		DeltaStatus: "indeterminate",
	}
	copyClaimEndpoint(&delta, left, true)
	copyClaimEndpoint(&delta, right, false)
	if !gate {
		delta.DeltaStatus = "incomparable"
		delta.Detail = stringPointer(gateDetail)
		return delta
	}
	if left.claim == nil || right.claim == nil ||
		left.claim.Bytes == nil || right.claim.Bytes == nil {
		delta.Detail = stringPointer("one or both numeric claims are unavailable")
		return delta
	}
	if left.claim.Status != StatusMeasured || right.claim.Status != StatusMeasured {
		delta.Detail = stringPointer("one or both claims are not measured")
		return delta
	}
	leftSource := effectiveSource(left)
	rightSource := effectiveSource(right)
	if left.claim.Basis == "" || right.claim.Basis == "" ||
		leftSource == "" || rightSource == "" {
		delta.Detail = stringPointer("one or both claims lack source or basis semantics")
		return delta
	}
	if left.claim.Basis != right.claim.Basis || leftSource != rightSource {
		delta.DeltaStatus = "incomparable"
		delta.Detail = stringPointer("claim source or basis differs")
		return delta
	}
	if left.claim.Bound != "exact" || right.claim.Bound != "exact" {
		delta.Detail = stringPointer("non-exact endpoints cannot produce a numeric delta")
		return delta
	}
	value := *right.claim.Bytes - *left.claim.Bytes
	delta.DeltaBytes = &value
	delta.DeltaStatus = "exact"
	return delta
}

func copyClaimEndpoint(delta *PlaneDelta, ref claimRef, left bool) {
	if ref.claim == nil {
		return
	}
	if left {
		delta.LeftBytes = ref.claim.Bytes
		delta.LeftBound = ref.claim.Bound
		delta.LeftBasis = ref.claim.Basis
		delta.LeftSource = effectiveSource(ref)
		return
	}
	delta.RightBytes = ref.claim.Bytes
	delta.RightBound = ref.claim.Bound
	delta.RightBasis = ref.claim.Basis
	delta.RightSource = effectiveSource(ref)
}

func effectiveSource(ref claimRef) string {
	if ref.claim != nil && ref.claim.Source != "" {
		return ref.claim.Source
	}
	return ref.source
}

func stringPointer(value string) *string {
	return &value
}

func targetVolumeClaim(plane VolumesPlane) claimRef {
	for index := range plane.Entries {
		if plane.Entries[index].IsTarget {
			return claimRef{plane.Entries[index].CapacityInUse, plane.Source}
		}
	}
	return claimRef{source: plane.Source}
}

func unionManagedIDs(left, right SystemManagedPlane) []string {
	ids := make(map[string]struct{})
	for _, entry := range left.Entries {
		ids[entry.ID] = struct{}{}
	}
	for _, entry := range right.Entries {
		ids[entry.ID] = struct{}{}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func managedClaim(plane SystemManagedPlane, id string) claimRef {
	for index := range plane.Entries {
		if plane.Entries[index].ID == id {
			return claimRef{plane.Entries[index].Allocated, plane.Source}
		}
	}
	return claimRef{source: plane.Source}
}

func compareHeldOpenClaim(left, right HeldOpenPlane, platformGate bool) PlaneDelta {
	if !platformGate {
		return compareClaim(
			"held_open",
			"logical_bytes",
			claimRef{left.LogicalBytes, left.Source},
			claimRef{right.LogicalBytes, right.Source},
			false,
			"platform does not provide a compatible identity gate",
		)
	}
	delta := compareClaim(
		"held_open",
		"logical_bytes",
		claimRef{left.LogicalBytes, left.Source},
		claimRef{right.LogicalBytes, right.Source},
		true,
		"",
	)
	if left.CollectionStatus == StatusNotRequested || right.CollectionStatus == StatusNotRequested {
		delta.DeltaStatus = "indeterminate"
		delta.DeltaBytes = nil
		delta.Detail = stringPointer("held-open collection was not requested on one or both inputs")
		return delta
	}
	if left.CollectionStatus != StatusMeasured || right.CollectionStatus != StatusMeasured {
		delta.DeltaStatus = "indeterminate"
		delta.DeltaBytes = nil
		delta.Detail = stringPointer("held-open collection is partial or unavailable on one or both inputs")
		return delta
	}
	if left.Privilege != right.Privilege {
		delta.DeltaStatus = "indeterminate"
		delta.DeltaBytes = nil
		delta.Detail = stringPointer("held-open privilege differs between inputs")
	}
	return delta
}

func planeNotes(planes Planes) []string {
	type planeState struct {
		name     string
		status   string
		coverage Coverage
	}
	states := []planeState{
		{"filesystem", planes.Filesystem.CollectionStatus, planes.Filesystem.Coverage},
		{"container", planes.Container.CollectionStatus, planes.Container.Coverage},
		{"volumes", planes.Volumes.CollectionStatus, planes.Volumes.Coverage},
		{"snapshots", planes.Snapshots.CollectionStatus, planes.Snapshots.Coverage},
		{"system_managed", planes.SystemManaged.CollectionStatus, planes.SystemManaged.Coverage},
		{"held_open", planes.HeldOpen.CollectionStatus, planes.HeldOpen.Coverage},
	}
	notes := make([]string, 0)
	for _, state := range states {
		if state.status != StatusMeasured {
			notes = append(notes, state.name+": "+state.status)
		}
		for _, gap := range state.coverage.Gaps {
			note := fmt.Sprintf("%s gap %s scope=%s", state.name, gap.Code, gap.Scope)
			if gap.OmittedObjects != nil {
				note += fmt.Sprintf(" omitted_objects=%d", *gap.OmittedObjects)
			}
			if gap.OmittedHolders != nil {
				note += fmt.Sprintf(" omitted_holders=%d", *gap.OmittedHolders)
			}
			notes = append(notes, note)
		}
	}
	return notes
}

func asymmetricPlanes(left, right Planes) []string {
	type pair struct {
		name        string
		leftStatus  string
		rightStatus string
	}
	pairs := []pair{
		{"filesystem", left.Filesystem.CollectionStatus, right.Filesystem.CollectionStatus},
		{"container", left.Container.CollectionStatus, right.Container.CollectionStatus},
		{"volumes", left.Volumes.CollectionStatus, right.Volumes.CollectionStatus},
		{"snapshots", left.Snapshots.CollectionStatus, right.Snapshots.CollectionStatus},
		{"system_managed", left.SystemManaged.CollectionStatus, right.SystemManaged.CollectionStatus},
		{"held_open", left.HeldOpen.CollectionStatus, right.HeldOpen.CollectionStatus},
	}
	result := make([]string, 0)
	for _, item := range pairs {
		if item.leftStatus != item.rightStatus {
			result = append(result, item.name)
		}
	}
	coveragePairs := []struct {
		name        string
		left, right Coverage
	}{
		{"filesystem", left.Filesystem.Coverage, right.Filesystem.Coverage},
		{"container", left.Container.Coverage, right.Container.Coverage},
		{"volumes", left.Volumes.Coverage, right.Volumes.Coverage},
		{"snapshots", left.Snapshots.Coverage, right.Snapshots.Coverage},
		{"system_managed", left.SystemManaged.Coverage, right.SystemManaged.Coverage},
		{"held_open", left.HeldOpen.Coverage, right.HeldOpen.Coverage},
	}
	for _, item := range coveragePairs {
		if !sameCoverageShape(item.left, item.right) && !containsString(result, item.name) {
			result = append(result, item.name)
		}
	}
	if left.HeldOpen.Privilege != right.HeldOpen.Privilege &&
		left.HeldOpen.Privilege != "" && right.HeldOpen.Privilege != "" &&
		!containsString(result, "held_open") {
		result = append(result, "held_open")
	}
	return result
}

func sameCoverageShape(left, right Coverage) bool {
	if len(left.Gaps) != len(right.Gaps) {
		return false
	}
	for index := range left.Gaps {
		leftGap, rightGap := left.Gaps[index], right.Gaps[index]
		if leftGap.Code != rightGap.Code || leftGap.Scope != rightGap.Scope ||
			optionalCount(leftGap.OmittedObjects) != optionalCount(rightGap.OmittedObjects) ||
			optionalCount(leftGap.OmittedHolders) != optionalCount(rightGap.OmittedHolders) {
			return false
		}
	}
	return true
}

func optionalCount(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
