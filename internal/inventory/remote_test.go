//go:build unix

package inventory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsRemoteFSType(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"nfs", true}, {"NFS", true}, {"smbfs", true}, {"afpfs", true}, {"webdav", true},
		{"macfuse", true}, {"fuse.sshfs", true}, {"fuseblk", true},
		{"apfs", false}, {"hfs", false}, {"ext4", false}, {"local", false}, {"", false},
	} {
		if got := isRemoteFSType(tc.in); got != tc.want {
			t.Errorf("isRemoteFSType(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}

// Same device is never remote; a different device is classified once by fs
// type and cached, so a walk issues at most one statfs per device.
func TestRemoteClassifier_DeviceChangeUsesCachedFSType(t *testing.T) {
	dir := t.TempDir()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c := newRemoteClassifier()
	c.fsType = func(string) (string, bool) { calls++; return "nfs", true }

	if got := c.remoteKind(dir, info, "dev-1", "dev-1"); got != "" {
		t.Fatalf("same device classified %q", got)
	}
	if calls != 0 {
		t.Fatalf("same device must not statfs (calls=%d)", calls)
	}
	for i := 0; i < 3; i++ {
		if got := c.remoteKind(dir, info, "dev-2", "dev-1"); got != remoteKindNetwork {
			t.Fatalf("nfs device classified %q", got)
		}
	}
	if calls != 1 {
		t.Fatalf("statfs per device must be cached: calls=%d", calls)
	}
	c.fsType = func(string) (string, bool) { return "apfs", true }
	if got := c.remoteKind(dir, info, "dev-3", "dev-1"); got != "" {
		t.Fatalf("local device classified %q", got)
	}
}

// A placeholder directory below the root is skipped before any open, as a
// non-completeness-affecting remote gap; --include-remote walks it; a named
// placeholder root is always walked.
func TestRun_PlaceholderDirectorySkippedByDefault(t *testing.T) {
	root := t.TempDir()
	cloud := filepath.Join(root, "OneDrive-Cloud")
	mustWrite(t, filepath.Join(cloud, "big"), 5000)
	mustWrite(t, filepath.Join(root, "local", "f"), 10)
	want, err := os.Stat(cloud)
	if err != nil {
		t.Fatal(err)
	}
	orig := datalessDir
	datalessDir = func(info os.FileInfo) bool { return os.SameFile(info, want) }
	t.Cleanup(func() { datalessDir = orig })
	origOpen := openDir
	openDir = func(p string) (*os.File, error) {
		if got, statErr := os.Stat(p); statErr == nil && os.SameFile(got, want) {
			t.Errorf("placeholder directory was opened: %s", p)
		}
		return os.Open(p)
	}
	t.Cleanup(func() { openDir = origOpen })

	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{Roots: []string{root}, Backend: BackendSerial, Workers: 1}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Lifecycle != LifecycleComplete || summary.RemoteSkipCount != 1 || summary.MatchedApparentBytes != 10 {
		t.Fatalf("default: summary=%+v", summary)
	}
	var remote int
	for _, g := range sink.gaps {
		if g.Kind == gapKindRemote {
			remote++
			if g.AffectsCompleteness || !strings.Contains(g.Detail, "--include-remote") {
				t.Fatalf("remote gap=%+v", g)
			}
		}
	}
	if remote != 1 {
		t.Fatalf("remote gaps=%d", remote)
	}

	openDir = origOpen
	summary, err = Run(context.Background(), Options{Roots: []string{root}, Backend: BackendSerial, Workers: 1, IncludeRemote: true}, &memorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.RemoteSkipCount != 0 || summary.MatchedApparentBytes != 5010 {
		t.Fatalf("include-remote: summary=%+v", summary)
	}

	summary, err = Run(context.Background(), Options{Roots: []string{cloud}, Backend: BackendSerial, Workers: 1}, &memorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.RemoteSkipCount != 0 || summary.MatchedApparentBytes != 5000 {
		t.Fatalf("named placeholder root must be walked: summary=%+v", summary)
	}
}
