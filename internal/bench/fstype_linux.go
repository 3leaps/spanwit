//go:build linux

package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// linuxFSMagic maps the statfs magic numbers this harness expects to meet.
// Anything unmapped is reported by its hexadecimal magic rather than guessed
// at, so a reader can identify it and the row still states a filesystem.
var linuxFSMagic = map[int64]string{
	0xEF53:     "ext4",
	0x58465342: "xfs",
	0x9123683E: "btrfs",
	0x01021994: "tmpfs",
	0x2FC12FC1: "zfs",
	0x6969:     "nfs",
	0xFF534D42: "cifs",
	0x65735546: "fuse",
	0x794C7630: "overlayfs",
	0x4D44:     "vfat",
	0x137F:     "minix",
	0x1CD1:     "devpts",
}

// filesystemType reads the filesystem for path, mapping the statfs magic to a
// name where known.
func filesystemType(path string) (string, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "", false
	}
	magic := int64(st.Type)
	if name, ok := linuxFSMagic[magic]; ok {
		return name, true
	}
	return fmt.Sprintf("magic:0x%x", magic), true
}

// deviceClass reads the rotational flag for the block device backing path.
//
// It resolves the device from the file's st_dev, then consults sysfs, walking
// from a partition up to its parent device when the partition itself carries
// no queue directory. Any step that fails returns false rather than a guess:
// a declared value from an operator who knows the hardware beats an inferred
// one from a harness that does not.
func deviceClass(path string) (DeviceClass, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	dev := uint64(st.Dev)
	major, minor := unix.Major(dev), unix.Minor(dev)

	// A device number of 0:x indicates a virtual filesystem with no block
	// device behind it, such as tmpfs or overlayfs.
	if major == 0 {
		return DeviceUnknown, true
	}

	sysPath := fmt.Sprintf("/sys/dev/block/%d:%d", major, minor)
	for _, candidate := range []string{sysPath, filepath.Join(sysPath, "..")} {
		data, err := os.ReadFile(filepath.Join(candidate, "queue", "rotational"))
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(data)) {
		case "0":
			return DeviceSSD, true
		case "1":
			return DeviceRotational, true
		}
	}
	return "", false
}
