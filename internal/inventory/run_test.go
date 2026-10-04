package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

type memorySink struct {
	mu          sync.Mutex
	headers     []Header
	entries     []Entry
	directories []Directory
	gaps        []Gap
	summaries   []Summary
	entryFn     func(Entry) error
	directoryFn func(Directory) error
}

func (s *memorySink) Header(value Header) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headers = append(s.headers, value)
	return nil
}

func (s *memorySink) Entry(value Entry) error {
	s.mu.Lock()
	s.entries = append(s.entries, value)
	fn := s.entryFn
	s.mu.Unlock()
	if fn != nil {
		return fn(value)
	}
	return nil
}

func (s *memorySink) Directory(value Directory) error {
	s.mu.Lock()
	s.directories = append(s.directories, value)
	fn := s.directoryFn
	s.mu.Unlock()
	if fn != nil {
		return fn(value)
	}
	return nil
}

func (s *memorySink) Gap(value Gap) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gaps = append(s.gaps, value)
	return nil
}

func (s *memorySink) Summary(value Summary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summaries = append(s.summaries, value)
	return nil
}

func TestDirectorySummaryUsesFullSubjectAndDeterministicTop(t *testing.T) {
	root := t.TempDir()
	writeSizedAt(t, filepath.Join(root, "a", "x.bin"), 10, time.Now())
	writeSizedAt(t, filepath.Join(root, "a", "y.bin"), 20, time.Now())
	writeSizedAt(t, filepath.Join(root, "b", "z.bin"), 30, time.Now())

	for _, backend := range []string{BackendSerial, BackendParallel} {
		t.Run(backend, func(t *testing.T) {
			sink := &memorySink{}
			summary, err := Run(context.Background(), Options{
				Roots: []string{root}, Backend: backend, Workers: workersFor(backend),
				EmissionMode: EmissionDirectorySummary, MinSize: 1000,
				DirectoryDepth: 1, DirectoryTop: 2, MaxAggregateDirectories: 10,
			}, sink)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if summary.Lifecycle != LifecycleComplete || summary.MatchedCount != 0 ||
				summary.DirectoryEmittedCount == nil || *summary.DirectoryEmittedCount != 2 ||
				summary.DirectorySelectionTruncated == nil || !*summary.DirectorySelectionTruncated {
				t.Fatalf("summary=%+v", summary)
			}
			if len(sink.headers) != 1 || sink.headers[0].Profile != AggregationProfileV0 ||
				sink.headers[0].Subject == nil || sink.headers[0].Subject.AccountingScope != "full_subject" {
				t.Fatalf("header=%+v", sink.headers)
			}
			want := []Directory{
				{RootID: "root-1", RelativePath: ".", Depth: 0, ApparentBytesSum: 60, FileCount: 3, DescendantDirectoryCount: 2, Lifecycle: LifecycleComplete},
				{RootID: "root-1", RelativePath: "a", Depth: 1, ApparentBytesSum: 30, FileCount: 2, Lifecycle: LifecycleComplete},
			}
			for i := range want {
				want[i].AllocatedBytesSum = sink.directories[i].AllocatedBytesSum
				want[i].AllocatedUnmeasuredCount = sink.directories[i].AllocatedUnmeasuredCount
			}
			if !reflect.DeepEqual(sink.directories, want) {
				t.Fatalf("directories=%+v want=%+v", sink.directories, want)
			}
		})
	}
}

func TestDirectorySummaryBudgetFailureEmitsNoDirectories(t *testing.T) {
	root := t.TempDir()
	writeSizedAt(t, filepath.Join(root, "a", "x.bin"), 1, time.Now())
	writeSizedAt(t, filepath.Join(root, "b", "y.bin"), 1, time.Now())
	if _, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		EmissionMode: EmissionDirectorySummary, DirectoryDepth: -1,
	}, &memorySink{}); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("zero aggregate budget error=%v", err)
	}
	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		EmissionMode: EmissionDirectorySummary, DirectoryDepth: -1,
		MaxAggregateDirectories: 2,
	}, sink)
	if !errors.Is(err, errAggregateBudget) {
		t.Fatalf("error=%v", err)
	}
	if summary.Lifecycle != LifecycleFailed || len(sink.directories) != 0 {
		t.Fatalf("summary=%+v directories=%+v", summary, sink.directories)
	}
}

func TestDirectoryAccountingClaimsQualifyTotals(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.bin")
	writeSizedAt(t, path, 100, time.Now())
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	meta := metadataOf(info)

	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		EmissionMode: EmissionDirectorySummary, DirectoryDepth: 0,
		MaxAggregateDirectories: 10, DirectoryAccounting: true,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Lifecycle != LifecycleComplete {
		t.Fatalf("summary=%+v", summary)
	}
	if len(sink.headers) != 1 || sink.headers[0].Profile != AggregationProfileV1 {
		t.Fatalf("header=%+v", sink.headers)
	}
	if sink.headers[0].DirectoryAccounting == nil || !*sink.headers[0].DirectoryAccounting {
		t.Fatalf("header must record directory accounting: %+v", sink.headers[0])
	}
	if len(sink.directories) != 1 {
		t.Fatalf("directories=%+v", sink.directories)
	}
	claims := sink.directories[0].Accounting
	if claims == nil {
		t.Fatal("v1 directory record requires accounting claims")
	}
	if claims.Apparent.Status != ClaimStatusMeasured ||
		claims.Apparent.Basis != BasisApparentPathEntrySum ||
		claims.Apparent.Bound != BoundExact ||
		claims.Apparent.Bytes == nil || *claims.Apparent.Bytes != 100 {
		t.Fatalf("apparent claim=%+v", claims.Apparent)
	}
	if meta.allocated == nil {
		if claims.Allocated.Status != ClaimStatusUnsupported || claims.Allocated.Bytes != nil ||
			claims.Allocated.Basis != "" {
			t.Fatalf("unmeasured allocated claim=%+v", claims.Allocated)
		}
	} else {
		if claims.Allocated.Status != ClaimStatusMeasured ||
			claims.Allocated.Basis != BasisAllocatedPathEntrySum ||
			claims.Allocated.Bound != BoundExact ||
			claims.Allocated.Bytes == nil || *claims.Allocated.Bytes != *meta.allocated {
			t.Fatalf("allocated claim=%+v", claims.Allocated)
		}
	}
	for name, claim := range map[string]AccountingClaim{
		"unique_physical":  claims.UniquePhysical,
		"shared_cloned":    claims.SharedCloned,
		"expected_reclaim": claims.ExpectedReclaim,
	} {
		if claim.Status != ClaimStatusUnsupported || claim.Bytes != nil ||
			claim.Basis != "" || claim.Bound != "" || claim.Detail == "" {
			t.Fatalf("%s claim must be unsupported with no number: %+v", name, claim)
		}
	}
}

func TestDirectoryAccountingGapDowngradesToPartialLower(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		writeSizedAt(t, filepath.Join(root, name, "file.bin"), 10, time.Now())
	}
	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		MaxPendingDirs: 1, EmissionMode: EmissionDirectorySummary,
		DirectoryDepth: 0, MaxAggregateDirectories: 10, DirectoryAccounting: true,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Lifecycle != LifecyclePartial || summary.QueueLimitSkipCount == 0 {
		t.Fatalf("summary=%+v", summary)
	}
	if len(sink.directories) == 0 {
		t.Fatal("expected directory records")
	}
	claims := sink.directories[0].Accounting
	if claims == nil || claims.Apparent.Status != ClaimStatusPartial ||
		claims.Apparent.Bound != BoundLower || claims.Apparent.Bytes == nil {
		t.Fatalf("apparent claim must be partial/lower: %+v", claims)
	}
	if claims.Allocated.Status == ClaimStatusMeasured {
		t.Fatalf("allocated claim must not stay measured under partial coverage: %+v", claims.Allocated)
	}
}

func TestDirectoryAccountingZeroMeasuredAllocationIsUnsupported(t *testing.T) {
	root := Root{ID: "root-1", Path: string(filepath.Separator) + "root"}
	a := newAggregator(2, true)
	if err := a.admit(root, root.Path); err != nil {
		t.Fatal(err)
	}
	if err := a.addFile(root, filepath.Join(root.Path, "x.bin"), 10, nil); err != nil {
		t.Fatal(err)
	}
	records, err := a.records(-1, 0, SizeApparent)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Accounting == nil {
		t.Fatalf("records=%+v", records)
	}
	allocated := records[0].Accounting.Allocated
	if allocated.Status != ClaimStatusUnsupported || allocated.Bytes != nil ||
		allocated.Bound != "" || allocated.Basis != "" || allocated.Detail == "" {
		t.Fatalf("zero-measured allocated claim=%+v", allocated)
	}
}

func TestDirectoryAccountingMixedAllocationKeepsPartialClaim(t *testing.T) {
	root := Root{ID: "root-1", Path: string(filepath.Separator) + "root"}
	known := int64(100)

	// Unmeasured first, then measured: the exact order that previously lost
	// the measured subtotal behind the nulled v0 sum.
	a := newAggregator(2, true)
	if err := a.admit(root, root.Path); err != nil {
		t.Fatal(err)
	}
	if err := a.addFile(root, filepath.Join(root.Path, "unknown.bin"), 10, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.addFile(root, filepath.Join(root.Path, "known.bin"), 100, &known); err != nil {
		t.Fatal(err)
	}
	records, err := a.records(-1, 0, SizeApparent)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Accounting == nil {
		t.Fatalf("records=%+v", records)
	}
	allocated := records[0].Accounting.Allocated
	if allocated.Status != ClaimStatusPartial || allocated.Bound != BoundLower ||
		allocated.Basis != BasisAllocatedPathEntrySum ||
		allocated.Bytes == nil || *allocated.Bytes != 100 ||
		allocated.UnmeasuredCount == nil || *allocated.UnmeasuredCount != 1 {
		t.Fatalf("mixed allocated claim=%+v", allocated)
	}

	// The mirrored order must produce the same claim.
	b := newAggregator(2, true)
	if err := b.admit(root, root.Path); err != nil {
		t.Fatal(err)
	}
	if err := b.addFile(root, filepath.Join(root.Path, "known.bin"), 100, &known); err != nil {
		t.Fatal(err)
	}
	if err := b.addFile(root, filepath.Join(root.Path, "unknown.bin"), 10, nil); err != nil {
		t.Fatal(err)
	}
	mirrored, err := b.records(-1, 0, SizeApparent)
	if err != nil {
		t.Fatal(err)
	}
	if len(mirrored) != 1 || !reflect.DeepEqual(mirrored[0].Accounting, records[0].Accounting) {
		t.Fatalf("mirrored claim=%+v want=%+v", mirrored[0].Accounting, records[0].Accounting)
	}
}

func TestBlockBytesRejectsNegativeAndOverflow(t *testing.T) {
	if bytes, ok := blockBytes(2); !ok || bytes != 1024 {
		t.Fatalf("blockBytes(2)=%d,%v", bytes, ok)
	}
	if _, ok := blockBytes(-1); ok {
		t.Fatal("negative block count must be rejected")
	}
	if _, ok := blockBytes(math.MaxInt64/512 + 1); ok {
		t.Fatal("overflowing block count must be rejected")
	}
}

func TestAggregatorPropagatesGapAndRejectsOverflow(t *testing.T) {
	root := Root{ID: "root-1", Path: string(filepath.Separator) + "root"}
	a := newAggregator(10, false)
	for _, path := range []string{root.Path, filepath.Join(root.Path, "a"), filepath.Join(root.Path, "a", "b")} {
		if err := a.admit(root, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.addGap(root, filepath.Join(root.Path, "a", "b", "denied")); err != nil {
		t.Fatal(err)
	}
	records, err := a.records(-1, 0, SizeApparent)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Lifecycle != LifecyclePartial || record.AffectingGapCount != 1 {
			t.Fatalf("gap did not propagate: %+v", records)
		}
	}

	overflow := newAggregator(1, false)
	if err := overflow.admit(root, root.Path); err != nil {
		t.Fatal(err)
	}
	overflow.values[aggregateKey{rootID: root.ID, rel: "."}].FileCount = math.MaxInt64
	if err := overflow.addFile(root, filepath.Join(root.Path, "x"), 1, nil); !errors.Is(err, errAggregateOverflow) {
		t.Fatalf("overflow error=%v", err)
	}
}

func TestAggregatorAllocatedTopRejectsUnmeasuredEligibleDirectory(t *testing.T) {
	root := Root{ID: "root-1", Path: string(filepath.Separator) + "root"}
	a := newAggregator(2, false)
	if err := a.admit(root, root.Path); err != nil {
		t.Fatal(err)
	}
	if err := a.addFile(root, filepath.Join(root.Path, "unknown.bin"), 100, nil); err != nil {
		t.Fatal(err)
	}
	if records, err := a.records(-1, 1, SizeAllocated); !errors.Is(err, errAggregateAllocatedTopUnknown) || records != nil {
		t.Fatalf("records=%+v error=%v", records, err)
	}
}

func TestDirectorySummaryExclusionIsOutsideSubject(t *testing.T) {
	root := t.TempDir()
	writeSizedAt(t, filepath.Join(root, "keep", "file.bin"), 10, time.Now())
	writeSizedAt(t, filepath.Join(root, "skip", "file.bin"), 20, time.Now())
	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		EmissionMode: EmissionDirectorySummary, DirectoryDepth: -1,
		MaxAggregateDirectories: 10, Exclusions: []string{"skip"},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Lifecycle != LifecycleComplete || summary.ExclusionCount != 1 {
		t.Fatalf("summary=%+v", summary)
	}
	if len(sink.directories) != 2 || sink.directories[0].RelativePath != "." ||
		sink.directories[0].ApparentBytesSum != 10 ||
		sink.directories[0].DescendantDirectoryCount != 1 ||
		sink.directories[1].RelativePath != "keep" {
		t.Fatalf("directories=%+v", sink.directories)
	}
}

func TestDirectorySummaryQueueGapMarksAncestorsPartial(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		writeSizedAt(t, filepath.Join(root, name, "file.bin"), 1, time.Now())
	}
	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		MaxPendingDirs: 1, EmissionMode: EmissionDirectorySummary,
		DirectoryDepth: -1, MaxAggregateDirectories: 10,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Lifecycle != LifecyclePartial || summary.QueueLimitSkipCount != 2 {
		t.Fatalf("summary=%+v gaps=%+v", summary, sink.gaps)
	}
	var rootRecord *Directory
	for i := range sink.directories {
		if sink.directories[i].RelativePath == "." {
			rootRecord = &sink.directories[i]
		}
	}
	if rootRecord == nil || rootRecord.Lifecycle != LifecyclePartial || rootRecord.AffectingGapCount != 2 {
		t.Fatalf("directories=%+v", sink.directories)
	}
}

func TestDirectorySummaryHardLinkAndSparseArePathSums(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "sparse.bin")
	writeSizedAt(t, original, 1<<20, time.Now())
	if err := os.Link(original, filepath.Join(root, "linked.bin")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	info, err := os.Stat(original)
	if err != nil {
		t.Fatal(err)
	}
	meta := metadataOf(info)
	sink := &memorySink{}
	_, err = Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		EmissionMode: EmissionDirectorySummary, DirectoryDepth: 0,
		MaxAggregateDirectories: 2,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.directories) != 1 || sink.directories[0].FileCount != 2 ||
		sink.directories[0].ApparentBytesSum != 2*info.Size() {
		t.Fatalf("directory=%+v", sink.directories)
	}
	if meta.allocated != nil {
		want := 2 * *meta.allocated
		if sink.directories[0].AllocatedBytesSum == nil || *sink.directories[0].AllocatedBytesSum != want {
			t.Fatalf("allocated=%v want path sum %d", sink.directories[0].AllocatedBytesSum, want)
		}
	}
}

func TestDirectorySummaryCancellationEmitsNoDirectories(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink := &memorySink{}
	summary, err := Run(ctx, Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		EmissionMode: EmissionDirectorySummary, DirectoryDepth: -1,
		MaxAggregateDirectories: 10,
	}, sink)
	if err != nil {
		t.Fatalf("cancellation should remain coverage evidence: %v", err)
	}
	if summary.Lifecycle != LifecyclePartial || !summary.Canceled || len(sink.directories) != 0 ||
		summary.DirectoryEmittedCount == nil || *summary.DirectoryEmittedCount != 0 {
		t.Fatalf("summary=%+v directories=%+v", summary, sink.directories)
	}
}

func TestDirectorySummarySinkFailureLeavesNoTerminalSummary(t *testing.T) {
	root := t.TempDir()
	writeSizedAt(t, filepath.Join(root, "file.bin"), 1, time.Now())
	want := errors.New("directory sink failed")
	sink := &memorySink{directoryFn: func(Directory) error { return want }}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		EmissionMode: EmissionDirectorySummary, DirectoryDepth: -1,
		MaxAggregateDirectories: 10,
	}, sink)
	if !errors.Is(err, want) || summary.Lifecycle != LifecycleFailed || len(sink.summaries) != 0 {
		t.Fatalf("error=%v summary=%+v terminal=%+v", err, summary, sink.summaries)
	}
}

func TestRunFiltersUseOneCapturedClockAndInclusiveBounds(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	writeSizedAt(t, filepath.Join(root, "exact-older.bin"), 200, now.Add(-10*24*time.Hour))
	writeSizedAt(t, filepath.Join(root, "too-fresh.bin"), 200, now.Add(-10*24*time.Hour+time.Second))
	writeSizedAt(t, filepath.Join(root, "exact-newer.bin"), 300, now.Add(-20*24*time.Hour))
	writeSizedAt(t, filepath.Join(root, "too-old.bin"), 300, now.Add(-20*24*time.Hour-time.Second))
	writeSizedAt(t, filepath.Join(root, "too-small.bin"), 99, now.Add(-15*24*time.Hour))

	for _, backend := range []string{BackendSerial, BackendParallel} {
		t.Run(backend, func(t *testing.T) {
			sink := &memorySink{}
			summary, err := Run(context.Background(), Options{
				Roots: []string{root}, Backend: backend, Workers: workersFor(backend),
				MinSize: 100, OlderThan: 10 * 24 * time.Hour,
				NewerThan: 20 * 24 * time.Hour, Now: now,
			}, sink)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if summary.Lifecycle != LifecycleComplete {
				t.Fatalf("lifecycle=%s gaps=%v", summary.Lifecycle, sink.gaps)
			}
			got := relativePaths(sink.entries)
			want := []string{"exact-newer.bin", "exact-older.bin"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("matches=%v want=%v", got, want)
			}
			if summary.VisitedEntries != 5 || summary.MatchedCount != 2 ||
				summary.EmittedCount != 2 || summary.EmittedApparentBytes != 500 {
				t.Fatalf("summary=%+v", summary)
			}
			if len(sink.headers) != 1 || len(sink.summaries) != 1 {
				t.Fatalf("headers=%d summaries=%d", len(sink.headers), len(sink.summaries))
			}
		})
	}
}

func TestTopKMatchesFullSortOracle(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	sizes := map[string]int64{
		"e.bin": 500, "a.bin": 100, "d.bin": 400, "b.bin": 400, "c.bin": 300,
	}
	for name, size := range sizes {
		writeSizedAt(t, filepath.Join(root, name), size, now)
	}
	// Expected ranking is size descending, then path ascending.
	want := []string{"e.bin", "b.bin", "d.bin"}

	for _, backend := range []string{BackendSerial, BackendParallel} {
		t.Run(backend, func(t *testing.T) {
			sink := &memorySink{}
			summary, err := Run(context.Background(), Options{
				Roots: []string{root}, Backend: backend, Workers: workersFor(backend),
				Top: 3, Now: now,
			}, sink)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(sink.entries))
			for _, entry := range sink.entries {
				got = append(got, entry.RelativePath)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("top=%v want=%v", got, want)
			}
			if summary.MatchedCount != 5 || summary.EmittedCount != 3 ||
				summary.EmittedApparentBytes != 1300 || !summary.SelectionTruncated {
				t.Fatalf("summary=%+v", summary)
			}
		})
	}
}

func TestSummaryOnlyPreservesCompleteWalkAndTopSelectionSemantics(t *testing.T) {
	root := t.TempDir()
	writeSizedAt(t, filepath.Join(root, "large.bin"), 20, time.Now())
	writeSizedAt(t, filepath.Join(root, "small.bin"), 10, time.Now())

	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		Top: 1, EmissionMode: EmissionSummaryOnly,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if got := sink.headers[0]; got.EmissionMode != EmissionSummaryOnly || got.PathProtection != PathProtectionSourceStructure {
		t.Fatalf("header=%+v", got)
	}
	if len(sink.entries) != 0 || summary.Lifecycle != LifecycleComplete ||
		summary.MatchedCount != 2 || summary.EmittedCount != 0 ||
		summary.EmittedApparentBytes != 0 || summary.EmittedAllocatedBytes != 0 ||
		!summary.EntriesSuppressed || !summary.SelectionTruncated {
		t.Fatalf("entries=%+v summary=%+v", sink.entries, summary)
	}
	assertRootReconciliation(t, summary)
}

func TestExclusionsAreNormalizedAndAppliedBeforeDescent(t *testing.T) {
	root := t.TempDir()
	writeSizedAt(t, filepath.Join(root, "keep.bin"), 10, time.Now())
	writeSizedAt(t, filepath.Join(root, "ignored.bin"), 10, time.Now())
	writeSizedAt(t, filepath.Join(root, "skiptree", "hidden.bin"), 10, time.Now())

	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		Exclusions: []string{"skiptree", "ignored.bin", "ignored.bin"},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := relativePaths(sink.entries), []string{"keep.bin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("entries=%v want=%v", got, want)
	}
	if summary.Lifecycle != LifecycleComplete || summary.ExclusionCount != 2 || summary.GapCount != 0 {
		t.Fatalf("summary=%+v gaps=%+v", summary, sink.gaps)
	}
	if got := sink.headers[0].Exclusions; !reflect.DeepEqual(got, []Exclusion{
		{Kind: "basename", Value: "ignored.bin"}, {Kind: "basename", Value: "skiptree"},
	}) {
		t.Fatalf("normalized exclusions=%+v", got)
	}
	assertRootReconciliation(t, summary)

	rules, err := normalizeExclusions([]string{"name", "./name", "nested/name"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []Exclusion{
		{Kind: "basename", Value: "name"},
		{Kind: "root_relative", Value: "name"},
		{Kind: "root_relative", Value: "nested/name"},
	}; !reflect.DeepEqual(rules, want) {
		t.Fatalf("root-relative/basename normalization=%+v want=%+v", rules, want)
	}

	for _, rule := range []string{"/outside", "../outside", "nested/../../outside", `C:\\outside`, `nested\\..\\outside`} {
		if _, err := Run(context.Background(), Options{Roots: []string{root}, Exclusions: []string{rule}}, &memorySink{}); err == nil {
			t.Errorf("exclusion %q was accepted", rule)
		}
	}
}

func TestProgressIsRateLimitedAndPathFree(t *testing.T) {
	root := t.TempDir()
	for i := range 8 {
		writeSizedAt(t, filepath.Join(root, fmt.Sprintf("f-%d", i)), 1, time.Now())
	}
	var snapshots []Progress
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		ProgressInterval: time.Hour,
		Progress:         func(snapshot Progress) { snapshots = append(snapshots, snapshot) },
	}, &memorySink{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].TotalRoots != 1 || snapshots[0].ActiveRootID != "root-1" {
		t.Fatalf("snapshots=%+v", snapshots)
	}
	if summary.Lifecycle != LifecycleComplete {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestCoordinatorDoneRetiresExactlyOneActiveDirectory(t *testing.T) {
	coord := newCoordinator("root", 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, ok := coord.next(ctx); !ok {
		t.Fatal("root was not available")
	}
	if accepted, running := coord.offer("child"); !accepted || !running {
		t.Fatalf("offer accepted=%v running=%v", accepted, running)
	}
	coord.done()
	if dir, ok := coord.next(ctx); !ok || dir != "child" {
		t.Fatalf("next=%q,%v", dir, ok)
	}
	coord.done()
	done := make(chan bool, 1)
	go func() { _, ok := coord.next(ctx); done <- ok }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("coordinator reported unexpected work")
		}
	case <-time.After(time.Second):
		coord.stop()
		t.Fatal("coordinator did not retire active work")
	}
}

func TestGapDetailIsBoundedAndDoesNotCopyLocalPath(t *testing.T) {
	root := Root{ID: "root-1", Path: "/patient"}
	gap := pathGap(root, "/patient/private/report.txt", errors.New("open /patient/private/report.txt: permission denied"))
	if strings.Contains(gap.Detail, "/patient") || gap.Detail != "filesystem operation failed" {
		t.Fatalf("gap=%+v", gap)
	}
	if got := sanitizeGapDetail(strings.Repeat("x", maxGapDetailRunes+1) + "\nignored"); len([]rune(got)) != maxGapDetailRunes {
		t.Fatalf("sanitized detail length=%d", len([]rune(got)))
	}
}

func TestMultipleRootsAndOverlapRefusal(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	if err := os.MkdirAll(filepath.Join(a, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSizedAt(t, filepath.Join(a, "a.bin"), 10, time.Now())
	writeSizedAt(t, filepath.Join(b, "b.bin"), 10, time.Now())

	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{
		Roots: []string{a, b}, Backend: BackendSerial, Workers: 1,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if summary.MatchedCount != 2 || len(sink.headers[0].Roots) != 2 {
		t.Fatalf("summary=%+v roots=%+v", summary, sink.headers[0].Roots)
	}
	assertRootReconciliation(t, summary)

	_, err = Run(context.Background(), Options{
		Roots: []string{a, filepath.Join(a, "nested")},
	}, &memorySink{})
	if err == nil || !strings.Contains(err.Error(), "overlapping roots") {
		t.Fatalf("overlap error=%v", err)
	}
}

func TestFailurePreservesCompletedRootAccounting(t *testing.T) {
	base := t.TempDir()
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	writeSizedAt(t, filepath.Join(first, "a.bin"), 10, time.Now())
	writeSizedAt(t, filepath.Join(second, "b.bin"), 10, time.Now())
	sink := &memorySink{entryFn: func(entry Entry) error {
		if entry.RootID == "root-2" {
			return io.ErrClosedPipe
		}
		return nil
	}}
	summary, err := Run(context.Background(), Options{
		Roots: []string{first, second}, Backend: BackendSerial, Workers: 1,
	}, sink)
	if !errors.Is(err, io.ErrClosedPipe) || summary.Lifecycle != LifecycleFailed {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if len(summary.Roots) != 2 || summary.Roots[0].Lifecycle != LifecycleComplete ||
		summary.Roots[1].Lifecycle != LifecycleFailed || !summary.Roots[1].Failed {
		t.Fatalf("root summaries=%+v", summary.Roots)
	}
	if summary.EmittedCount != 1 || summary.EmittedApparentBytes != 10 ||
		summary.Roots[0].EmittedApparentBytes != 10 || summary.Roots[1].EmittedApparentBytes != 0 {
		t.Fatalf("emitted accounting=%+v", summary)
	}
	assertRootReconciliation(t, summary)
}

func TestCancellationAddsAffectingGapForEveryIncompleteRoot(t *testing.T) {
	base := t.TempDir()
	first, second, third := filepath.Join(base, "first"), filepath.Join(base, "second"), filepath.Join(base, "third")
	writeSizedAt(t, filepath.Join(first, "a.bin"), 10, time.Now())
	writeSizedAt(t, filepath.Join(second, "b.bin"), 10, time.Now())
	writeSizedAt(t, filepath.Join(third, "c.bin"), 10, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &memorySink{entryFn: func(entry Entry) error {
		if entry.RootID == "root-2" {
			cancel()
		}
		return nil
	}}
	summary, err := Run(ctx, Options{
		Roots: []string{first, second, third}, Backend: BackendSerial, Workers: 1,
	}, sink)
	if err != nil {
		t.Fatalf("cancellation should be represented as inventory evidence: %v", err)
	}
	if summary.Lifecycle != LifecyclePartial || !summary.Canceled || summary.GapCount != 2 {
		t.Fatalf("summary=%+v gaps=%+v", summary, sink.gaps)
	}
	if len(sink.gaps) != 2 || sink.gaps[0].RootID != "root-2" ||
		sink.gaps[1].RootID != "root-3" {
		t.Fatalf("cancellation gaps=%+v", sink.gaps)
	}
	if len(summary.Roots) != 3 || summary.Roots[0].RootID != "root-1" ||
		summary.Roots[1].RootID != "root-2" || summary.Roots[2].RootID != "root-3" {
		t.Fatalf("root order=%+v", summary.Roots)
	}
	if first := summary.Roots[0]; first.Lifecycle != LifecycleComplete || first.Canceled || first.GapCount != 0 {
		t.Fatalf("completed root=%+v", first)
	}
	for _, root := range summary.Roots[1:] {
		if root.Lifecycle != LifecyclePartial || !root.Canceled || root.GapCount != 1 {
			t.Fatalf("incomplete root=%+v", root)
		}
	}
	assertRootReconciliation(t, summary)
}

func TestDeadlineExpiryIsPartialCancellationEvidence(t *testing.T) {
	base := t.TempDir()
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	writeSizedAt(t, filepath.Join(first, "a.bin"), 10, time.Now())
	writeSizedAt(t, filepath.Join(second, "b.bin"), 10, time.Now())
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	sink := &memorySink{}
	summary, err := Run(ctx, Options{
		Roots: []string{first, second}, Backend: BackendSerial, Workers: 1,
	}, sink)
	if err != nil {
		t.Fatalf("deadline expiry should be represented as inventory evidence: %v", err)
	}
	if summary.Lifecycle != LifecyclePartial || !summary.Canceled || summary.GapCount != 2 {
		t.Fatalf("summary=%+v gaps=%+v", summary, sink.gaps)
	}
	if len(sink.gaps) != 2 || sink.gaps[0].RootID != "root-1" ||
		sink.gaps[1].RootID != "root-2" {
		t.Fatalf("deadline gaps=%+v", sink.gaps)
	}
	assertRootReconciliation(t, summary)
}

func TestAllocatedSizeBasisDiffersFromApparentForSparseFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sparse.bin")
	writeSizedAt(t, path, 1<<30, time.Now())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	meta := metadataOf(info)
	if meta.allocated == nil {
		t.Skip("allocated size is unavailable")
	}
	if *meta.allocated >= info.Size()/2 {
		t.Skipf("filesystem did not create a sparse fixture: allocated=%d apparent=%d",
			*meta.allocated, info.Size())
	}
	floor := info.Size() / 2

	apparentSink := &memorySink{}
	apparent, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		MinSize: floor, SizeBasis: SizeApparent,
	}, apparentSink)
	if err != nil {
		t.Fatal(err)
	}
	allocatedSink := &memorySink{}
	allocated, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		MinSize: floor, SizeBasis: SizeAllocated,
	}, allocatedSink)
	if err != nil {
		t.Fatal(err)
	}
	if apparent.MatchedCount != 1 || allocated.MatchedCount != 0 {
		t.Fatalf("apparent=%+v allocated=%+v", apparent, allocated)
	}
	if len(apparentSink.entries) != 1 ||
		apparentSink.entries[0].AllocatedSizeBytes == nil {
		t.Fatalf("entry=%+v", apparentSink.entries)
	}
}

func TestSymlinkDirectoryIsNeverFollowed(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeSizedAt(t, filepath.Join(outside, "outside.bin"), 100, time.Now())
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	writeSizedAt(t, filepath.Join(root, "inside.bin"), 100, time.Now())

	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendParallel, Workers: 4,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if got := relativePaths(sink.entries); !reflect.DeepEqual(got, []string{"inside.bin"}) {
		t.Fatalf("entries=%v", got)
	}
	if summary.VisitedEntries != 2 {
		t.Fatalf("visited=%d want symlink+file", summary.VisitedEntries)
	}
}

func TestRegularFileContentsAreNeverRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sealed.bin")
	writeSizedAt(t, path, 100, time.Now())
	if err := os.Chmod(path, 0); err != nil {
		t.Skipf("chmod unavailable: %v", err)
	}
	defer func() { _ = os.Chmod(path, 0o600) }()

	for _, backend := range []string{BackendSerial, BackendParallel} {
		t.Run(backend, func(t *testing.T) {
			sink := &memorySink{}
			summary, err := Run(context.Background(), Options{
				Roots: []string{root}, Backend: backend, Workers: workersFor(backend),
			}, sink)
			if err != nil {
				t.Fatal(err)
			}
			if summary.Lifecycle != LifecycleComplete ||
				!reflect.DeepEqual(relativePaths(sink.entries), []string{"sealed.bin"}) {
				t.Fatalf("summary=%+v entries=%+v gaps=%+v",
					summary, sink.entries, sink.gaps)
			}
		})
	}
}

func TestDirectoryQueueLimitBoundsBothProductBackends(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeSizedAt(t, filepath.Join(dir, "entry.bin"), 10, time.Now())
	}

	for _, backend := range []string{BackendSerial, BackendParallel} {
		t.Run(backend, func(t *testing.T) {
			sink := &memorySink{}
			summary, err := Run(context.Background(), Options{
				Roots: []string{root}, Backend: backend, Workers: 1,
				MaxOpenDirs: 1, MaxPendingDirs: 1,
			}, sink)
			if err != nil {
				t.Fatal(err)
			}
			if summary.Lifecycle != LifecyclePartial {
				t.Fatalf("lifecycle=%s want partial", summary.Lifecycle)
			}
			if summary.PeakPendingDirectories != 1 {
				t.Fatalf("peak pending=%d want 1", summary.PeakPendingDirectories)
			}
			if summary.QueueLimitSkipCount != 3 || summary.GapCount != 3 {
				t.Fatalf("summary=%+v gaps=%+v", summary, sink.gaps)
			}
			for _, gap := range sink.gaps {
				if gap.Kind != "directory-queue-limit" || !gap.AffectsCompleteness {
					t.Fatalf("gap=%+v", gap)
				}
			}
			if len(sink.headers) != 1 || sink.headers[0].MaxPendingDirs != 1 {
				t.Fatalf("headers=%+v", sink.headers)
			}
		})
	}
}

func TestPermissionGapMakesLifecyclePartial(t *testing.T) {
	root := t.TempDir()
	sealed := filepath.Join(root, "sealed")
	if err := os.Mkdir(sealed, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSizedAt(t, filepath.Join(sealed, "hidden.bin"), 100, time.Now())
	if err := os.Chmod(sealed, 0); err != nil {
		t.Skipf("chmod unavailable: %v", err)
	}
	defer func() { _ = os.Chmod(sealed, 0o755) }()
	if f, err := os.Open(sealed); err == nil {
		_ = f.Close()
		t.Skip("filesystem or test identity does not enforce mode 000")
	}

	sink := &memorySink{}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Lifecycle != LifecyclePartial || summary.GapCount == 0 {
		t.Fatalf("summary=%+v gaps=%+v", summary, sink.gaps)
	}
	if len(sink.gaps) != 1 || sink.gaps[0].Kind != "permission" {
		t.Fatalf("gaps=%+v", sink.gaps)
	}
}

func TestCancellationEmitsPartialTerminalSummary(t *testing.T) {
	root := t.TempDir()
	for i := range 200 {
		writeSizedAt(t, filepath.Join(root, fmt.Sprintf("f-%03d.bin", i)), 10, time.Now())
	}
	ctx, cancel := context.WithCancel(context.Background())
	sink := &memorySink{}
	sink.entryFn = func(Entry) error {
		cancel()
		return nil
	}
	summary, err := Run(ctx, Options{
		Roots: []string{root}, Backend: BackendParallel, Workers: 4,
	}, sink)
	if err != nil {
		t.Fatalf("cancellation should be represented in summary: %v", err)
	}
	if summary.Lifecycle != LifecyclePartial || !summary.Canceled {
		t.Fatalf("summary=%+v", summary)
	}
	if len(sink.summaries) != 1 || len(sink.gaps) == 0 {
		t.Fatalf("summary records=%d gaps=%+v", len(sink.summaries), sink.gaps)
	}
}

func TestOutputFailureCannotEmitFalseCompleteSummary(t *testing.T) {
	root := t.TempDir()
	writeSizedAt(t, filepath.Join(root, "a.bin"), 10, time.Now())
	sink := &memorySink{entryFn: func(Entry) error { return io.ErrClosedPipe }}
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
	}, sink)
	if err == nil || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error=%v", err)
	}
	if summary.Lifecycle != LifecycleFailed || summary.EmittedCount != 0 ||
		summary.EmittedApparentBytes != 0 || summary.EmittedAllocatedBytes != 0 {
		t.Fatalf("summary=%+v", summary)
	}
	if len(sink.summaries) != 0 {
		t.Fatalf("unwritable stream received terminal summary: %+v", sink.summaries)
	}
}

func TestJSONLStreamIsTypedMonotonicAndTerminal(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	writeSizedAt(t, filepath.Join(root, "a.bin"), 10, now)
	writeSizedAt(t, filepath.Join(root, "b.bin"), 20, now)

	var buf bytes.Buffer
	summary, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		Now: now, RunID: "run-test",
	}, NewJSONLSink(&buf))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Lifecycle != LifecycleComplete {
		t.Fatalf("summary=%+v", summary)
	}
	assertJSONLStreamInvariants(t, buf.Bytes(), true)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("records=%d\n%s", len(lines), buf.String())
	}
	wantTypes := []string{RecordHeader, RecordEntry, RecordEntry, RecordSummary}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if record["type"] != wantTypes[i] {
			t.Fatalf("line %d type=%v want=%s", i, record["type"], wantTypes[i])
		}
		if record["run_id"] != "run-test" || int(record["seq"].(float64)) != i {
			t.Fatalf("line %d envelope=%v", i, record)
		}
		if strings.Contains(line, `"execute"`) || strings.Contains(line, `"prunable"`) {
			t.Fatalf("inventory record carries action authority: %s", line)
		}
	}
}

func TestJSONLGoldenProfiles(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	allocated := int64(8)
	header := Header{
		RunID: "golden", CapturedAt: now, Profile: ProfileV0,
		Roots: []Root{{ID: "root-1", Path: "/patient"}},
		Filters: FilterSummary{
			EntryType: "file", SizeBasis: SizeApparent,
			CapturedAt: now.Format(time.RFC3339Nano),
		},
		Backend: BackendSerial, Workers: 1, MaxOpenDirs: 1,
		MaxPendingDirs: DefaultMaxPendingDirs, EmissionMode: EmissionEntries,
		Exclusions: []Exclusion{}, Ordering: "not_promised",
		PathProtection:   PathProtectionSourceStructure,
		MutationContract: MutationContractOpen,
	}
	entry := Entry{
		RootID: "root-1", RelativePath: "a.bin",
		LocalAbsolutePath: "/patient/a.bin", EntryType: "file",
		ApparentSizeBytes: 10, AllocatedSizeBytes: &allocated,
		ModifiedAt: now.Add(-time.Hour), ObservedAt: now,
		MetadataSource: "lstat",
	}
	summary := Summary{
		Lifecycle: LifecycleComplete, Backend: BackendSerial,
		VisitedEntries: 1, VisitedDirectories: 1,
		MatchedCount: 1, EmittedCount: 1,
		MatchedApparentBytes: 10, MatchedAllocatedBytes: 8,
		EmittedApparentBytes: 10, EmittedAllocatedBytes: 8,
		TerminalObservedAt: now,
		EmissionMode:       EmissionEntries,
		Roots: []RootSummary{{
			RootID: "root-1", Lifecycle: LifecycleComplete,
			VisitedEntries: 1, VisitedDirectories: 1, MatchedCount: 1, EmittedCount: 1,
			MatchedApparentBytes: 10, MatchedAllocatedBytes: 8,
			EmittedApparentBytes: 10, EmittedAllocatedBytes: 8,
		}},
	}

	cases := []struct {
		name string
		emit func(*JSONLSink) error
	}{
		{
			name: "complete",
			emit: func(s *JSONLSink) error {
				if err := s.Header(header); err != nil {
					return err
				}
				if err := s.Entry(entry); err != nil {
					return err
				}
				return s.Summary(summary)
			},
		},
		{
			name: "partial-permission",
			emit: func(s *JSONLSink) error {
				if err := s.Header(header); err != nil {
					return err
				}
				if err := s.Gap(Gap{
					RootID: "root-1", RelativePath: "sealed",
					LocalAbsolutePath: "/patient/sealed", Kind: "permission",
					Detail: "permission denied", AffectsCompleteness: true,
				}); err != nil {
					return err
				}
				value := summary
				value.Roots = append([]RootSummary(nil), summary.Roots...)
				value.Lifecycle = LifecyclePartial
				value.VisitedEntries = 0
				value.MatchedCount = 0
				value.EmittedCount = 0
				value.EmittedApparentBytes = 0
				value.EmittedAllocatedBytes = 0
				value.MatchedApparentBytes = 0
				value.MatchedAllocatedBytes = 0
				value.GapCount = 1
				value.Roots[0].Lifecycle = LifecyclePartial
				value.Roots[0].VisitedEntries = 0
				value.Roots[0].MatchedCount = 0
				value.Roots[0].EmittedCount = 0
				value.Roots[0].EmittedApparentBytes = 0
				value.Roots[0].EmittedAllocatedBytes = 0
				value.Roots[0].MatchedApparentBytes = 0
				value.Roots[0].MatchedAllocatedBytes = 0
				value.Roots[0].GapCount = 1
				return s.Summary(value)
			},
		},
		{
			name: "canceled",
			emit: func(s *JSONLSink) error {
				if err := s.Header(header); err != nil {
					return err
				}
				if err := s.Gap(Gap{
					RootID: "root-1", Kind: "canceled", Detail: "enumeration canceled",
					AffectsCompleteness: true,
				}); err != nil {
					return err
				}
				value := summary
				value.Roots = append([]RootSummary(nil), summary.Roots...)
				value.Lifecycle = LifecyclePartial
				value.VisitedEntries = 0
				value.MatchedCount = 0
				value.EmittedCount = 0
				value.EmittedApparentBytes = 0
				value.EmittedAllocatedBytes = 0
				value.MatchedApparentBytes = 0
				value.MatchedAllocatedBytes = 0
				value.GapCount = 1
				value.Canceled = true
				value.Roots[0].Lifecycle = LifecyclePartial
				value.Roots[0].VisitedEntries = 0
				value.Roots[0].MatchedCount = 0
				value.Roots[0].EmittedCount = 0
				value.Roots[0].EmittedApparentBytes = 0
				value.Roots[0].EmittedAllocatedBytes = 0
				value.Roots[0].MatchedApparentBytes = 0
				value.Roots[0].MatchedAllocatedBytes = 0
				value.Roots[0].GapCount = 1
				value.Roots[0].Canceled = true
				return s.Summary(value)
			},
		},
		{
			name: "top-k",
			emit: func(s *JSONLSink) error {
				value := header
				value.Top = 1
				if err := s.Header(value); err != nil {
					return err
				}
				if err := s.Entry(entry); err != nil {
					return err
				}
				terminal := summary
				terminal.Roots = append([]RootSummary(nil), summary.Roots...)
				terminal.VisitedEntries = 2
				terminal.MatchedCount = 2
				terminal.MatchedApparentBytes = 19
				terminal.MatchedAllocatedBytes = 16
				terminal.Top = 1
				terminal.SelectionTruncated = true
				terminal.Roots[0].VisitedEntries = 2
				terminal.Roots[0].MatchedCount = 2
				terminal.Roots[0].MatchedApparentBytes = 19
				terminal.Roots[0].MatchedAllocatedBytes = 16
				return s.Summary(terminal)
			},
		},
		{
			name: "summary-only",
			emit: func(s *JSONLSink) error {
				value := header
				value.EmissionMode = EmissionSummaryOnly
				if err := s.Header(value); err != nil {
					return err
				}
				terminal := summary
				terminal.Roots = append([]RootSummary(nil), summary.Roots...)
				terminal.EmittedCount = 0
				terminal.EmittedApparentBytes = 0
				terminal.EmittedAllocatedBytes = 0
				terminal.EmissionMode = EmissionSummaryOnly
				terminal.EntriesSuppressed = true
				terminal.Roots[0].EmittedCount = 0
				terminal.Roots[0].EmittedApparentBytes = 0
				terminal.Roots[0].EmittedAllocatedBytes = 0
				return s.Summary(terminal)
			},
		},
		{
			name: "excluded-subtree",
			emit: func(s *JSONLSink) error {
				value := header
				value.Exclusions = []Exclusion{{Kind: "root_relative", Value: "generated"}}
				if err := s.Header(value); err != nil {
					return err
				}
				if err := s.Entry(entry); err != nil {
					return err
				}
				terminal := summary
				terminal.Roots = append([]RootSummary(nil), summary.Roots...)
				terminal.ExclusionCount = 1
				terminal.Roots[0].ExclusionCount = 1
				return s.Summary(terminal)
			},
		},
		{
			name: "multi-root-canceled",
			emit: func(s *JSONLSink) error {
				value := header
				value.Roots = []Root{
					{ID: "root-1", Path: "/patient-a"},
					{ID: "root-2", Path: "/patient-b"},
					{ID: "root-3", Path: "/patient-c"},
				}
				if err := s.Header(value); err != nil {
					return err
				}
				first := entry
				first.LocalAbsolutePath = "/patient-a/a.bin"
				if err := s.Entry(first); err != nil {
					return err
				}
				for _, rootID := range []string{"root-2", "root-3"} {
					if err := s.Gap(Gap{
						RootID: rootID, Kind: "canceled", Detail: "enumeration canceled",
						AffectsCompleteness: true,
					}); err != nil {
						return err
					}
				}
				terminal := summary
				terminal.Roots = []RootSummary{
					{RootID: "root-1", Lifecycle: LifecycleComplete, VisitedEntries: 1,
						VisitedDirectories: 1, MatchedCount: 1, EmittedCount: 1,
						EmittedApparentBytes: 10, EmittedAllocatedBytes: 8,
						MatchedApparentBytes: 10, MatchedAllocatedBytes: 8},
					{RootID: "root-2", Lifecycle: LifecyclePartial, VisitedDirectories: 1,
						GapCount: 1, Canceled: true},
					{RootID: "root-3", Lifecycle: LifecyclePartial, GapCount: 1, Canceled: true},
				}
				terminal.Lifecycle = LifecyclePartial
				terminal.VisitedEntries = 1
				terminal.VisitedDirectories = 2
				terminal.MatchedCount = 1
				terminal.EmittedCount = 1
				terminal.EmittedApparentBytes = 10
				terminal.EmittedAllocatedBytes = 8
				terminal.MatchedApparentBytes = 10
				terminal.MatchedAllocatedBytes = 8
				terminal.GapCount = 2
				terminal.Canceled = true
				return s.Summary(terminal)
			},
		},
		{
			name: "multi-root",
			emit: func(s *JSONLSink) error {
				value := header
				value.Roots = []Root{
					{ID: "root-1", Path: "/patient-a"},
					{ID: "root-2", Path: "/patient-b"},
				}
				if err := s.Header(value); err != nil {
					return err
				}
				first := entry
				first.LocalAbsolutePath = "/patient-a/a.bin"
				if err := s.Entry(first); err != nil {
					return err
				}
				second := entry
				second.RootID = "root-2"
				second.RelativePath = "b.bin"
				second.LocalAbsolutePath = "/patient-b/b.bin"
				second.ApparentSizeBytes = 20
				if err := s.Entry(second); err != nil {
					return err
				}
				terminal := summary
				terminal.Roots = append([]RootSummary(nil), summary.Roots...)
				terminal.VisitedEntries = 2
				terminal.VisitedDirectories = 2
				terminal.MatchedCount = 2
				terminal.EmittedCount = 2
				terminal.EmittedApparentBytes = 30
				terminal.EmittedAllocatedBytes = 16
				terminal.MatchedApparentBytes = 30
				terminal.MatchedAllocatedBytes = 16
				terminal.Roots = []RootSummary{
					{RootID: "root-1", Lifecycle: LifecycleComplete, VisitedEntries: 1,
						VisitedDirectories: 1, MatchedCount: 1, EmittedCount: 1,
						MatchedApparentBytes: 10, MatchedAllocatedBytes: 8,
						EmittedApparentBytes: 10, EmittedAllocatedBytes: 8},
					{RootID: "root-2", Lifecycle: LifecycleComplete, VisitedEntries: 1,
						VisitedDirectories: 1, MatchedCount: 1, EmittedCount: 1,
						MatchedApparentBytes: 20, MatchedAllocatedBytes: 8,
						EmittedApparentBytes: 20, EmittedAllocatedBytes: 8},
				}
				return s.Summary(terminal)
			},
		},
		{
			name: "vanished",
			emit: func(s *JSONLSink) error {
				if err := s.Header(header); err != nil {
					return err
				}
				if err := s.Gap(Gap{
					RootID: "root-1", RelativePath: "gone.bin",
					LocalAbsolutePath: "/patient/gone.bin", Kind: "vanished",
					Detail: "file does not exist", AffectsCompleteness: true,
				}); err != nil {
					return err
				}
				value := summary
				value.Roots = append([]RootSummary(nil), summary.Roots...)
				value.Lifecycle = LifecyclePartial
				value.VisitedEntries = 0
				value.MatchedCount = 0
				value.EmittedCount = 0
				value.EmittedApparentBytes = 0
				value.EmittedAllocatedBytes = 0
				value.MatchedApparentBytes = 0
				value.MatchedAllocatedBytes = 0
				value.GapCount = 1
				value.VanishedCount = 1
				value.Roots[0].Lifecycle = LifecyclePartial
				value.Roots[0].VisitedEntries = 0
				value.Roots[0].MatchedCount = 0
				value.Roots[0].EmittedCount = 0
				value.Roots[0].EmittedApparentBytes = 0
				value.Roots[0].EmittedAllocatedBytes = 0
				value.Roots[0].MatchedApparentBytes = 0
				value.Roots[0].MatchedAllocatedBytes = 0
				value.Roots[0].GapCount = 1
				value.Roots[0].VanishedCount = 1
				return s.Summary(value)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			sink := NewJSONLSink(&buf)
			sink.now = func() time.Time { return now }
			if err := tc.emit(sink); err != nil {
				t.Fatal(err)
			}
			assertJSONLStreamInvariants(t, buf.Bytes(), true)
			assertGoldenJSONL(t, tc.name, buf.String())
		})
	}

	t.Run("output-failure", func(t *testing.T) {
		var buf bytes.Buffer
		writer := &failAfterNewlinesWriter{writer: &buf, remaining: 1}
		sink := NewJSONLSink(writer)
		sink.now = func() time.Time { return now }
		if err := sink.Header(header); err != nil {
			t.Fatal(err)
		}
		if err := sink.Entry(entry); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("entry error=%v", err)
		}
		assertJSONLStreamInvariants(t, buf.Bytes(), false)
		assertGoldenJSONL(t, "output-failure", buf.String())
	})
}

func TestRun_HeaderEmitsMutationContract(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "patient")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSizedAt(t, filepath.Join(root, "data.bin"), 128, time.Now())
	for _, want := range []string{MutationContractOpen, MutationContractReadOnly} {
		sink := &memorySink{}
		_, err := Run(context.Background(), Options{
			Roots: []string{root}, Backend: BackendSerial, Workers: 1,
			MutationContract: want,
		}, sink)
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		if len(sink.headers) != 1 || sink.headers[0].MutationContract != want {
			t.Fatalf("%s: headers=%+v", want, sink.headers)
		}
	}
}

func TestRun_JSONLSerializesMutationContractAndValidatesSchema(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "patient")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSizedAt(t, filepath.Join(root, "data.bin"), 128, time.Now())
	for _, want := range []string{MutationContractOpen, MutationContractReadOnly} {
		var buf bytes.Buffer
		sink := NewJSONLSink(&buf)
		_, err := Run(context.Background(), Options{
			Roots: []string{root}, Backend: BackendSerial, Workers: 1,
			MutationContract: want, EmissionMode: EmissionSummaryOnly,
		}, sink)
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		// First JSONL line is the header record; must carry mutation_contract.
		first, _, _ := strings.Cut(buf.String(), "\n")
		if !strings.Contains(first, `"mutation_contract":"`+want+`"`) {
			t.Fatalf("%s: header JSON missing contract: %s", want, first)
		}
		// Schema-validate every record in the stream.
		for i, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			if err := contract.ValidateJSON(spanwitschema.SpanwitFilesystemInventoryV0, []byte(line)); err != nil {
				t.Fatalf("%s record %d: %v\n%s", want, i+1, err, line)
			}
		}
	}
}

func TestRunCreatesNoApplicationFiles(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "patient")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSizedAt(t, filepath.Join(root, "data.bin"), 128, time.Now())
	before := snapshotFiles(t, base)

	_, err := Run(context.Background(), Options{
		Roots: []string{root}, Backend: BackendParallel, Workers: 4,
	}, &memorySink{})
	if err != nil {
		t.Fatal(err)
	}
	after := snapshotFiles(t, base)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("inventory changed patient tree:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestParseSizeAndAge(t *testing.T) {
	for raw, want := range map[string]int64{
		"": 0, "1": 1, "1K": 1 << 10, "5GiB": 5 << 30, "2tb": 2 << 40,
	} {
		got, err := ParseSize(raw)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q)=%d,%v want=%d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"-1G", "1XB", "1.5G"} {
		if _, err := ParseSize(raw); err == nil {
			t.Errorf("ParseSize(%q) accepted", raw)
		}
	}
	if got, err := ParseAge("30d"); err != nil || got != 30*24*time.Hour {
		t.Fatalf("ParseAge=%v,%v", got, err)
	}
	if _, err := ParseAge("1y"); err == nil {
		t.Fatal("unsupported year unit accepted")
	}
}

func TestMountBoundaryRecordDoesNotMakeDeclaredScopePartial(t *testing.T) {
	sink := &memorySink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := newRunState(ctx, cancel, normalizedOptions{
		Options: Options{Backend: BackendSerial},
	}, sink)
	state.addGap(Gap{
		Kind: "mount-boundary", AffectsCompleteness: false,
	})
	summary := state.summary(time.Second, time.Now().UTC())
	if summary.BoundarySkipCount != 1 || summary.GapCount != 0 ||
		summary.Lifecycle != LifecycleComplete {
		t.Fatalf("summary=%+v", summary)
	}
}

func assertGoldenJSONL(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "jsonl", name+".jsonl")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\nactual:\n%s", path, err, got)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch %s\nwant:\n%s\ngot:\n%s", path, want, got)
	}
}

func assertJSONLStreamInvariants(t *testing.T, stream []byte, terminalExpected bool) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(stream), []byte{'\n'})
	if len(lines) == 0 || len(lines[0]) == 0 {
		t.Fatal("empty JSONL stream")
	}

	type envelope struct {
		Type  string          `json:"type"`
		RunID string          `json:"run_id"`
		Seq   int64           `json:"seq"`
		TS    string          `json:"ts"`
		Data  json.RawMessage `json:"data"`
	}

	var (
		header              Header
		summary             Summary
		headerCount         int
		summaryCount        int
		summaryTS           string
		entryCount          int64
		affectingGapCount   int64
		boundaryGapCount    int64
		vanishedRecordCount int64
		runID               string
	)
	for i, line := range lines {
		var record envelope
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("record %d is not JSON: %v", i, err)
		}
		if record.Seq != int64(i) {
			t.Fatalf("record %d seq=%d", i, record.Seq)
		}
		if record.RunID == "" {
			t.Fatalf("record %d has empty run_id", i)
		}
		if i == 0 {
			runID = record.RunID
		} else if record.RunID != runID {
			t.Fatalf("record %d run_id=%q want %q", i, record.RunID, runID)
		}

		switch record.Type {
		case RecordHeader:
			headerCount++
			if i != 0 {
				t.Fatalf("header appears at record %d", i)
			}
			if err := json.Unmarshal(record.Data, &header); err != nil {
				t.Fatalf("decode header: %v", err)
			}
		case RecordEntry:
			entryCount++
		case RecordGap:
			var gap Gap
			if err := json.Unmarshal(record.Data, &gap); err != nil {
				t.Fatalf("decode gap: %v", err)
			}
			switch {
			case gap.Kind == "mount-boundary" && !gap.AffectsCompleteness:
				boundaryGapCount++
			case gap.AffectsCompleteness:
				affectingGapCount++
			}
			if gap.Kind == "vanished" {
				vanishedRecordCount++
			}
		case RecordSummary:
			summaryCount++
			if i != len(lines)-1 {
				t.Fatalf("summary appears at record %d of %d", i, len(lines))
			}
			if err := json.Unmarshal(record.Data, &summary); err != nil {
				t.Fatalf("decode summary: %v", err)
			}
			summaryTS = record.TS
		default:
			t.Fatalf("record %d has unknown type %q", i, record.Type)
		}
	}

	if headerCount != 1 {
		t.Fatalf("header count=%d", headerCount)
	}
	if !terminalExpected {
		if summaryCount != 0 {
			t.Fatalf("unwritable stream has %d summaries", summaryCount)
		}
		return
	}
	if summaryCount != 1 {
		t.Fatalf("summary count=%d", summaryCount)
	}
	if summary.Backend != header.Backend {
		t.Fatalf("summary backend=%q header backend=%q", summary.Backend, header.Backend)
	}
	if summary.TerminalObservedAt.IsZero() || summaryTS != summary.TerminalObservedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("terminal observation=%s summary envelope ts=%s", summary.TerminalObservedAt, summaryTS)
	}
	if summary.EmittedCount != entryCount {
		t.Fatalf("emitted_count=%d entry records=%d", summary.EmittedCount, entryCount)
	}
	if summary.GapCount != affectingGapCount {
		t.Fatalf("gap_count=%d affecting gap records=%d",
			summary.GapCount, affectingGapCount)
	}
	if summary.BoundarySkipCount != boundaryGapCount {
		t.Fatalf("boundary_skip_count=%d boundary gap records=%d",
			summary.BoundarySkipCount, boundaryGapCount)
	}
	if summary.VanishedCount != vanishedRecordCount {
		t.Fatalf("vanished_count=%d vanished records=%d",
			summary.VanishedCount, vanishedRecordCount)
	}
	if summary.MatchedCount < summary.EmittedCount {
		t.Fatalf("matched_count=%d emitted_count=%d",
			summary.MatchedCount, summary.EmittedCount)
	}
	if header.EmissionMode == EmissionEntries && summary.Top == 0 && summary.MatchedCount != summary.EmittedCount {
		t.Fatalf("stream-all matched_count=%d emitted_count=%d",
			summary.MatchedCount, summary.EmittedCount)
	}
	if header.EmissionMode == EmissionSummaryOnly && (summary.EmittedCount != 0 || !summary.EntriesSuppressed) {
		t.Fatalf("summary-only emitted=%d suppressed=%v", summary.EmittedCount, summary.EntriesSuppressed)
	}
	if summary.Top > 0 && summary.EmittedCount > int64(summary.Top) {
		t.Fatalf("top=%d emitted_count=%d", summary.Top, summary.EmittedCount)
	}
	selectedCount := summary.MatchedCount
	if summary.Top > 0 && selectedCount > int64(summary.Top) {
		selectedCount = int64(summary.Top)
	}
	wantTruncated := summary.Top > 0 && summary.MatchedCount > selectedCount
	if summary.SelectionTruncated != wantTruncated {
		t.Fatalf("selection_truncated=%v want %v", summary.SelectionTruncated, wantTruncated)
	}
	assertRootReconciliation(t, summary)
}

type failAfterNewlinesWriter struct {
	writer    io.Writer
	remaining int
}

func (w *failAfterNewlinesWriter) Write(p []byte) (int, error) {
	if w.remaining == 0 {
		return 0, io.ErrClosedPipe
	}
	w.remaining -= bytes.Count(p, []byte{'\n'})
	return w.writer.Write(p)
}

func workersFor(backend string) int {
	if backend == BackendSerial {
		return 1
	}
	return 4
}

func relativePaths(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.RelativePath)
	}
	sort.Strings(out)
	return out
}

func assertRootReconciliation(t *testing.T, summary Summary) {
	t.Helper()
	var visitedEntries, visitedDirectories, matched, emitted, apparent, allocated int64
	var emittedApparent, emittedAllocated, emittedUnmeasured int64
	var unmeasured, gaps, boundaries, exclusions, vanished, queue int64
	for _, root := range summary.Roots {
		visitedEntries += root.VisitedEntries
		visitedDirectories += root.VisitedDirectories
		matched += root.MatchedCount
		emitted += root.EmittedCount
		emittedApparent += root.EmittedApparentBytes
		emittedAllocated += root.EmittedAllocatedBytes
		emittedUnmeasured += root.EmittedUnmeasuredCount
		apparent += root.MatchedApparentBytes
		allocated += root.MatchedAllocatedBytes
		unmeasured += root.AllocatedUnmeasuredCount
		gaps += root.GapCount
		boundaries += root.BoundarySkipCount
		exclusions += root.ExclusionCount
		vanished += root.VanishedCount
		queue += root.QueueLimitSkipCount
	}
	if len(summary.Roots) == 0 || visitedEntries != summary.VisitedEntries ||
		visitedDirectories != summary.VisitedDirectories || matched != summary.MatchedCount ||
		emitted != summary.EmittedCount || apparent != summary.MatchedApparentBytes ||
		allocated != summary.MatchedAllocatedBytes || unmeasured != summary.AllocatedUnmeasuredCount ||
		emittedApparent != summary.EmittedApparentBytes ||
		emittedAllocated != summary.EmittedAllocatedBytes ||
		emittedUnmeasured != summary.EmittedUnmeasuredCount ||
		gaps != summary.GapCount || boundaries != summary.BoundarySkipCount ||
		exclusions != summary.ExclusionCount || vanished != summary.VanishedCount ||
		queue != summary.QueueLimitSkipCount {
		t.Fatalf("global summary does not reconcile root rows: %+v", summary)
	}
}

func writeSizedAt(t *testing.T, path string, size int64, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

type fileSnapshot struct {
	size    int64
	modTime int64
	mode    os.FileMode
}

func snapshotFiles(t *testing.T, root string) map[string]fileSnapshot {
	t.Helper()
	out := map[string]fileSnapshot{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out[path] = fileSnapshot{
			size: info.Size(), modTime: info.ModTime().UnixNano(), mode: info.Mode(),
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
