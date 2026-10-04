//go:build unix

package inventory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

// A FIFO open blocks until a writer appears: a real kernel-blocked open.
func TestOpenDirWithDeadline_AbandonsBlockedOpenAndClosesLateFD(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "blocking")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	var late *os.File
	var lateMu sync.Mutex
	orig := openDir
	openDir = func(p string) (*os.File, error) {
		f, err := os.Open(p)
		lateMu.Lock()
		late = f
		lateMu.Unlock()
		return f, err
	}
	t.Cleanup(func() { openDir = orig })

	start := time.Now()
	f, stalled, err := openDirWithDeadline(fifo, 100*time.Millisecond, 0, nil)
	if !stalled || f != nil || err != nil {
		t.Fatalf("want stalled abandon, got f=%v stalled=%v err=%v", f, stalled, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("abandon took %s", elapsed)
	}

	// Unblock the helper; the descriptor it finally gets must be closed.
	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		lateMu.Lock()
		got := late
		lateMu.Unlock()
		if got != nil {
			if _, statErr := got.Stat(); !errors.Is(statErr, os.ErrClosed) {
				if time.Now().After(deadline) {
					t.Fatalf("late descriptor left open (stat err=%v)", statErr)
				}
			} else {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never completed its open")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOpenDirWithDeadline_FastOpenIsNotStalled(t *testing.T) {
	f, stalled, err := openDirWithDeadline(t.TempDir(), time.Second, 0, nil)
	if err != nil || stalled || f == nil {
		t.Fatalf("f=%v stalled=%v err=%v", f, stalled, err)
	}
	_ = f.Close()
}

// blockOpen makes opens of one directory hang until release is closed.
func blockOpen(t *testing.T, target string) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	want, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	orig := openDir
	openDir = func(p string) (*os.File, error) {
		// Match by identity: the walker opens the normalized (symlink-
		// resolved) path, not the string the test created.
		if got, statErr := os.Stat(p); statErr == nil && os.SameFile(got, want) {
			hits.Add(1)
			<-gate
		}
		return os.Open(p)
	}
	t.Cleanup(func() {
		if hits.Load() == 0 {
			t.Errorf("blockOpen never intercepted %s; test would be vacuous", target)
		}
	})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() { release(); openDir = orig })
	return release
}

// The walk finishes despite a directory whose open never returns: that subtree
// becomes one path-free stalled gap, the run is partial, siblings are walked,
// and the operator gets exactly one coalesced, path-free alert.
func TestRun_StalledDirectoryIsAbandonedPathFreeAndPartial(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "session-7f3a9c2e-secret")
	mustWrite(t, filepath.Join(secret, "inner", "f"), 10)
	mustWrite(t, filepath.Join(root, "sibling", "g"), 20)
	blockOpen(t, secret)

	var alerts atomic.Int32
	var alertText atomic.Value
	for _, backend := range []string{BackendSerial, BackendParallel} {
		alerts.Store(0)
		sink := &memorySink{}
		done := make(chan struct{})
		var summary Summary
		var runErr error
		go func() {
			defer close(done)
			summary, runErr = Run(context.Background(), Options{
				Roots: []string{root}, Backend: backend, Workers: workersFor(backend),
				StallTimeout: 150 * time.Millisecond, stallAlertAfter: 30 * time.Millisecond,
				OnStall: func(a StallAlert) {
					alerts.Add(1)
					alertText.Store(a)
				},
			}, sink)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: walk hung on a blocked directory open", backend)
		}
		if runErr != nil {
			t.Fatal(runErr)
		}
		if summary.Lifecycle != LifecyclePartial || summary.StalledCount != 1 {
			t.Fatalf("%s: summary=%+v", backend, summary)
		}
		if summary.MatchedCount != 1 {
			t.Fatalf("%s: sibling not walked: matched=%d", backend, summary.MatchedCount)
		}
		var stalledGaps int
		for _, g := range sink.gaps {
			if g.Kind != gapKindStalled {
				continue
			}
			stalledGaps++
			if g.RelativePath != "" || g.LocalAbsolutePath != "" || strings.Contains(g.Detail, "session-7f3a9c2e") {
				t.Fatalf("%s: stalled gap discloses a path: %+v", backend, g)
			}
			if !g.AffectsCompleteness {
				t.Fatalf("%s: stalled gap must affect completeness", backend)
			}
		}
		if stalledGaps != 1 {
			t.Fatalf("%s: stalled gaps=%d", backend, stalledGaps)
		}
		if n := alerts.Load(); n != 1 {
			t.Fatalf("%s: alerts=%d want exactly 1", backend, n)
		}
		if a, _ := alertText.Load().(StallAlert); a.Pending < 1 || a.Timeout != 150*time.Millisecond {
			t.Fatalf("%s: alert=%+v", backend, a)
		}
	}
}

// Directory aggregates must not claim a stalled subtree is complete: its
// ancestors turn partial while an unrelated sibling stays complete.
func TestRun_StalledSubtreeMakesOnlyAncestorsPartial(t *testing.T) {
	root := t.TempDir()
	stuck := filepath.Join(root, "a", "stuck")
	mustWrite(t, filepath.Join(stuck, "f"), 10)
	mustWrite(t, filepath.Join(root, "b", "g"), 20)
	blockOpen(t, stuck)

	sink := &memorySink{}
	_, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		EmissionMode: EmissionDirectorySummary, DirectoryDepth: -1, MaxAggregateDirectories: 1000,
		StallTimeout: 100 * time.Millisecond,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	life := map[string]string{}
	for _, d := range sink.directories {
		life[filepath.ToSlash(d.RelativePath)] = d.Lifecycle
	}
	rootLife, ok := life["."]
	if !ok {
		rootLife = life[""]
	}
	if life["a"] != LifecyclePartial || rootLife != LifecyclePartial {
		t.Fatalf("ancestors of stalled subtree must be partial: %v", life)
	}
	if life["b"] != LifecycleComplete {
		t.Fatalf("unrelated sibling must stay complete: %v", life)
	}
}

// A negative StallTimeout never abandons: the walk waits for the open.
func TestRun_NegativeStallTimeoutWaits(t *testing.T) {
	root := t.TempDir()
	slow := filepath.Join(root, "slow")
	mustWrite(t, filepath.Join(slow, "f"), 10)
	release := blockOpen(t, slow)
	time.AfterFunc(300*time.Millisecond, release)

	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1, StallTimeout: -1,
	}, &memorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.StalledCount != 0 || summary.Lifecycle != LifecycleComplete || summary.MatchedCount != 1 {
		t.Fatalf("summary=%+v", summary)
	}
}

func mustWrite(t *testing.T, path string, size int64) {
	t.Helper()
	writeSizedAt(t, path, size, time.Now())
}

// Every record of a stream carrying a stalled gap, a remote gap, and the new
// counters validates against the published profiles.
func TestRun_StallAndRemoteStreamValidatesAgainstSchemas(t *testing.T) {
	root := t.TempDir()
	stuck := filepath.Join(root, "stuck")
	cloud := filepath.Join(root, "cloud")
	mustWrite(t, filepath.Join(stuck, "f"), 10)
	mustWrite(t, filepath.Join(cloud, "g"), 10)
	mustWrite(t, filepath.Join(root, "ok", "h"), 10)
	blockOpen(t, stuck)
	cloudInfo, err := os.Stat(cloud)
	if err != nil {
		t.Fatal(err)
	}
	orig := datalessDir
	datalessDir = func(info os.FileInfo) bool { return os.SameFile(info, cloudInfo) }
	t.Cleanup(func() { datalessDir = orig })

	for _, tc := range []struct {
		name   string
		schema []byte
		opts   Options
	}{
		{"entries", spanwitschema.SpanwitFilesystemInventoryV0, Options{}},
		{"aggregation-v0", spanwitschema.SpanwitFilesystemInventoryAggregationV0,
			Options{EmissionMode: EmissionDirectorySummary, DirectoryDepth: -1, MaxAggregateDirectories: 100}},
		{"aggregation-v1", spanwitschema.SpanwitFilesystemInventoryAggregationV1,
			Options{EmissionMode: EmissionDirectorySummary, DirectoryDepth: -1, MaxAggregateDirectories: 100, DirectoryAccounting: true}},
	} {
		opts := tc.opts
		opts.Roots, opts.Backend, opts.Workers = []string{root}, BackendSerial, 1
		opts.StallTimeout = 100 * time.Millisecond
		var stream bytes.Buffer
		summary, err := Run(context.Background(), opts, NewJSONLSink(&stream))
		if err != nil {
			t.Fatal(err)
		}
		if summary.StalledCount != 1 || summary.RemoteSkipCount != 1 {
			t.Fatalf("%s: summary=%+v", tc.name, summary)
		}
		for i, line := range bytes.Split(bytes.TrimSpace(stream.Bytes()), []byte("\n")) {
			if err := contract.ValidateJSON(tc.schema, line); err != nil {
				t.Fatalf("%s record %d: %v\n%s", tc.name, i, err, line)
			}
			// The stall itself is never named: gap and summary records stay
			// path-free. A directory aggregate may name the directory (its name
			// came from the parent's ordinary listing and appears whether or not
			// it stalled) but must then report it partial, never complete.
			isGapOrSummary := bytes.Contains(line, []byte(`.gap.v1"`)) || bytes.Contains(line, []byte(`.summary.v1"`))
			if isGapOrSummary && bytes.Contains(line, []byte("stuck")) {
				t.Fatalf("%s record %d names the stalled directory: %s", tc.name, i, line)
			}
			if bytes.Contains(line, []byte(`"relative_path":"stuck"`)) && !bytes.Contains(line, []byte(`"lifecycle":"partial"`)) {
				t.Fatalf("%s record %d reports the stalled directory as not partial: %s", tc.name, i, line)
			}
		}
	}
}

// Two directories blocked in the same episode raise exactly one alert.
func TestRun_ConcurrentStallsCoalesceIntoOneAlert(t *testing.T) {
	root := t.TempDir()
	one := filepath.Join(root, "one")
	two := filepath.Join(root, "two")
	mustWrite(t, filepath.Join(one, "f"), 1)
	mustWrite(t, filepath.Join(two, "g"), 1)
	blockOpen(t, one)
	// blockOpen replaces openDir; chain a second block on top of the first.
	wantTwo, err := os.Stat(two)
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	t.Cleanup(func() { close(gate) })
	inner := openDir
	openDir = func(p string) (*os.File, error) {
		if got, statErr := os.Stat(p); statErr == nil && os.SameFile(got, wantTwo) {
			<-gate
		}
		return inner(p)
	}
	t.Cleanup(func() { openDir = inner })

	var alerts atomic.Int32
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendParallel, Workers: 4,
		StallTimeout: 400 * time.Millisecond, stallAlertAfter: 50 * time.Millisecond,
		OnStall: func(StallAlert) { alerts.Add(1) },
	}, &memorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.StalledCount != 2 {
		t.Fatalf("stalled=%d want 2", summary.StalledCount)
	}
	if n := alerts.Load(); n != 1 {
		t.Fatalf("alerts=%d want 1 (coalesced)", n)
	}
}
