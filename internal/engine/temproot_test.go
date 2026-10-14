package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// privateDir makes a directory with the given mode under a private parent,
// so the parent-safety check passes and only the root's own mode is tested.
func privateDir(t *testing.T, mode os.FileMode) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "p")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(parent, "root")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, mode); err != nil {
		t.Fatal(err)
	}
	return d
}

func skipUnlessUnixTemp(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("temp roots are darwin and linux only")
	}
	if os.Geteuid() == 0 {
		t.Skip("euid 0 never admits a temp root")
	}
}

func TestAdmitTempRoot_ExactlyOwnerOnly(t *testing.T) {
	skipUnlessUnixTemp(t)
	cases := map[os.FileMode]string{
		0o700:                 "",
		0o755:                 TempRootMode,
		0o750:                 TempRootMode,
		0o775:                 TempRootGroupOtherWritable,
		0o777:                 TempRootGroupOtherWritable,
		0o757:                 TempRootGroupOtherWritable,
		0o700 | os.ModeSticky: TempRootSticky,
		0o777 | os.ModeSticky: TempRootSticky,
	}
	for mode, want := range cases {
		d := privateDir(t, mode)
		got := AdmitTempRoot(d, "")
		if want == "" {
			if !got.Admitted {
				t.Errorf("mode %v: want admitted, got %+v", mode, got)
			}
		} else if got.Admitted || got.Reason != want {
			t.Errorf("mode %v: want refused %q, got %+v", mode, want, got)
		}
	}
}

func TestAdmitTempRoot_SharedPathsAndAncestors(t *testing.T) {
	skipUnlessUnixTemp(t)
	for _, p := range []string{"/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp",
		"/tmp/mine", "/private/tmp/x/y", "/", "/var", "/private", "/private/var"} {
		if got := AdmitTempRoot(p, ""); got.Admitted || got.Reason != TempRootSharedPath {
			t.Errorf("%s: want shared_path, got %+v", p, got)
		}
	}
}

func TestAdmitTempRoot_SymlinkRefused(t *testing.T) {
	skipUnlessUnixTemp(t)
	real := privateDir(t, 0o700)
	link := filepath.Join(filepath.Dir(real), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got := AdmitTempRoot(link, ""); got.Admitted || got.Reason != TempRootSymlink {
		t.Fatalf("symlinked root: want symlink, got %+v", got)
	}
	// A symlink component above the root (not a trusted OS alias).
	via := filepath.Join(t.TempDir(), "via")
	if err := os.Symlink(filepath.Dir(real), via); err != nil {
		t.Fatal(err)
	}
	if got := AdmitTempRoot(filepath.Join(via, "root"), ""); got.Admitted || got.Reason != TempRootSymlink {
		t.Fatalf("symlink component: want symlink, got %+v", got)
	}
}

func TestAdmitTempRoot_ParentMustNotBeWritable(t *testing.T) {
	skipUnlessUnixTemp(t)
	parent := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(parent, "root")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if got := AdmitTempRoot(d, ""); got.Admitted || got.Reason != TempRootGroupOtherWritable {
		t.Fatalf("writable parent: want refused, got %+v", got)
	}
}

func TestAdmitTempRoot_OverlapWithAnalysisRoot(t *testing.T) {
	skipUnlessUnixTemp(t)
	d := privateDir(t, 0o700)
	inner := filepath.Join(d, "inner")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, analysis := range map[string]string{"equal": d, "analysis inside": inner, "analysis above": filepath.Dir(d)} {
		if got := AdmitTempRoot(d, analysis); got.Admitted || got.Reason != TempRootOverlapsAnalysis {
			t.Errorf("%s: want overlaps_analysis_root, got %+v", name, got)
		}
	}
}

// fakeInfo lets the pure admission function see an owner and mode a normal
// user cannot create (another uid).
type fakeInfo struct {
	mode os.FileMode
}

func (f fakeInfo) Name() string       { return "root" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

func TestAdmitTempRoot_EuidRootAndPlatform(t *testing.T) {
	ok := func(string) (os.FileInfo, error) { return fakeInfo{os.ModeDir | 0o700}, nil }
	if got := admitTempRoot("/home/u/tmp", "", "linux", 0, 0, ok); got.Admitted || got.Reason != TempRootEuidRoot {
		t.Fatalf("euid 0: want euid_root, got %+v", got)
	}
	if got := admitTempRoot("C:/Temp", "", "windows", 1000, 1000, ok); got.Admitted || got.Reason != TempRootUnsupported {
		t.Fatalf("windows: want unsupported_platform, got %+v", got)
	}
}

// With no Stat_t owner available (another uid cannot be fabricated as a
// normal user), ownership is unknown and admission refuses.
func TestAdmitTempRoot_UnknownOwnerRefused(t *testing.T) {
	skipUnlessUnixTemp(t)
	d := privateDir(t, 0o700)
	canon := canonicalTempRoot(d, runtime.GOOS)
	lstat := func(p string) (os.FileInfo, error) {
		if p == canon {
			return fakeInfo{os.ModeDir | 0o700}, nil
		}
		return os.Lstat(p)
	}
	if got := admitTempRoot(d, "", runtime.GOOS, os.Geteuid(), os.Getuid(), lstat); got.Admitted || got.Reason != TempRootNotOwned {
		t.Fatalf("unknown owner: want not_owned, got %+v", got)
	}
}

func TestAdmitTempRoot_CanonicalDarwinAlias(t *testing.T) {
	if got := canonicalTempRoot("/var/folders/x/T", "darwin"); got != "/private/var/folders/x/T" {
		t.Fatalf("darwin /var alias: got %q", got)
	}
	if got := canonicalTempRoot("/var/folders/x/T", "linux"); got != "/var/folders/x/T" {
		t.Fatalf("linux leaves /var: got %q", got)
	}
}

// A carried temp root must be the user's temp dir recomputed now: another
// private, owned 0700 directory is refused as not_user_temp before admission.
func TestReadmitCarriedTempRoot_MustBeUserTemp(t *testing.T) {
	skipUnlessUnixTemp(t)
	user := privateDir(t, 0o700)
	other := privateDir(t, 0o700)
	if got := readmitCarriedTempRoot(other, "", user, runtime.GOOS); got.Admitted || got.Reason != TempRootNotUserTemp {
		t.Fatalf("another private dir must be refused as not_user_temp, got %+v", got)
	}
	if got := readmitCarriedTempRoot(user, "", user, runtime.GOOS); !got.Admitted {
		t.Fatalf("the user temp dir should be re-admitted, got %+v", got)
	}
}
