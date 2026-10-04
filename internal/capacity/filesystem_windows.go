//go:build windows

package capacity

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func sampleFilesystem(path string) (FilesystemSample, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return FilesystemSample{}, fmt.Errorf("path encode %s: %w", path, err)
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &available, &total, &free); err != nil {
		return FilesystemSample{}, fmt.Errorf("GetDiskFreeSpaceEx %s: %w", path, err)
	}
	used := int64(total) - int64(free)
	if used < 0 {
		used = int64(total) - int64(available)
	}
	root := volumeRoot(path)
	return FilesystemSample{
		Path:      path,
		Mount:     root,
		VolumeID:  windowsVolumeSerial(root),
		Total:     int64(total),
		Used:      used,
		Available: int64(available),
	}, nil
}

func volumeRoot(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	volume := filepath.VolumeName(abs)
	if strings.HasSuffix(volume, ":") {
		return volume + `\`
	}
	if volume != "" {
		return volume
	}
	return abs
}

func windowsVolumeSerial(root string) string {
	rootPtr, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return ""
	}
	var serial uint32
	if err := windows.GetVolumeInformation(rootPtr, nil, 0, &serial, nil, nil, nil, 0); err != nil {
		return ""
	}
	return fmt.Sprintf("volserial:%08x", serial)
}
