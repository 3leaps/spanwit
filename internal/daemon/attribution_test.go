//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/inventory"
	"golang.org/x/sys/unix"
)

func TestRootObservationRealWalk(t *testing.T) {
	root := privateDir(t)
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	o, err := newTestRootObserver(t, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := o.Observe(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Coverage != "complete" || rows[0].Files != 1 || rows[0].AllocatedBytes == nil || rows[0].Activity != "unknown" {
		t.Fatalf("%+v", rows)
	}
	old := rows[0]
	if err := os.WriteFile(filepath.Join(root, "second"), make([]byte, 8192), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err = o.Observe(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := RootDelta(old, rows[0]); !ok {
		t.Fatal("complete comparable roots rejected")
	}
	// Internal hard links are path entries; never label this unique allocation.
	if err := os.Link(filepath.Join(root, "second"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	rows, err = o.Observe(context.Background(), nil)
	if err != nil || rows[0].Files != 3 || rows[0].Basis != inventory.BasisAllocatedPathEntrySum {
		t.Fatalf("%+v %v", rows, err)
	}
}

func TestRootScopeAdmission(t *testing.T) {
	for _, roots := range [][]string{nil, {"relative"}, {"/a", "/a"}, {"/a", "/a/b"}, {"/a/b", "/a"}} {
		if _, err := newTestRootObserver(t, roots); err == nil {
			t.Fatalf("accepted %v", roots)
		}
	}
	if _, err := newTestRootObserver(t, []string{"/a", "/ab"}); err != nil {
		t.Fatal(err)
	}
	root := privateDir(t)
	alias := filepath.Join(privateDir(t), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	o, err := newTestRootObserver(t, []string{alias, filepath.Join(root, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := o.Observe(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Coverage != "unavailable" || row.AllocatedBytes != nil {
			t.Fatalf("%+v", row)
		}
	}
}

func TestRootObservationWithholdsIncompleteAndChangedIdentity(t *testing.T) {
	for _, kind := range []string{"gap", "remote", "unmeasured", "error", "changed", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			root := privateDir(t)
			o, err := newTestRootObserver(t, []string{root})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			o.run = func(_ context.Context, opts inventory.Options, sink inventory.Sink) (inventory.Summary, error) {
				if opts.Workers != 2 || opts.MaxPendingDirs != 4096 || !opts.OneFilesystem || opts.IncludeRemote {
					t.Fatal("unbounded walk")
				}
				id, err := rootIdentity(root)
				if err != nil {
					t.Fatal(err)
				}
				if err := sink.Header(inventory.Header{Roots: []inventory.Root{{DeviceID: id.device, Path: root}}}); err != nil {
					t.Fatal(err)
				}
				s := inventory.Summary{Lifecycle: inventory.LifecycleComplete, MatchedAllocatedBytes: 42}
				switch kind {
				case "gap":
					s.GapCount = 1
				case "remote":
					s.RemoteSkipCount = 1
				case "unmeasured":
					s.AllocatedUnmeasuredCount = 1
				case "error":
					return s, errors.New("failure")
				case "changed":
					if err := os.Rename(root, root+"-old"); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(root + "-old") })
					if err := os.Mkdir(root, 0700); err != nil {
						t.Fatal(err)
					}
				case "cancel":
					cancel()
				}
				return s, nil
			}
			rows, err := o.Observe(ctx, nil)
			if kind == "cancel" {
				if !errors.Is(err, context.Canceled) || rows != nil {
					t.Fatalf("%+v %v", rows, err)
				}
				return
			}
			if err != nil || rows[0].Coverage == "complete" || rows[0].AllocatedBytes != nil {
				t.Fatalf("%+v %v", rows, err)
			}
		})
	}
}

func TestRootScopeRejectsNormalizationAndEncodingDrift(t *testing.T) {
	root := privateDir(t)
	for _, name := range []string{"name", "name "} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(root, "name "), filepath.Join(root, "name ") + "/.", "/invalid-" + string([]byte{0xff})} {
		if _, err := newTestRootObserver(t, []string{path}); err == nil {
			t.Fatalf("accepted ambiguous path %q", path)
		}
	}
	var roots []string
	for i := 0; i < MaxAttributionRoots; i++ {
		roots = append(roots, "/"+strings.Repeat(string(rune('a'+i)), 10)+"/"+strings.Repeat("\x01", 900))
	}
	if _, err := newTestRootObserver(t, roots); err == nil {
		t.Fatal("accepted oversized escaped roots")
	}
}

func TestRootActivityDefersWalk(t *testing.T) {
	root := privateDir(t)
	o, err := newTestRootObserver(t, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	o.run = func(context.Context, inventory.Options, inventory.Sink) (inventory.Summary, error) {
		t.Fatal("walked active root")
		return inventory.Summary{}, nil
	}
	rows, err := o.Observe(context.Background(), map[string]bool{root: true})
	if err != nil || rows[0].Coverage != "deferred" || rows[0].Activity != "active" || rows[0].AllocatedBytes != nil {
		t.Fatalf("%+v %v", rows, err)
	}
}

func TestRootDeltaRequiresComparableEvidence(t *testing.T) {
	n := int64(12)
	a := RootObservation{Path: "/root", DeviceID: "1", FileID: "2", Identity: "generation", Basis: AttributionBasis, Coverage: "complete", AllocatedBytes: &n, CapturedAt: time.Unix(10, 0)}
	b := a
	b.CapturedAt = b.CapturedAt.Add(time.Second)
	for _, change := range []func(*RootObservation){
		func(r *RootObservation) { r.FileID = "3" }, func(r *RootObservation) { r.DeviceID = "3" },
		func(r *RootObservation) { r.Path = "/other" }, func(r *RootObservation) { r.Basis = "apparent" },
		func(r *RootObservation) { r.Coverage = "partial" }, func(r *RootObservation) { r.AllocatedBytes = nil },
		func(r *RootObservation) { r.CapturedAt = a.CapturedAt.Add(-time.Second) },
	} {
		altered := b
		change(&altered)
		if _, ok := RootDelta(a, altered); ok {
			t.Fatalf("accepted %+v", altered)
		}
	}
}

func newTestRootObserver(t *testing.T, roots []string) (*RootObserver, error) {
	t.Helper()
	o, err := NewRootObserver(roots)
	if err == nil {
		t.Cleanup(func() { closeTest(t, o) })
	}
	return o, err
}

func TestRootContinuityHandlesAndGenerations(t *testing.T) {
	root := privateDir(t)
	o, err := newTestRootObserver(t, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := o.Observe(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	old := rows[0]
	pin := o.pins[root].file
	if _, err := pin.Stat(); err != nil {
		t.Fatal("continuity handle not retained", err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := pin.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("continuity handle leaked", err)
	}
	next, err := newTestRootObserver(t, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = next.Observe(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if old.DeviceID != rows[0].DeviceID || old.FileID != rows[0].FileID {
		t.Fatal("fixture changed identity")
	}
	if old.Identity == rows[0].Identity {
		t.Fatal("reused observation generation")
	}
	if _, ok := RootDelta(old, rows[0]); ok {
		t.Fatal("compared observations without continuous handle")
	}
}

func TestRootPinRejectsFIFOReplacementWithoutBlocking(t *testing.T) {
	parent := privateDir(t)
	root := filepath.Join(parent, "subject")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := rootIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	o, err := newTestRootObserver(t, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(parent, "old")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(root, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := o.pinRoot(root, id); done <- err }()
	select {
	case err := <-done:
		if err == nil || len(o.pins) != 0 {
			t.Fatal("accepted FIFO or retained a handle")
		}
	case <-time.After(time.Second):
		// Release a regressed blocking reader before failing the test.
		if fd, err := unix.Open(root, unix.O_WRONLY|unix.O_NONBLOCK, 0); err == nil {
			_ = unix.Close(fd)
		}
		select {
		case err := <-done:
			t.Logf("blocked reader returned during cleanup: %v", err)
		case <-time.After(time.Second):
		}
		t.Fatal("directory continuity open blocked on FIFO")
	}
}
