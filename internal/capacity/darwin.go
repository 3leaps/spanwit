package capacity

import (
	"context"
	"fmt"
	"strings"
)

const diskutilPath = "/usr/sbin/diskutil"

type darwinInfo struct {
	DeviceID   string
	VolumeUUID string
	Container  string
	Mount      string
	FSType     string
}

type darwinContainer struct {
	ID       string
	Capacity int64
	Free     int64
	Volumes  []VolumeEntry
}

func collectDarwin(ctx context.Context, runner CommandRunner, sample *FilesystemSample, accounting *Accounting) {
	if ctx.Err() != nil {
		setDarwinUnavailable(&accounting.Planes, deadlineGap(
			"diskutil",
			"public platform queries were skipped because the capacity context budget was exhausted",
		), "diskutil.plist")
		return
	}
	infoTarget := sample.Mount
	if infoTarget == "" {
		infoTarget = sample.Path
	}
	infoRaw, err := runner.Run(ctx, diskutilPath, "info", "-plist", infoTarget)
	if err != nil {
		gap := gapForRunError(err, "diskutil.info")
		setDarwinUnavailable(&accounting.Planes, gap, "diskutil.info.plist")
		return
	}
	info, err := parseDarwinInfo(infoRaw)
	if err != nil {
		gap := CoverageGap{Code: "parse_failed", Scope: "diskutil.info", Detail: "diskutil info plist was missing required typed identity fields"}
		setDarwinUnavailable(&accounting.Planes, gap, "diskutil.info.plist")
		return
	}
	if info.Mount != "" {
		sample.Mount = info.Mount
	}
	if info.FSType != "" {
		sample.FSType = strings.ToLower(info.FSType)
	}
	accounting.Target.ContainerID = info.Container
	if !strings.EqualFold(info.FSType, "apfs") {
		return
	}

	listRaw, listErr := runner.Run(ctx, diskutilPath, "apfs", "list", "-plist")
	if listErr != nil {
		gap := gapForRunError(listErr, "diskutil.apfs.list")
		accounting.Planes.Container = unavailableContainer(gap, "diskutil.apfs.list.plist")
		accounting.Planes.Volumes = unavailableVolumes(gap, "diskutil.apfs.list.plist")
	} else {
		container, parseErr := parseAPFSList(listRaw, info.DeviceID)
		if parseErr != nil {
			gap := CoverageGap{Code: "parse_failed", Scope: "diskutil.apfs.list", Detail: "APFS list plist did not contain one typed target volume mapping"}
			accounting.Planes.Container = unavailableContainer(gap, "diskutil.apfs.list.plist")
			accounting.Planes.Volumes = unavailableVolumes(gap, "diskutil.apfs.list.plist")
		} else {
			accounting.Target.ContainerID = container.ID
			capacityClaim := measuredClaim(container.Capacity, "apfs_container_size", "exact")
			freeClaim := measuredClaim(container.Free, "apfs_container_free", "exact")
			accounting.Planes.Container = ContainerPlane{
				CollectionStatus: StatusMeasured,
				Source:           "diskutil.apfs.list.plist",
				Coverage:         emptyCoverage(),
				ID:               container.ID,
				Capacity:         &capacityClaim,
				Free:             &freeClaim,
			}
			accounting.Planes.Volumes = VolumesPlane{
				CollectionStatus: StatusMeasured,
				Source:           "diskutil.apfs.list.plist",
				Coverage:         emptyCoverage(),
				Entries:          container.Volumes,
			}
		}
	}

	snapshotRaw, snapshotErr := runner.Run(ctx, diskutilPath, "apfs", "listSnapshots", "-plist", info.DeviceID)
	if snapshotErr != nil {
		accounting.Planes.Snapshots = unavailableSnapshots(
			gapForRunError(snapshotErr, "diskutil.apfs.listSnapshots"),
			"diskutil.apfs.listSnapshots.plist",
		)
		return
	}
	entries, parseErr := parseSnapshots(snapshotRaw)
	if parseErr != nil {
		accounting.Planes.Snapshots = unavailableSnapshots(CoverageGap{
			Code:   "parse_failed",
			Scope:  "diskutil.apfs.listSnapshots",
			Detail: "snapshot plist was missing required typed identity fields",
		}, "diskutil.apfs.listSnapshots.plist")
		return
	}
	count := len(entries)
	bytesClaim := unsupportedClaim("darwin public APIs do not expose snapshot bytes")
	accounting.Planes.Snapshots = SnapshotsPlane{
		CollectionStatus: StatusPartial,
		Source:           "diskutil.apfs.listSnapshots.plist",
		Coverage:         emptyCoverage(),
		Count:            &count,
		Bytes:            &bytesClaim,
		Entries:          entries,
	}
}

func parseDarwinInfo(raw []byte) (darwinInfo, error) {
	object, err := parsePlist(raw)
	if err != nil {
		return darwinInfo{}, err
	}
	device, err := requiredString(object, "DeviceIdentifier")
	if err != nil {
		return darwinInfo{}, err
	}
	fsType, err := requiredString(object, "FilesystemType")
	if err != nil {
		return darwinInfo{}, err
	}
	info := darwinInfo{
		DeviceID:   device,
		FSType:     fsType,
		VolumeUUID: optionalString(object, "VolumeUUID", "APFSVolumeUUID"),
		Container:  optionalString(object, "APFSContainerReference", "ContainerReference"),
		Mount:      optionalString(object, "MountPoint"),
	}
	if strings.EqualFold(fsType, "apfs") && info.Container == "" {
		return darwinInfo{}, fmt.Errorf("APFS info missing container reference")
	}
	return info, nil
}

func parseAPFSList(raw []byte, targetDevice string) (darwinContainer, error) {
	object, err := parsePlist(raw)
	if err != nil {
		return darwinContainer{}, err
	}
	containers, err := requiredArray(object, "Containers")
	if err != nil {
		return darwinContainer{}, err
	}
	var matches []darwinContainer
	for _, item := range containers {
		containerObject, ok := item.(map[string]any)
		if !ok {
			return darwinContainer{}, fmt.Errorf("container entry has wrong type")
		}
		id, err := requiredString(containerObject, "ContainerReference")
		if err != nil {
			return darwinContainer{}, err
		}
		capacityBytes, err := requiredInt(containerObject, "CapacityCeiling")
		if err != nil {
			return darwinContainer{}, err
		}
		freeBytes, err := requiredInt(containerObject, "CapacityFree")
		if err != nil {
			return darwinContainer{}, err
		}
		volumeItems, err := requiredArray(containerObject, "Volumes")
		if err != nil {
			return darwinContainer{}, err
		}
		container := darwinContainer{ID: id, Capacity: capacityBytes, Free: freeBytes, Volumes: []VolumeEntry{}}
		targets := 0
		for _, volumeItem := range volumeItems {
			volumeObject, ok := volumeItem.(map[string]any)
			if !ok {
				return darwinContainer{}, fmt.Errorf("volume entry has wrong type")
			}
			device, err := requiredString(volumeObject, "DeviceIdentifier")
			if err != nil {
				return darwinContainer{}, err
			}
			inUse, err := requiredInt(volumeObject, "CapacityInUse")
			if err != nil {
				return darwinContainer{}, err
			}
			roles, err := optionalStrings(volumeObject, "Roles")
			if err != nil {
				return darwinContainer{}, err
			}
			claim := measuredClaim(inUse, "CapacityInUse", "exact")
			isTarget := device == targetDevice
			if isTarget {
				targets++
			}
			role := ""
			if len(roles) > 0 {
				role = roles[0]
			}
			container.Volumes = append(container.Volumes, VolumeEntry{
				ID:            device,
				Name:          optionalString(volumeObject, "Name", "VolumeName"),
				Role:          role,
				Roles:         roles,
				Mount:         optionalString(volumeObject, "MountPoint"),
				CapacityInUse: &claim,
				IsTarget:      isTarget,
			})
		}
		if targets == 1 {
			matches = append(matches, container)
		} else if targets > 1 {
			return darwinContainer{}, fmt.Errorf("target volume is ambiguous")
		}
	}
	if len(matches) != 1 {
		return darwinContainer{}, fmt.Errorf("target volume mapping count=%d", len(matches))
	}
	return matches[0], nil
}

func parseSnapshots(raw []byte) ([]SnapshotEntry, error) {
	object, err := parsePlist(raw)
	if err != nil {
		return nil, err
	}
	items, err := requiredArray(object, "Snapshots")
	if err != nil {
		return nil, err
	}
	entries := make([]SnapshotEntry, 0, len(items))
	for _, item := range items {
		snapshot, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("snapshot entry has wrong type")
		}
		id, err := requiredStringAny(snapshot, "SnapshotUUID", "APFSSnapshotUUID")
		if err != nil {
			return nil, err
		}
		purgeable, err := optionalBool(snapshot, "Purgeable", "IsPurgeable")
		if err != nil {
			return nil, err
		}
		limitsShrink, err := optionalBool(snapshot, "LimitsContainerShrink")
		if err != nil {
			return nil, err
		}
		entries = append(entries, SnapshotEntry{
			ID:                    id,
			Name:                  optionalString(snapshot, "Name", "SnapshotName"),
			Purgeable:             purgeable,
			LimitsContainerShrink: limitsShrink,
		})
	}
	return entries, nil
}

func setDarwinUnavailable(planes *Planes, gap CoverageGap, source string) {
	planes.Container = unavailableContainer(gap, source)
	planes.Volumes = unavailableVolumes(gap, source)
	planes.Snapshots = unavailableSnapshots(gap, source)
}

func unavailableContainer(gap CoverageGap, source string) ContainerPlane {
	return ContainerPlane{
		CollectionStatus: StatusUnavailable,
		Source:           source,
		Coverage:         Coverage{Gaps: []CoverageGap{gap}},
	}
}

func unavailableVolumes(gap CoverageGap, source string) VolumesPlane {
	return VolumesPlane{
		CollectionStatus: StatusUnavailable,
		Source:           source,
		Coverage:         Coverage{Gaps: []CoverageGap{gap}},
	}
}

func unavailableSnapshots(gap CoverageGap, source string) SnapshotsPlane {
	return SnapshotsPlane{
		CollectionStatus: StatusUnavailable,
		Source:           source,
		Coverage:         Coverage{Gaps: []CoverageGap{gap}},
	}
}

func requiredString(object map[string]any, key string) (string, error) {
	value, ok := object[key]
	if !ok {
		return "", fmt.Errorf("missing %s", key)
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", key)
	}
	return text, nil
}

func requiredStringAny(object map[string]any, keys ...string) (string, error) {
	for _, key := range keys {
		if _, ok := object[key]; ok {
			return requiredString(object, key)
		}
	}
	return "", fmt.Errorf("missing required identity field")
}

func optionalString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok {
			return value
		}
	}
	return ""
}

func requiredInt(object map[string]any, key string) (int64, error) {
	value, ok := object[key]
	if !ok {
		return 0, fmt.Errorf("missing %s", key)
	}
	number, ok := value.(int64)
	if !ok || number < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return number, nil
}

func requiredArray(object map[string]any, key string) ([]any, error) {
	value, ok := object[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	array, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	return array, nil
}

func optionalStrings(object map[string]any, key string) ([]string, error) {
	value, ok := object[key]
	if !ok {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s entries must be strings", key)
		}
		result = append(result, text)
	}
	return result, nil
}

func optionalBool(object map[string]any, keys ...string) (*bool, error) {
	for _, key := range keys {
		value, ok := object[key]
		if !ok {
			continue
		}
		boolean, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("%s must be a boolean", key)
		}
		return &boolean, nil
	}
	return nil, nil
}
