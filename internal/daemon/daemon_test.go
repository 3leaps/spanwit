//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/observe"
)

func privateDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	p, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestOwnershipAndHandover(t *testing.T) {
	dir := privateDir(t)
	a, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second writer: %v", err)
	}
	s, err := a.Sample(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "observing" || s.RecordID == "" {
		t.Fatalf("sample: %+v", s)
	}
	if _, ok, err := a.stream.Head(s.RecordID); err != nil || !ok {
		t.Fatalf("not durable: %v %v", ok, err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, b)
	if _, ok, err := b.stream.Head(s.RecordID); err != nil || !ok {
		t.Fatalf("lost across handover: %v %v", ok, err)
	}
}
func TestUnsafeOwnership(t *testing.T) {
	t.Run("permissions", func(t *testing.T) {
		dir := privateDir(t)
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if o, err := Acquire(dir); err == nil {
			closeTest(t, o)
			t.Fatal("accepted public directory")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		dir := privateDir(t)
		target := filepath.Join(privateDir(t), "target")
		if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "coordinator.lock")); err != nil {
			t.Fatal(err)
		}
		if o, err := Acquire(dir); err == nil {
			closeTest(t, o)
			t.Fatal("accepted symlink")
		}
		b, _ := os.ReadFile(target)
		if string(b) != "keep" {
			t.Fatal("changed target")
		}
	})
	t.Run("partial-journal", func(t *testing.T) {
		dir := privateDir(t)
		if err := os.Mkdir(filepath.Join(dir, "journal"), 0700); err != nil {
			t.Fatal(err)
		}
		if c, err := Open(dir, nil); err == nil {
			closeTest(t, c)
			t.Fatal("reinitialized partial store")
		}
		o, err := Acquire(dir)
		if err != nil {
			t.Fatal("leaked ownership", err)
		}
		closeTest(t, o)
	})
}

type waitingSampler struct {
	entered chan struct{}
	release chan struct{}
}

func (s waitingSampler) Sample(ctx context.Context, _ string) (observe.Sample, error) {
	close(s.entered)
	select {
	case <-ctx.Done():
		return observe.Sample{}, ctx.Err()
	case <-s.release:
		return observe.Sample{}, errors.New("probe failure")
	}
}
func TestStatusDoesNotWaitForSampler(t *testing.T) {
	s := waitingSampler{make(chan struct{}), make(chan struct{})}
	c, err := Open(privateDir(t), s)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = c.Sample(context.Background(), "") }()
	<-s.entered
	status := make(chan Status, 1)
	go func() { status <- c.Status() }()
	select {
	case <-status:
	case <-time.After(time.Second):
		close(s.release)
		t.Fatal("status blocked on sampler")
	}
	close(s.release)
	<-done
	if c.Status().State != "sample_unavailable" {
		t.Fatal(c.Status())
	}
}
func TestHTTPReadSurface(t *testing.T) {
	c, err := Open(privateDir(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	for _, tc := range []struct {
		method, path string
		code         int
	}{{"GET", "/v1/status", 200}, {"GET", "/v1/capabilities", 200}, {"GET", "/v1/proposals", 200}, {"POST", "/v1/execute", 405}, {"GET", "/v2/status", 404}, {"GET", "/v1/status?path=/", 400}} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		Handler(c).ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.ContentLength = 1
	w := httptest.NewRecorder()
	Handler(c).ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestSocketRoundTripAndShutdown(t *testing.T) {
	// Keep the socket below Unix path limits on macOS.
	dir, err := os.MkdirTemp("/tmp", "spanwit-daemon-test-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dir, dir, time.Minute) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err = Read(context.Background(), dir, "capabilities")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = Open(dir, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("offline writer admitted: %v", err)
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	closeTest(t, c)
	if _, err = os.Lstat(filepath.Join(dir, SocketName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket left: %v", err)
	}
}
func TestStaleSocketRefused(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "spanwit-stale-test-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	}()
	path := filepath.Join(dir, SocketName)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	closeTest(t, l)
	if err = Run(context.Background(), dir, dir, time.Minute); err == nil {
		t.Fatal("stale endpoint accepted")
	}
	if _, err = os.Lstat(path); err != nil {
		t.Fatal("stale endpoint removed", err)
	}
}

func TestWritableAncestorRefused(t *testing.T) {
	parent := privateDir(t)
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	if c, err := Open(filepath.Join(parent, "state"), nil); err == nil {
		closeTest(t, c)
		t.Fatal("accepted non-sticky writable ancestor")
	}
	if _, err := os.Stat(filepath.Join(parent, "state", "journal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("journal created before ancestor admission")
	}
}

type transientSampler struct{ calls int }

func (s *transientSampler) Sample(ctx context.Context, path string) (observe.Sample, error) {
	s.calls++
	if s.calls == 1 {
		return observe.Sample{}, errors.New("transient")
	}
	return (observe.CapacitySampler{}).Sample(ctx, path)
}
func awaitState(t *testing.T, c *Coordinator, state string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if c.Status().State == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("state %s, want %s", c.Status().State, state)
}
func TestCoordinateRetriesSampleFailure(t *testing.T) {
	dir := privateDir(t)
	c, err := Open(dir, &transientSampler{})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() { done <- coordinate(ctx, c, dir, ticks, make(chan error)) }()
	awaitState(t, c, "sample_unavailable")
	ticks <- time.Now()
	awaitState(t, c, "observing")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestCoordinateJournalFailureSuspendsWrites(t *testing.T) {
	dir := privateDir(t)
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The injected store closure also makes final store cleanup return closed;
	// ownership still must be released by Coordinator.Close.
	defer func() { _ = c.Close() }()
	// An unavailable store is injected without changing the sampler or adapter.
	if err := c.store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() { done <- coordinate(ctx, c, dir, ticks, make(chan error)) }()
	awaitState(t, c, "journal_unavailable")
	ticks <- time.Now()
	w := httptest.NewRecorder()
	Handler(c).ServeHTTP(w, httptest.NewRequest("GET", "/v1/status", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if c.Status().State != "journal_unavailable" {
		t.Fatal(c.Status())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestCoordinateCancellationDuringSample(t *testing.T) {
	sampler := waitingSampler{make(chan struct{}), make(chan struct{})}
	c, err := Open(privateDir(t), sampler)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- coordinate(ctx, c, "", make(chan time.Time), make(chan error)) }()
	<-sampler.entered
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStateSymlinkRedirectRefused(t *testing.T) {
	parent := privateDir(t)
	victim := privateDir(t)
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(victim, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{alias, filepath.Join(alias, "new-state")} {
		if c, err := Open(path, nil); err == nil {
			closeTest(t, c)
			t.Fatal("accepted symlink", path)
		}
	}
	entries, err := os.ReadDir(victim)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("redirected writes into victim")
	}
}

func closeTest(t *testing.T, c interface{ Close() error }) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Error(err)
	}
}
