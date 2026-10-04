package inventory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/corpus"
)

func TestOneFilesystemSkipsObservedHostBoundary(t *testing.T) {
	root, boundary, exclusions := observedMountFixture(t)
	boundaryPrefix := filepath.Clean(boundary) + string(filepath.Separator)

	for _, backend := range []string{BackendSerial, BackendParallel} {
		t.Run(backend, func(t *testing.T) {
			sink := &memorySink{}
			summary, err := Run(context.Background(), Options{
				Roots: []string{root}, Backend: backend,
				Workers: workersFor(backend), OneFilesystem: true,
				Exclusions: exclusions,
			}, sink)
			if err != nil {
				t.Fatal(err)
			}

			var observed bool
			for _, gap := range sink.gaps {
				if filepath.Clean(gap.LocalAbsolutePath) == filepath.Clean(boundary) {
					if gap.Kind != "mount-boundary" || gap.AffectsCompleteness {
						t.Fatalf("boundary gap=%+v", gap)
					}
					observed = true
				}
			}
			if !observed {
				t.Fatalf("no mount-boundary record for %s; gaps=%+v", boundary, sink.gaps)
			}
			if summary.BoundarySkipCount == 0 {
				t.Fatalf("summary did not count boundary skip: %+v", summary)
			}
			for _, entry := range sink.entries {
				clean := filepath.Clean(entry.LocalAbsolutePath)
				if clean == filepath.Clean(boundary) || strings.HasPrefix(clean, boundaryPrefix) {
					t.Fatalf("walker descended across boundary %s and emitted %s",
						boundary, entry.LocalAbsolutePath)
				}
			}
		})
	}
}

func TestDirectorySummaryMountBoundaryRemainsComplete(t *testing.T) {
	root, boundary, exclusions := observedMountFixture(t)
	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		OneFilesystem: true, Exclusions: exclusions, EmissionMode: EmissionDirectorySummary,
		DirectoryDepth: 0, MaxAggregateDirectories: 10000,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Lifecycle != LifecycleComplete || summary.BoundarySkipCount == 0 {
		t.Fatalf("summary=%+v boundary=%s", summary, boundary)
	}
	if len(sink.directories) != 1 || sink.directories[0].Lifecycle != LifecycleComplete ||
		sink.directories[0].AffectingGapCount != 0 {
		t.Fatalf("directories=%+v", sink.directories)
	}
}

// observedMountFixture finds a real mount boundary and returns its parent as
// the walk root plus exclusions that isolate the walk to that boundary, so the
// result does not depend on unrelated host siblings (e.g. /Volumes/.timemachine).
func observedMountFixture(t *testing.T) (root, boundary string, exclusions []string) {
	t.Helper()
	candidates := []string{"/dev", "/Volumes", "/run", "/mnt", "/media"}
	observable := false
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		boundaries, ok, err := corpus.MountBoundariesUnder(candidate, 2)
		if err != nil {
			t.Logf("mount fixture probe %s: %v", candidate, err)
			continue
		}
		observable = observable || ok
		if len(boundaries) == 0 {
			continue
		}
		parent := filepath.Dir(boundaries[0].Path)
		rules, err := corpus.IsolatingExclusions(parent, boundaries[0].Path)
		if err != nil {
			t.Logf("mount fixture isolate %s: %v", parent, err)
			continue
		}
		return parent, boundaries[0].Path, rules
	}

	omission, _ := corpus.FixtureOmission(observable, 0)
	t.Skipf("mount-boundary fixture omitted: %s", omission.Reason)
	return "", "", nil
}
