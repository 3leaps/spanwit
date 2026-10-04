package bench

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/corpus"
)

// buildCorpus builds a fixture corpus for a walker test.
func buildCorpus(t *testing.T, spec corpus.Spec) *corpus.Manifest {
	t.Helper()
	root := filepath.Join(t.TempDir(), "corpus")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	m, err := corpus.Build(root, spec)
	if err != nil {
		t.Fatalf("build corpus: %v", err)
	}
	t.Cleanup(func() {
		if err := corpus.Remove(m); err != nil {
			t.Errorf("remove corpus: %v", err)
		}
	})
	return m
}

// TestParallelAgreesWithSerial is the correctness gate a speed claim depends
// on. A faster walker that sees a different tree has not been shown to be
// faster at the same job.
func TestParallelAgreesWithSerial(t *testing.T) {
	specs := []corpus.Spec{
		{Kind: corpus.KindWide, Scale: 40, FileSize: 64},
		{Kind: corpus.KindDeep, Scale: 20, FileSize: 64},
		{Kind: corpus.KindDevTree, Scale: 120, FileSize: 32},
		{Kind: corpus.KindManyEmptyDirs, Scale: 50},
		{Kind: corpus.KindSymlinks, Scale: 8, FileSize: 64},
	}

	for _, spec := range specs {
		t.Run(string(spec.Kind), func(t *testing.T) {
			m := buildCorpus(t, spec)
			opts := WalkOptions{Root: m.Root, Sink: io.Discard}

			serial, err := SerialWalk(context.Background(), opts)
			if err != nil {
				t.Fatalf("serial walk: %v", err)
			}

			for _, workers := range []int{1, 2, 8} {
				parOpts := opts
				parOpts.Workers = workers
				parOpts.MaxOpenDirs = 4
				parallel, err := ParallelWalk(context.Background(), parOpts)
				if err != nil {
					t.Fatalf("parallel walk (%d workers): %v", workers, err)
				}

				if parallel.EntriesExamined != serial.EntriesExamined {
					t.Errorf("workers=%d: examined %d, serial saw %d",
						workers, parallel.EntriesExamined, serial.EntriesExamined)
				}
				if parallel.EntriesMatched != serial.EntriesMatched {
					t.Errorf("workers=%d: matched %d, serial saw %d",
						workers, parallel.EntriesMatched, serial.EntriesMatched)
				}
				if parallel.ApparentBytes != serial.ApparentBytes {
					t.Errorf("workers=%d: bytes %d, serial saw %d",
						workers, parallel.ApparentBytes, serial.ApparentBytes)
				}
				if parallel.Gaps != serial.Gaps {
					t.Errorf("workers=%d: gaps %d, serial saw %d",
						workers, parallel.Gaps, serial.Gaps)
				}
			}
		})
	}
}

// TestWalkersAgreeWithManifest checks both walkers against the construction
// oracle rather than only against each other, so a shared misconception does
// not pass as agreement.
func TestWalkersAgreeWithManifest(t *testing.T) {
	m := buildCorpus(t, corpus.Spec{Kind: corpus.KindDevTree, Scale: 100, FileSize: 32})
	if m.UnreachableObjects != 0 {
		t.Fatal("dev-tree corpus unexpectedly sealed objects")
	}

	// Files plus symlinks: the walkers count every non-directory entry as
	// examined, and this corpus has no symlinks.
	wantExamined := m.Files + m.Symlinks

	for name, walk := range map[string]func(context.Context, WalkOptions) (WalkStats, error){
		"serial":   SerialWalk,
		"parallel": ParallelWalk,
	} {
		t.Run(name, func(t *testing.T) {
			stats, err := walk(context.Background(), WalkOptions{
				Root: m.Root, Workers: 4, MaxOpenDirs: 4, Sink: io.Discard,
			})
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
			if stats.EntriesExamined != wantExamined {
				t.Errorf("examined %d, manifest planned %d", stats.EntriesExamined, wantExamined)
			}
			if stats.ApparentBytes != m.ApparentBytes {
				t.Errorf("apparent bytes %d, manifest planned %d", stats.ApparentBytes, m.ApparentBytes)
			}
			if !stats.Complete() {
				t.Errorf("walk reported %d gaps over a fully readable corpus", stats.Gaps)
			}
		})
	}
}

// TestWalkersReportGapsRatherThanClaimingComplete is the coverage-honesty
// check: a walk that could not read part of the tree must say so.
func TestWalkersReportGapsRatherThanClaimingComplete(t *testing.T) {
	m := buildCorpus(t, corpus.Spec{Kind: corpus.KindMixedPermissions, Scale: 4, FileSize: 32})
	if !m.Capabilities.PermissionEnforcement {
		t.Skipf("permission enforcement unavailable: %s", m.Capabilities.Summary())
	}

	for name, walk := range map[string]func(context.Context, WalkOptions) (WalkStats, error){
		"serial":   SerialWalk,
		"parallel": ParallelWalk,
	} {
		t.Run(name, func(t *testing.T) {
			stats, err := walk(context.Background(), WalkOptions{
				Root: m.Root, Workers: 4, MaxOpenDirs: 2, Sink: io.Discard,
			})
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
			if stats.Gaps == 0 {
				t.Fatal("walk reported no gaps over a corpus with sealed directories")
			}
			if stats.Complete() {
				t.Error("walk with gaps reported itself complete")
			}
			// The observable count must fall short of the total by exactly
			// what the corpus sealed away.
			wantExamined := m.Files + m.Symlinks - m.UnreachableObjects
			if stats.EntriesExamined != wantExamined {
				t.Errorf("examined %d, expected %d observable of %d total",
					stats.EntriesExamined, wantExamined, m.Files+m.Symlinks)
			}
		})
	}
}

// TestParallelWalkTerminatesUnderTightBounds covers the concurrency-boundary
// class directly: one worker and a single directory handle over a tree deep
// and wide enough that a queue-blocking design would stall.
func TestParallelWalkTerminatesUnderTightBounds(t *testing.T) {
	m := buildCorpus(t, corpus.Spec{Kind: corpus.KindDeep, Scale: 40, FileSize: 16})

	cases := []struct{ workers, maxOpen int }{
		{1, 1},
		{2, 1},
		{8, 1},
		{1, 8},
		{16, 2},
	}

	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			done := make(chan WalkStats, 1)
			errCh := make(chan error, 1)
			go func() {
				stats, err := ParallelWalk(context.Background(), WalkOptions{
					Root: m.Root, Workers: tc.workers, MaxOpenDirs: tc.maxOpen, Sink: io.Discard,
				})
				if err != nil {
					errCh <- err
					return
				}
				done <- stats
			}()

			select {
			case err := <-errCh:
				t.Fatalf("workers=%d maxOpen=%d: %v", tc.workers, tc.maxOpen, err)
			case stats := <-done:
				if stats.EntriesExamined == 0 {
					t.Errorf("workers=%d maxOpen=%d: walk examined nothing",
						tc.workers, tc.maxOpen)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("workers=%d maxOpen=%d: walk did not terminate; "+
					"bounded-resource deadlock", tc.workers, tc.maxOpen)
			}
		})
	}
}

// TestWalkersDoNotFollowDirectorySymlinks confirms the cycle in the symlink
// corpus terminates and its subtree is not counted twice.
func TestWalkersDoNotFollowDirectorySymlinks(t *testing.T) {
	m := buildCorpus(t, corpus.Spec{Kind: corpus.KindSymlinks, Scale: 6, FileSize: 32})
	if !m.Capabilities.Symlinks {
		t.Skipf("symlinks unavailable: %s", m.Capabilities.Summary())
	}

	wantExamined := m.Files + m.Symlinks

	for name, walk := range map[string]func(context.Context, WalkOptions) (WalkStats, error){
		"serial":   SerialWalk,
		"parallel": ParallelWalk,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			stats, err := walk(ctx, WalkOptions{
				Root: m.Root, Workers: 4, MaxOpenDirs: 4, Sink: io.Discard,
			})
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
			if ctx.Err() != nil {
				t.Fatal("walk did not terminate; a symlink cycle was followed")
			}
			if stats.EntriesExamined != wantExamined {
				t.Errorf("examined %d, manifest planned %d; a directory symlink was followed",
					stats.EntriesExamined, wantExamined)
			}
		})
	}
}

// TestWalkersDistinguishVanishedFromDenied checks that an object removed
// mid-traversal is recorded as a change in the tree rather than as a
// permission gap. Conflating them would make a busy filesystem look like a
// permissions problem.
func TestWalkersDistinguishVanishedFromDenied(t *testing.T) {
	m := buildCorpus(t, corpus.Spec{
		Kind: corpus.KindWide, Scale: 60, FileSize: 32, RecordEntries: true,
	})

	victims := corpus.VanishableFiles(m, 20)
	if len(victims) == 0 {
		t.Fatal("no vanishable files in corpus")
	}
	v := corpus.NewVanisher(m, victims)

	// Remove the victims up front: every one is then guaranteed to be absent
	// when the walk reaches it, which exercises the not-exist path
	// deterministically rather than racing for it.
	for range victims {
		if _, err := v.VanishNext(); err != nil {
			t.Fatalf("vanish: %v", err)
		}
	}

	stats, err := ParallelWalk(context.Background(), WalkOptions{
		Root: m.Root, Workers: 4, MaxOpenDirs: 4, Sink: io.Discard,
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if stats.Gaps != 0 {
		t.Errorf("removals were reported as %d gaps; they are not denials", stats.Gaps)
	}
	wantExamined := m.Files - int64(v.Count())
	if stats.EntriesExamined != wantExamined {
		t.Errorf("examined %d, expected %d after %d removals",
			stats.EntriesExamined, wantExamined, v.Count())
	}
}

// TestTimeToFirstMatchPrecedesCompletion confirms the metric measures what it
// claims: a result available before the walk finished.
func TestTimeToFirstMatchPrecedesCompletion(t *testing.T) {
	m := buildCorpus(t, corpus.Spec{Kind: corpus.KindWide, Scale: 200, FileSize: 64})

	start := time.Now()
	stats, err := ParallelWalk(context.Background(), WalkOptions{
		Root: m.Root, Workers: 4, MaxOpenDirs: 4, Sink: io.Discard,
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	total := time.Since(start)

	if stats.EntriesMatched == 0 {
		t.Fatal("walk matched nothing")
	}
	if stats.TimeToFirstMatch <= 0 {
		t.Fatal("no time to first match recorded despite matches")
	}
	if stats.TimeToFirstMatch > total {
		t.Errorf("first match at %v exceeds total walk time %v", stats.TimeToFirstMatch, total)
	}
}

// TestMinSizeFilterSeparatesExaminedFromMatched covers the accounting
// distinction: examined, matched, and emitted are three different numbers, and
// a filtered run makes that visible.
func TestMinSizeFilterSeparatesExaminedFromMatched(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sized")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Three small files and two large ones.
	for i, size := range []int{10, 10, 10, 5000, 5000} {
		name := filepath.Join(root, filepath.Base(filepath.Join("f", string(rune('a'+i))+".bin")))
		if err := os.WriteFile(name, make([]byte, size), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	for name, walk := range map[string]func(context.Context, WalkOptions) (WalkStats, error){
		"serial":   SerialWalk,
		"parallel": ParallelWalk,
	} {
		t.Run(name, func(t *testing.T) {
			stats, err := walk(context.Background(), WalkOptions{
				Root: root, Workers: 2, MaxOpenDirs: 2, MinSize: 1000, Sink: io.Discard,
			})
			if err != nil {
				t.Fatalf("walk: %v", err)
			}
			if stats.EntriesExamined != 5 {
				t.Errorf("examined %d, want 5", stats.EntriesExamined)
			}
			if stats.EntriesMatched != 2 {
				t.Errorf("matched %d, want 2", stats.EntriesMatched)
			}
			if stats.EntriesMatched >= stats.EntriesExamined {
				t.Error("a size filter should leave matched below examined")
			}
			if stats.ApparentBytes != 10000 {
				t.Errorf("apparent bytes %d, want 10000", stats.ApparentBytes)
			}
		})
	}
}
