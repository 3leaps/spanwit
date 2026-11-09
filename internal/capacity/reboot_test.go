package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

// Target container disk9 (Data disk9s5, VM-role disk9s4); disk7 is a second
// container with its own VM-role volume (apfs-list-multi.plist).
const (
	devRoot   = 100 // disk9s1s1 "/" and disk9s5 Data share st_dev (volume group)
	devVM9    = 101 // disk9s4
	devPre9   = 102 // disk9s2
	devVM7    = 201 // disk7s1
	devParent = 100 // /System/Volumes
)

type rebootFixture struct {
	platform   string
	swap       SwapUsageFunc
	devices    map[string]uint64
	mounts     []MountEntry
	mountsErr  error
	lsof       string // non-empty: --held-open requested with this output
	lsofErr    error
	noVolumes  bool // apfs list fails: volumes plane not measured
	heldOpenOn bool
}

func gib(n int64) SwapUsageFunc {
	return func(context.Context) (int64, error) { return n << 30, nil }
}

func liveTarget() rebootFixture {
	return rebootFixture{
		platform: "darwin",
		swap:     gib(6),
		devices:  map[string]uint64{darwinVMMount: devVM9, darwinVMMountParent: devParent},
		mounts: []MountEntry{
			{From: "/dev/disk9s1s1", On: "/", Dev: devRoot, StatOK: true},
			{From: "/dev/disk9s5", On: "/System/Volumes/Data", Dev: devRoot, StatOK: true},
			{From: "/dev/disk9s4", On: darwinVMMount, Dev: devVM9, StatOK: true},
			{From: "/dev/disk9s2", On: "/System/Volumes/Preboot", Dev: devPre9, StatOK: true},
		},
	}
}

func runReboot(t *testing.T, f rebootFixture) *RebootReleasable {
	t.Helper()
	runner := RunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch strings.Join(args[:len(args)-1], " ") {
		case "info -plist":
			return fixture(t, "info-apfs.plist"), nil
		case "apfs list":
			if f.noVolumes {
				return nil, &RunError{Code: GapCommandFailed}
			}
			return fixture(t, "apfs-list-multi.plist"), nil
		case "apfs listSnapshots -plist":
			return fixture(t, "snapshots.plist"), nil
		}
		t.Fatalf("unexpected argv %q", args)
		return nil, nil
	})
	c := Collector{
		Platform: f.platform,
		Now:      func() time.Time { return time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC) },
		FilesystemSampler: func(_ context.Context, path string) (FilesystemSample, error) {
			return testSample(path), nil
		},
		DarwinRunner: runner,
		ManagedRootMeasurer: func(context.Context, string, int) ManagedMeasurement {
			return ManagedMeasurement{Complete: true, Gaps: []CoverageGap{}}
		},
		SwapUsage: f.swap,
		DeviceOf: func(p string) (uint64, error) {
			if d, ok := f.devices[p]; ok {
				return d, nil
			}
			return 0, errors.New("stat failed")
		},
		MountTable: func() ([]MountEntry, error) { return f.mounts, f.mountsErr },
		HeldOpenRunner: RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
			return []byte(f.lsof), f.lsofErr
		}),
	}
	req := Request{
		TargetPath: "/target", AnalysisPath: "/target",
		Tool:     ToolIdentity{Name: "spanwit", Version: "test"},
		HeldOpen: f.heldOpenOn,
	}
	if f.heldOpenOn {
		req.Disclosure = DisclosureFull // strongest mode: the block must still be aggregate
	}
	res, err := c.Collect(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accounting.RebootReleasable == nil {
		t.Fatal("reboot_releasable missing")
	}
	return res.Accounting.RebootReleasable
}

func wantSwap(t *testing.T, r *RebootReleasable, status, reason, scope string, bytes *int64, block string) {
	t.Helper()
	s := r.Swap
	if s.Status != status || s.Reason != reason || s.Scope != scope {
		t.Fatalf("swap=%+v want %s/%q/%s", s, status, reason, scope)
	}
	switch {
	case bytes == nil && s.Bytes != nil:
		t.Fatalf("swap bytes must be absent: %+v", s.Bytes)
	case bytes != nil && (s.Bytes == nil || s.Bytes.Bytes == nil || *s.Bytes.Bytes != *bytes ||
		s.Bytes.Basis != BasisVMSwapUsageTotal || s.Bytes.Bound != BoundExact):
		t.Fatalf("swap bytes=%+v want %d on vm_swapusage_total", s.Bytes, *bytes)
	}
	if r.Status != block {
		t.Fatalf("block status=%s want %s", r.Status, block)
	}
	if r.Semantics != RebootSemanticsObservedGross || r.TempPlanes.Disposition != RebootTempMayBeCleared ||
		r.Purgeable.Status != StatusUnavailable || r.Purgeable.Reason != RebootReasonNoUnprivilegedSrc {
		t.Fatalf("consts drifted: %+v", r)
	}
}

func i64(n int64) *int64 { return &n }

func TestReboot_SwapInTargetContainerIsMeasured(t *testing.T) {
	r := runReboot(t, liveTarget())
	wantSwap(t, r, StatusMeasured, "", RebootScopeTargetContainer, i64(6<<30), StatusMeasured)
}

// The figure is the reservation total, never "used": the fake reports only
// a total, and the decoder reads xsu_total (first field).
func TestParseXswUsageTotal(t *testing.T) {
	raw := make([]byte, 32)
	total := uint64(6144) << 20
	used := uint64(4717) << 20
	for i := 0; i < 8; i++ {
		raw[i] = byte(total >> (8 * i))
		raw[16+i] = byte(used >> (8 * i))
	}
	got, err := parseXswUsageTotal(raw)
	if err != nil || got != int64(total) {
		t.Fatalf("total=%d err=%v", got, err)
	}
	if _, err := parseXswUsageTotal(raw[:20]); err == nil {
		t.Fatal("short buffer must fail")
	}
	over := make([]byte, 24)
	for i := 0; i < 8; i++ {
		over[i] = 0xff
	}
	if _, err := parseXswUsageTotal(over); err == nil {
		t.Fatal("overflow must fail, never clamp")
	}
}

func TestReboot_SwapZeroIsMeasuredZero(t *testing.T) {
	f := liveTarget()
	f.swap = gib(0)
	r := runReboot(t, f)
	wantSwap(t, r, StatusMeasured, "", RebootScopeTargetContainer, i64(0), StatusMeasured)
}

// devrev counterexample: live swap is in container A (disk7); the target's
// container B (disk9) has its own VM-role volume. Role presence must not
// attribute A's reservation to B.
func TestReboot_TwoContainersBothWithVMRole(t *testing.T) {
	f := liveTarget()
	f.devices[darwinVMMount] = devVM7
	f.mounts = []MountEntry{
		{From: "/dev/disk9s1s1", On: "/", Dev: devRoot, StatOK: true},
		{From: "/dev/disk9s5", On: "/System/Volumes/Data", Dev: devRoot, StatOK: true},
		{From: "/dev/disk9s4", On: "/Volumes/Ext/System/Volumes/VM", Dev: devVM9, StatOK: true},
		{From: "/dev/disk7s1", On: darwinVMMount, Dev: devVM7, StatOK: true},
	}
	r := runReboot(t, f)
	wantSwap(t, r, RebootSwapNotReleasable, RebootReasonNotBootContainer, RebootScopeOtherContainer, nil, StatusMeasured)
}

// The volumes plane carries no mount points on real hosts; binding must come
// from the mount table, never from an empty mount field read as "unmounted".
func TestReboot_VolumesWithoutMountPointsStillBind(t *testing.T) {
	r := runReboot(t, liveTarget()) // fixture volumes have no MountPoint
	if r.Swap.Scope != RebootScopeTargetContainer {
		t.Fatalf("scope=%s", r.Swap.Scope)
	}
}

func TestReboot_UnknownScopeWithholdsBytes(t *testing.T) {
	cases := map[string]func(*rebootFixture){
		"vm not a mount point": func(f *rebootFixture) { f.devices[darwinVMMount] = devParent },
		"vm stat fails":        func(f *rebootFixture) { delete(f.devices, darwinVMMount) },
		"mount table fails":    func(f *rebootFixture) { f.mountsErr = errors.New("getfsstat") },
		"volumes not measured": func(f *rebootFixture) { f.noVolumes = true; f.devices[darwinVMMount] = devVM7 },
		"target mount unstatted, vm elsewhere": func(f *rebootFixture) {
			f.devices[darwinVMMount] = devVM7
			f.mounts[3].StatOK = false
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			f := liveTarget()
			mut(&f)
			r := runReboot(t, f)
			wantSwap(t, r, StatusMeasured, "", RebootScopeUnknown, nil, StatusPartial)
		})
	}
}

func TestReboot_SwapProbeFailureIsUnavailableForEveryScope(t *testing.T) {
	failures := map[string]SwapUsageFunc{
		"error":    func(context.Context) (int64, error) { return 0, errors.New("sysctl") },
		"negative": func(context.Context) (int64, error) { return -1, nil },
		"nil":      nil,
	}
	scopes := map[string]func(*rebootFixture){
		RebootScopeTargetContainer: func(*rebootFixture) {},
		RebootScopeOtherContainer: func(f *rebootFixture) {
			f.devices[darwinVMMount] = devVM7
		},
		RebootScopeUnknown: func(f *rebootFixture) { f.mountsErr = errors.New("x") },
	}
	for fname, probe := range failures {
		for scope, mut := range scopes {
			t.Run(fname+"/"+scope, func(t *testing.T) {
				f := liveTarget()
				f.swap = probe
				mut(&f)
				r := runReboot(t, f)
				wantSwap(t, r, StatusUnavailable, RebootReasonProbeFailed, scope, nil, StatusPartial)
			})
		}
	}
}

func TestReboot_LinuxIsStaticAndComplete(t *testing.T) {
	r := deriveRebootReleasable(
		deriveSwap(context.Background(), "linux", gib(6), RebootScopeUnknown),
		deriveHeldOpen(true, HeldOpenPlane{CollectionStatus: StatusUnsupported}, nil), "linux")
	wantSwap(t, r, RebootSwapNotReleasable, RebootReasonStaticSwap, RebootScopeUnknown, nil, StatusMeasured)
	if r.HeldOpen.Status != StatusUnsupported {
		t.Fatalf("held=%+v", r.HeldOpen)
	}
}

func TestReboot_OtherPlatformsUnsupported(t *testing.T) {
	r := deriveRebootReleasable(deriveSwap(context.Background(), "windows", gib(6), RebootScopeUnknown),
		deriveHeldOpen(false, HeldOpenPlane{CollectionStatus: StatusNotRequested}, nil), "windows")
	wantSwap(t, r, StatusUnsupported, "", RebootScopeUnknown, nil, StatusUnsupported)
}

func lsofObj(fd, dev, ino, size, path string) string {
	return lsofSet("f"+fd, "D"+dev, "i"+ino, "k0", "s"+size, "n"+path)
}

func TestReboot_HeldOpen(t *testing.T) {
	target := "0x64" // devRoot (100)
	other := "0xc9"  // devVM7 (201), mounted: known other filesystem
	t.Run("not requested", func(t *testing.T) {
		r := runReboot(t, liveTarget())
		if r.HeldOpen.Status != StatusNotRequested || r.HeldOpen.LogicalBytes != nil ||
			r.HeldOpen.ScopeFilter != RebootScopeTargetContainer {
			t.Fatalf("held=%+v", r.HeldOpen)
		}
	})
	t.Run("scoped, deduplicated, other container dropped", func(t *testing.T) {
		f := liveTarget()
		f.mounts = append(f.mounts, MountEntry{From: "/dev/disk7s1", On: "/Volumes/X", Dev: devVM7, StatOK: true})
		f.heldOpenOn = true
		f.lsof = lsofSet("p10", "cCodex", "u501") +
			lsofObj("3", target, "1", "1000", "/a") +
			lsofObj("4", target, "2", "500", "/b") +
			lsofSet("p11", "cnode", "u501") +
			lsofObj("5", target, "1", "1000", "/a") + // same object, second holder
			lsofObj("6", other, "9", "7777", "/x")
		r := runReboot(t, f)
		h := r.HeldOpen
		if h.Status != StatusMeasured || h.LogicalBytes == nil || *h.LogicalBytes.Bytes != 1500 ||
			h.LogicalBytes.Basis != "lsof_file_size_dedup_device_inode" || h.LogicalBytes.Bound != BoundLower {
			t.Fatalf("held=%+v %+v", h, h.LogicalBytes)
		}
		if r.Status != StatusMeasured {
			t.Fatalf("block=%s", r.Status)
		}
	})
	t.Run("unmappable device is partial with those bytes excluded", func(t *testing.T) {
		f := liveTarget()
		f.heldOpenOn = true
		f.lsof = lsofSet("p10", "cCodex", "u501") + lsofObj("3", target, "1", "1000", "/a") + lsofObj("4", "0x999", "2", "500", "/b")
		r := runReboot(t, f)
		if r.HeldOpen.Status != StatusPartial || r.HeldOpen.LogicalBytes == nil || *r.HeldOpen.LogicalBytes.Bytes != 1000 || r.Status != StatusPartial {
			t.Fatalf("held=%+v block=%s", r.HeldOpen, r.Status)
		}
	})
	t.Run("collection failure is partial, never a measured zero", func(t *testing.T) {
		f := liveTarget()
		f.heldOpenOn = true
		f.lsofErr = &RunError{Code: GapTimeout}
		r := runReboot(t, f)
		if r.HeldOpen.Status != StatusPartial || r.HeldOpen.LogicalBytes != nil || r.Status != StatusPartial {
			t.Fatalf("held=%+v block=%s", r.HeldOpen, r.Status)
		}
	})
	t.Run("incomplete device map is partial with no figure", func(t *testing.T) {
		f := liveTarget()
		f.heldOpenOn = true
		f.mountsErr = errors.New("getfsstat")
		f.lsof = lsofSet("p10", "cCodex", "u501") + lsofObj("3", target, "1", "1000", "/a")
		r := runReboot(t, f)
		if r.HeldOpen.Status != StatusPartial || r.HeldOpen.LogicalBytes != nil {
			t.Fatalf("held=%+v", r.HeldOpen)
		}
	})
	t.Run("overflow is partial, never wrapped", func(t *testing.T) {
		s := scopeHeldOpen(heldOpenParseResult{files: []heldOpenFile{
			{device: target, inode: "1", size: math.MaxInt64},
			{device: target, inode: "2", size: 10},
		}}, targetDevices{devs: map[uint64]struct{}{devRoot: {}}, complete: true}, nil)
		if !s.overflow {
			t.Fatalf("scoped=%+v", s)
		}
		h := deriveHeldOpen(true, HeldOpenPlane{CollectionStatus: StatusMeasured, LogicalBytes: &ByteClaim{Bytes: i64(1)}}, &s)
		if h.Status != StatusPartial || h.LogicalBytes != nil {
			t.Fatalf("held=%+v", h)
		}
	})
}

// devrev sparse/shared counterexample: an unlinked open sparse file reads 1
// GiB logical with no data blocks. It appears only as a labelled logical
// claim and is never combined with swap into any total.
func TestReboot_SparseHeldOpenIsOnlyLogicalAndNeverSummed(t *testing.T) {
	f := liveTarget()
	f.heldOpenOn = true
	f.lsof = lsofSet("p10", "cqemu", "u501") + lsofObj("3", "0x64", "7", "1073741824", "/tmp/sparse.img")
	r := runReboot(t, f)
	if r.HeldOpen.LogicalBytes == nil || *r.HeldOpen.LogicalBytes.Bytes != 1<<30 || *r.Swap.Bytes.Bytes != 6<<30 {
		t.Fatalf("r=%+v", r)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"total"`, `"counted"`, `"bound":"exact","source":"lsof`, `7516192768`} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("block must not sum or relabel held-open (%s): %s", banned, raw)
		}
	}
}

// Zero paths and zero process names in every disclosure mode, including full
// path disclosure on the held-open plane.
func TestReboot_AggregateOnlyUnderFullDisclosure(t *testing.T) {
	f := liveTarget()
	f.heldOpenOn = true
	f.lsof = lsofSet("p10", "cHOSTILE-proc\x1b[31m", "u501") + lsofObj("3", "0x64", "1", "4096", "/Users/secret/HOSTILE-path.db")
	r := runReboot(t, f)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"HOSTILE", "/Users", darwinVMMount, "disk9", "disk7", "0x64"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("reboot_releasable leaked %q: %s", leak, raw)
		}
	}
}

func TestMountedFromVolume(t *testing.T) {
	for _, tc := range []struct {
		from, id string
		want     bool
	}{
		{"/dev/disk3s6", "disk3s6", true},
		{"/dev/disk3s1s1", "disk3s1", true},
		{"/dev/disk3s1s12", "disk3s1", true},
		{"/dev/disk3s10", "disk3s1", false},
		{"/dev/disk3s1sx", "disk3s1", false},
		{"/dev/disk3s1s", "disk3s1", false},
		{"map auto_home", "disk3s1", false},
	} {
		if got := mountedFromVolume(tc.from, tc.id); got != tc.want {
			t.Errorf("mountedFromVolume(%q,%q)=%v", tc.from, tc.id, got)
		}
	}
}
