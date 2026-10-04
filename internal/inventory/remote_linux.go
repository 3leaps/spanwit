//go:build linux

package inventory

import (
	"os"

	"golang.org/x/sys/unix"
)

func isDatalessDir(os.FileInfo) bool { return false }

// fsTypeOf maps statfs magic numbers for network and FUSE filesystems to the
// names isRemoteFSType understands; local types return "local".
func fsTypeOf(path string) (string, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "", false
	}
	switch uint32(st.Type) {
	case unix.NFS_SUPER_MAGIC:
		return "nfs", true
	case unix.SMB_SUPER_MAGIC:
		return "smbfs", true
	case 0xFF534D42: // CIFS_MAGIC_NUMBER
		return "cifs", true
	case 0xFE534D42: // SMB2_MAGIC_NUMBER
		return "smb2", true
	case unix.FUSE_SUPER_MAGIC:
		return "fuse", true
	}
	return "local", true
}
