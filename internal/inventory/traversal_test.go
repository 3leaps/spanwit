package inventory

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"testing/fstest"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

func TestTraversalDepthDeclaresPartialScope(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "shallow"), 7)
	mustWrite(t, filepath.Join(root, "nested", "deep"), 31)
	for _, workers := range []int{1, 4} {
		sink := &memorySink{}
		summary, err := Run(context.Background(), Options{Roots: []string{root}, Workers: workers,
			EmissionMode: EmissionSummaryOnly, Traversal: &TraversalOptions{MaxDepth: 0}}, sink)
		if err != nil {
			t.Fatal(err)
		}
		if summary.Lifecycle != LifecyclePartial || summary.MatchedApparentBytes != 7 || summary.GapCount != 1 ||
			summary.Roots[0].Observed == nil || !*summary.Roots[0].Observed {
			t.Fatalf("workers %d: %+v", workers, summary)
		}
		if len(sink.gaps) != 1 || sink.gaps[0].Kind != "depth-limit" || !sink.gaps[0].AffectsCompleteness {
			t.Fatalf("gaps: %+v", sink.gaps)
		}
		if sink.headers[0].Traversal == nil || sink.headers[0].Traversal.MaxDepth != 0 {
			t.Fatalf("depth not declared: %+v", sink.headers)
		}
		for recordType, value := range map[string]any{"header": sink.headers[0], "summary": summary, "gap": sink.gaps[0]} {
			raw, err := json.Marshal(map[string]any{"type": "spanwit.inventory." + recordType + ".v1",
				"run_id": "test", "seq": 0, "ts": "2026-09-29T00:00:00Z", "data": value})
			if err != nil {
				t.Fatal(err)
			}
			if err := contract.ValidateJSON(spanwitschema.SpanwitFilesystemInventoryV0, raw); err != nil {
				t.Fatalf("%s schema: %v\n%s", recordType, err, raw)
			}
		}
	}
	// Existing runs have neither a depth cap nor new observation metadata.
	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{Roots: []string{root}}, sink)
	if err != nil || summary.MatchedApparentBytes != 38 || summary.Lifecycle != LifecycleComplete ||
		summary.Roots[0].Observed != nil || sink.headers[0].Traversal != nil {
		t.Fatalf("default changed: %+v %v", summary, err)
	}
}

func TestDiscoveredAdmissionStableFailuresAndOverlap(t *testing.T) {
	parent := t.TempDir()
	a := filepath.Join(parent, "a")
	nested := filepath.Join(a, "nested")
	b := filepath.Join(parent, "b")
	missing := filepath.Join(parent, "missing")
	mustWrite(t, filepath.Join(a, "f"), 7)
	mustWrite(t, filepath.Join(nested, "g"), 31)
	mustWrite(t, filepath.Join(b, "h"), 5)
	var first []Root
	for _, paths := range [][]string{{nested, b, missing, a, a}, {missing, a, b, nested}} {
		sink := &memorySink{}
		summary, err := Run(context.Background(), Options{Roots: paths, EmissionMode: EmissionSummaryOnly,
			Traversal: &TraversalOptions{MaxDepth: -1, DiscoveredRoots: true}}, sink)
		if err != nil {
			t.Fatal(err)
		}
		if len(summary.Roots) != 4 || summary.MatchedApparentBytes != 43 || summary.GapCount != 2 || summary.Lifecycle != LifecyclePartial {
			t.Fatalf("siblings/overlap lost: %+v", summary)
		}
		if first == nil {
			first = sink.headers[0].Roots
		} else if !reflect.DeepEqual(first, sink.headers[0].Roots) {
			t.Fatalf("root identities depend on request order: %v vs %v", first, sink.headers[0].Roots)
		}
		kinds := make([]string, 0)
		for _, gap := range sink.gaps {
			kinds = append(kinds, gap.Kind)
			if gap.RootID == "" || !gap.AffectsCompleteness {
				t.Fatalf("unaccounted gap: %+v", gap)
			}
		}
		sort.Strings(kinds)
		if !reflect.DeepEqual(kinds, []string{"overlapping-root", "vanished"}) {
			t.Fatalf("gaps: %v", kinds)
		}
		for _, row := range summary.Roots {
			if row.Observed == nil || (row.GapCount > 0 && *row.Observed) {
				t.Fatalf("never-read candidate masquerades as measured: %+v", row)
			}
		}
	}
	// Caller-supplied missing/overlapping roots still fail before a header.
	for _, roots := range [][]string{{a, missing}, {a, nested}, {""}} {
		sink := &memorySink{}
		_, err := Run(context.Background(), Options{Roots: roots, EmissionMode: EmissionSummaryOnly,
			Traversal: &TraversalOptions{MaxDepth: -1}}, sink)
		if err == nil || len(sink.headers) != 0 {
			t.Fatalf("explicit admission no longer fails hard: %v, %v", roots, err)
		}
	}
}

func TestTraversalDiscoveryPrunesAndDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "target", "deep", "file"), 100)
	mustWrite(t, filepath.Join(root, "keep", "file"), 5)
	var visited []string
	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{Roots: []string{root}, EmissionMode: EmissionSummaryOnly,
		Traversal: &TraversalOptions{MaxDepth: 2, OnDirectory: func(v DirectoryVisit) bool {
			visited = append(visited, v.RelativePath)
			return filepath.Base(v.Path) == "target"
		}}}, sink)
	if err != nil || summary.MatchedApparentBytes != 5 || summary.ExclusionCount != 1 {
		t.Fatalf("pruning: %+v %v", summary, err)
	}
	sort.Strings(visited)
	if !reflect.DeepEqual(visited, []string{"keep", "target"}) || !sink.headers[0].Traversal.DirectoryPruning {
		t.Fatalf("discovery: %v", visited)
	}
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	sink = &memorySink{}
	summary, err = Run(context.Background(), Options{Roots: []string{link}, EmissionMode: EmissionSummaryOnly,
		Traversal: &TraversalOptions{MaxDepth: -1, DiscoveredRoots: true}}, sink)
	if err != nil || summary.MatchedCount != 0 || summary.GapCount != 1 || *summary.Roots[0].Observed {
		t.Fatalf("automatic symlink followed: %+v %v", summary, err)
	}
}

func TestObservationTraversalNotAvailableToStreamProfiles(t *testing.T) {
	_, err := Run(context.Background(), Options{Roots: []string{t.TempDir()},
		Traversal: &TraversalOptions{MaxDepth: 0}}, &memorySink{})
	if err == nil {
		t.Fatal("non-summary traversal should fail")
	}
}

type unknownTypeEntry struct{ fs.DirEntry }

func (unknownTypeEntry) Type() fs.FileMode { return 0 }

type unknownTypeDirectory struct{ fs.ReadDirFile }

func (d unknownTypeDirectory) ReadDir(n int) ([]fs.DirEntry, error) {
	entries, err := d.ReadDirFile.ReadDir(n)
	for i, entry := range entries {
		entries[i] = unknownTypeEntry{entry}
	}
	return entries, err
}

func TestTraversalUnknownTypeSymlinkIsCoverageGap(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		for _, observation := range []bool{false, true} {
			opts := Options{Roots: []string{t.TempDir()}, EmissionMode: EmissionSummaryOnly}
			if observation {
				opts.Traversal = &TraversalOptions{MaxDepth: -1}
			}
			nopts, err := normalizeOptions(opts)
			if err != nil {
				t.Fatal(err)
			}
			// Synthetic directory metadata exercises unknown d_type on every OS,
			// without requiring symlink privileges or a particular filesystem.
			fixture := fstest.MapFS{"link": &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("target")}}
			file, err := fixture.Open(".")
			if err != nil {
				t.Fatal(err)
			}
			reader := file.(fs.ReadDirFile)
			if unknown {
				reader = unknownTypeDirectory{reader}
			}
			ctx, cancel := context.WithCancel(context.Background())
			sink := &memorySink{}
			state := newRunState(ctx, cancel, nopts, sink)
			root := nopts.roots[0]
			state.beginRoot(root)
			scanDirectoryContents(ctx, root, root.Path, nopts, state, reader, newCoordinator(root.Path, nopts.MaxPendingDirs))
			state.completeRoot(root)
			summary := state.summary(0, time.Now())
			cancel()
			_ = file.Close()
			if summary.VisitedEntries != 1 || summary.MatchedCount != 0 || summary.MatchedApparentBytes != 0 {
				t.Fatalf("unknown=%v observation=%v: symlink counted as file: %+v", unknown, observation, summary)
			}
			if observation {
				if summary.Lifecycle != LifecyclePartial || summary.GapCount != 1 || len(sink.gaps) != 1 ||
					sink.gaps[0].Kind != "symlink" || !sink.gaps[0].AffectsCompleteness ||
					summary.Roots[0].Observed == nil || !*summary.Roots[0].Observed {
					t.Fatalf("unknown=%v: omitted symlink looks like measured empty: %+v gaps=%+v", unknown, summary, sink.gaps)
				}
			} else if summary.Lifecycle != LifecycleComplete || len(sink.gaps) != 0 || summary.Roots[0].Observed != nil {
				t.Fatalf("unknown=%v: default inventory semantics changed: %+v gaps=%+v", unknown, summary, sink.gaps)
			}
		}
	}
}
