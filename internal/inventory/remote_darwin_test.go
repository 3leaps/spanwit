//go:build darwin

package inventory

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
	"time"
)

type fakeInfo struct {
	dir bool
	st  *syscall.Stat_t
}

func (f fakeInfo) Name() string { return "x" }
func (f fakeInfo) Size() int64  { return 0 }
func (f fakeInfo) Mode() fs.FileMode {
	if f.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return f.st }

var _ os.FileInfo = fakeInfo{}

func TestIsDatalessDir_ReadsSFDatalessFlag(t *testing.T) {
	if !isDatalessDir(fakeInfo{dir: true, st: &syscall.Stat_t{Flags: sfDataless}}) {
		t.Fatal("dataless directory not detected")
	}
	if isDatalessDir(fakeInfo{dir: true, st: &syscall.Stat_t{}}) {
		t.Fatal("ordinary directory flagged dataless")
	}
	if isDatalessDir(fakeInfo{dir: false, st: &syscall.Stat_t{Flags: sfDataless}}) {
		t.Fatal("dataless regular file must not be treated as a directory placeholder")
	}
}
