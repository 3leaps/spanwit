package inventory

import (
	"os"
	"strings"
	"sync"
)

// Remote and cloud-placeholder directories are a distinct coverage class. A
// read-only walk is not side-effect-free there: opening a network-filesystem or
// File Provider directory can download content or block on the network. They
// are skipped by default (a non-completeness-affecting policy gap, like a mount
// boundary) and walked only with Options.IncludeRemote. An explicitly named
// root is always walked; naming it is the opt-in.
const (
	gapKindRemote  = "remote"
	gapKindStalled = "stalled"

	remoteKindNetwork     = "network-filesystem"
	remoteKindPlaceholder = "cloud-placeholder"
)

// remoteFSTypes are filesystem type names treated as network/object-store
// backed. Matching is exact, except that any "fuse" prefix counts.
var remoteFSTypes = map[string]bool{
	"nfs": true, "nfs4": true, "smbfs": true, "cifs": true, "smb2": true,
	"afpfs": true, "webdav": true, "macfuse": true, "osxfuse": true,
}

func isRemoteFSType(fsType string) bool {
	t := strings.ToLower(fsType)
	return remoteFSTypes[t] || strings.HasPrefix(t, "fuse")
}

// remoteClassifier caches filesystem type per device so a walk issues at most
// one statfs per distinct device.
type remoteClassifier struct {
	mu     sync.Mutex
	byDev  map[string]bool
	fsType func(path string) (string, bool)
}

var remoteFilesystemType = fsTypeOf

func newRemoteClassifier() *remoteClassifier {
	return &remoteClassifier{byDev: map[string]bool{}, fsType: remoteFilesystemType}
}

// remoteKind reports why a child directory must be skipped by default, or ""
// to walk it. It inspects only metadata already read by lstat plus, on a
// device change, one cached statfs; it never opens the directory.
func (c *remoteClassifier) remoteKind(path string, info os.FileInfo, deviceID, rootDeviceID string) string {
	if datalessDir(info) {
		return remoteKindPlaceholder
	}
	if deviceID == "" || deviceID == rootDeviceID {
		return ""
	}
	c.mu.Lock()
	remote, seen := c.byDev[deviceID]
	c.mu.Unlock()
	if !seen {
		fsType, ok := c.fsType(path)
		remote = ok && isRemoteFSType(fsType)
		c.mu.Lock()
		c.byDev[deviceID] = remote
		c.mu.Unlock()
	}
	if remote {
		return remoteKindNetwork
	}
	return ""
}

// datalessDir is the placeholder check used by the walker (a seam for tests;
// a real SF_DATALESS directory cannot be created without a File Provider).
var datalessDir = isDatalessDir

// RemoteRootKind reports whether an explicitly named root is itself remote or a
// cloud placeholder, so a caller can tell the operator the root was walked by
// explicit choice. It returns "" for an ordinary local root.
func RemoteRootKind(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return ""
	}
	if isDatalessDir(info) {
		return remoteKindPlaceholder
	}
	if fsType, ok := fsTypeOf(path); ok && isRemoteFSType(fsType) {
		return remoteKindNetwork
	}
	return ""
}
