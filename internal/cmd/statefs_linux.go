//go:build linux

package cmd

import (
	"fmt"
	"syscall"
)

// Filesystem magic numbers (statfs f_type) from linux/magic.h.
var linuxFSNames = map[int64]string{
	0xEF53:     "ext4",
	0x58465342: "xfs",
	0x9123683E: "btrfs",
	0x01021994: "tmpfs",
	0x794C7630: "overlay",
	0x6969:     "nfs",
	0x2FC12FC1: "zfs",
	0xFF534D42: "cifs",
	0x65735546: "fuse",
	0x4d44:     "vfat",
}

// stateFSType names the filesystem holding path and reports whether the
// journal store accepts it (recordprims: ext4, xfs, btrfs).
func stateFSType(path string) (string, bool, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", false, err
	}
	magic := int64(st.Type)
	name, ok := linuxFSNames[magic]
	if !ok {
		name = fmt.Sprintf("unknown (0x%x)", magic)
	}
	return name, name == "ext4" || name == "xfs" || name == "btrfs", nil
}
