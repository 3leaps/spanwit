package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/config"
)

const sparseLogical = int64(1) << 30

// sparseCargoFixture builds root/app/{Cargo.toml,target/debug/disk.img}
// where disk.img is 1 GiB logical and (on a sparse-capable filesystem)
// almost nothing allocated. The tree is aged 3h so the handoff floor passes.
func sparseCargoFixture(t *testing.T) (root, target string, at time.Time) {
	t.Helper()
	if !AllocatedSizesSupported {
		t.Skip("build cannot read allocated sizes")
	}
	root = t.TempDir()
	writeFile(t, filepath.Join(root, "app", "Cargo.toml"), 32)
	target = filepath.Join(root, "app", "target")
	img := filepath.Join(target, "debug", "disk.img")
	if err := os.MkdirAll(filepath.Dir(img), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(img)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(sparseLogical); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := DirSizeContext(context.Background(), target, -1)
	if err != nil {
		t.Fatal(err)
	}
	if r.AllocatedBytes >= sparseLogical/2 {
		t.Skipf("filesystem does not keep sparse files sparse (allocated %d)", r.AllocatedBytes)
	}
	at = time.Now().Add(-3 * time.Hour)
	setTreeTime(t, target, at)
	return root, target, at
}

// measuredBaseline is what a space scan on the default basis would carry
// for dir right now.
func measuredBaseline(t *testing.T, dir string, at time.Time) *ExactBaseline {
	t.Helper()
	r, err := DirSizeContext(context.Background(), dir, -1)
	if err != nil {
		t.Fatal(err)
	}
	logical := r.Bytes
	b := &ExactBaseline{
		SizeBasis:      SizeBasisAllocated,
		SizeBytes:      r.AllocatedBytes,
		LogicalBytes:   &logical,
		LastActivityAt: at.UTC().Format(time.RFC3339Nano),
		ActivityBasis:  ActivityBasisDescendantMtime,
	}
	return b
}

func sparseConfig(root, minSize string) *config.Config {
	return &config.Config{
		Version:  1,
		Defaults: config.Defaults{MinSize: minSize},
		Paths: []config.PathProfile{{
			Path:     root,
			MaxDepth: 4,
			Targets:  []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}
}

func TestDirSize_SparseFileAllocatedBelowLogical(t *testing.T) {
	_, target, _ := sparseCargoFixture(t)
	r, err := DirSizeContext(context.Background(), target, -1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Bytes != sparseLogical {
		t.Fatalf("logical=%d want %d", r.Bytes, sparseLogical)
	}
	if r.AllocatedUnmeasured || r.Incomplete {
		t.Fatalf("allocation should be fully measured: %+v", r)
	}
}

// Acceptance: a sparse candidate (logical 1G, allocated ~0) reports the
// allocated figure as its headline, with logical as the companion.
func TestBuildPrunePlan_SparseHeadlineIsAllocated(t *testing.T) {
	root, target, _ := sparseCargoFixture(t)
	plan, err := BuildPrunePlan(context.Background(), sparseConfig(root, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Path != target {
		t.Fatalf("candidates=%+v", plan.Candidates)
	}
	c := plan.Candidates[0]
	if plan.SizeBasis != SizeBasisAllocated || c.SizeBasis != SizeBasisAllocated {
		t.Fatalf("basis plan=%q row=%q", plan.SizeBasis, c.SizeBasis)
	}
	if c.Size != c.AllocatedBytes || c.Size >= sparseLogical/2 {
		t.Fatalf("headline %d should be the allocated figure (%d), far below logical", c.Size, c.AllocatedBytes)
	}
	if c.LogicalBytes < sparseLogical || c.LogicalIncomplete || c.SizeIncomplete {
		t.Fatalf("logical=%d incomplete=%v/%v", c.LogicalBytes, c.LogicalIncomplete, c.SizeIncomplete)
	}
	if plan.TotalSize != c.Size {
		t.Fatalf("TotalSize %d should be on the allocated basis (%d)", plan.TotalSize, c.Size)
	}
	if note := LogicalNote(c.SizeBasis, c.Size, &c.LogicalBytes, c.SizeIncomplete); !strings.Contains(note, "1.0G logical") {
		t.Fatalf("text note = %q", note)
	}
}

// Acceptance: min_size between the two sums (allocated < 1M <= logical)
// withholds the row at scan time and on replay of that plan alike. A legacy
// (apparent) carrier of the same tree decides on logical bytes instead.
func TestSparseMinSize_ScanAndReplayAgree(t *testing.T) {
	root, target, at := sparseCargoFixture(t)
	plan, err := BuildPrunePlan(context.Background(), sparseConfig(root, "1M"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("candidates=%+v", plan.Candidates)
	}
	wantState(t, plan.Candidates[0], CandidateStateWithheld, WithheldReasonMinSize)

	entry := func(b *ExactBaseline) ExactEntry {
		e := cargoEntry(target, FilterDisplayNone, b)
		e.EffectiveMinSize = "1M"
		return e
	}
	replay, c := planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{entry(measuredBaseline(t, target, at))}})
	if replay.SizeBasis != SizeBasisAllocated {
		t.Fatalf("replay basis=%q", replay.SizeBasis)
	}
	wantState(t, c, CandidateStateWithheld, WithheldReasonMinSize)
	wantDrift(t, c, DriftUnchanged, DriftUnchanged)

	legacy := baselineOf(sparseLogical, at, time.RFC3339Nano)
	replay, c = planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{entry(legacy)}})
	if replay.SizeBasis != SizeBasisApparent || c.SizeBasis != SizeBasisApparent {
		t.Fatalf("legacy replay basis plan=%q row=%q", replay.SizeBasis, c.SizeBasis)
	}
	wantState(t, c, CandidateStatePrunable, "")
	if c.Drift.SizeAllocated != "" || c.Drift.SizeLogical != DriftUnchanged {
		t.Fatalf("legacy carrier has no allocated axis: %+v", c.Drift)
	}
}

// Acceptance: appending within the last block leaves the allocated sum
// unchanged, but the logical axis still sees it, so the row is size drift.
func TestFreshness_SubBlockAppendIsSizeDrift(t *testing.T) {
	if !AllocatedSizesSupported {
		t.Skip("build cannot read allocated sizes")
	}
	root, target := cargoFixture(t)
	f := filepath.Join(target, "debug", "small")
	if err := os.WriteFile(f, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-3 * time.Hour)
	setTreeTime(t, target, at)
	b := measuredBaseline(t, target, at)
	if err := os.WriteFile(f, make([]byte, 110), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f, at, at); err != nil {
		t.Fatal(err)
	}
	plan, c := planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{cargoEntry(target, FilterDisplayNone, b)}})
	if c.Drift.SizeAllocated != DriftUnchanged {
		t.Fatalf("a sub-block append should not move the allocated sum: %+v (alloc %d vs %d)", c.Drift, c.AllocatedBytes, b.SizeBytes)
	}
	if c.Drift.SizeLogical != DriftChanged {
		t.Fatalf("logical axis should see the append: %+v", c.Drift)
	}
	wantState(t, c, CandidateStateWithheld, WithheldReasonSizeDrift)
	wantDrift(t, c, DriftChanged, DriftUnchanged)
	if c.Drift.SizeDeltaBytes == nil || *c.Drift.SizeDeltaBytes != 0 {
		t.Fatalf("delta is on the allocated basis and should be 0: %v", c.Drift.SizeDeltaBytes)
	}
	if plan.Freshness.SizeDrift != 1 {
		t.Fatalf("freshness=%+v", plan.Freshness)
	}
}

// Acceptance: an allocated baseline replayed where allocation cannot be
// measured leaves that axis unknown. Logical is still compared, and an
// unmeasured axis is never size drift by itself.
func TestCompareDrift_AllocatedUnmeasuredReplayIsUnknown(t *testing.T) {
	logical := int64(100)
	b := &ExactBaseline{SizeBasis: SizeBasisAllocated, SizeBytes: 4096, LogicalBytes: &logical}
	c := PruneCandidate{
		Path: "/x", State: CandidateStatePrunable, SizeBasis: SizeBasisApparent,
		Size: 100, LogicalBytes: 100, AllocatedIncomplete: true,
	}
	f := NewFreshness("")
	cands := []PruneCandidate{c}
	applyDrift(cands, map[string]*ExactBaseline{candidatePathKey("/x"): b}, f)
	d := cands[0].Drift
	if d.SizeAllocated != DriftUnknown || d.SizeLogical != DriftUnchanged || d.Size != DriftUnknown {
		t.Fatalf("drift=%+v", d)
	}
	if cands[0].State != CandidateStatePrunable || f.SizeDrift != 0 || f.DriftUnknown != 1 {
		t.Fatalf("unknown must not withhold: state=%s freshness=%+v", cands[0].State, f)
	}
	if d.SizeDeltaBytes == nil || *d.SizeDeltaBytes != 0 {
		t.Fatalf("delta follows the row basis (apparent): %v", d.SizeDeltaBytes)
	}

	// Logical growth is still caught on the same host.
	c.LogicalBytes, c.Size = 200, 200
	cands = []PruneCandidate{c}
	applyDrift(cands, map[string]*ExactBaseline{candidatePathKey("/x"): b}, NewFreshness(""))
	wantState(t, cands[0], CandidateStateWithheld, WithheldReasonSizeDrift)
}

func TestCombineAxes(t *testing.T) {
	for _, tc := range []struct {
		axes []string
		want string
	}{
		{[]string{DriftUnchanged, ""}, DriftUnchanged},
		{[]string{DriftUnchanged, DriftUnchanged}, DriftUnchanged},
		{[]string{DriftUnchanged, DriftUnknown}, DriftUnknown},
		{[]string{DriftChanged, DriftUnknown}, DriftChanged},
		{[]string{DriftUnknown, DriftChanged}, DriftChanged},
		{[]string{"", ""}, DriftUnknown},
	} {
		if got := combineAxes(tc.axes...); got != tc.want {
			t.Errorf("combineAxes(%q)=%s want %s", tc.axes, got, tc.want)
		}
	}
}

// One basis per plan: a build without st_blocks reports every row apparent,
// and an allocated plan's min_size stays decided on allocated bytes, which
// that build cannot measure (a lower bound, so indeterminate below floor).
func TestResolveSizePolicyFor(t *testing.T) {
	for _, tc := range []struct {
		requested       string
		supported       bool
		headline, gate  string
		wantErrContains string
	}{
		{"", true, SizeBasisAllocated, SizeBasisAllocated, ""},
		{"", false, SizeBasisApparent, SizeBasisApparent, ""},
		{SizeBasisAllocated, true, SizeBasisAllocated, SizeBasisAllocated, ""},
		{SizeBasisAllocated, false, SizeBasisApparent, SizeBasisAllocated, ""},
		{SizeBasisApparent, true, SizeBasisApparent, SizeBasisApparent, ""},
		{"blocks", true, "", "", "invalid size basis"},
	} {
		p, err := resolveSizePolicyFor(tc.requested, tc.supported)
		if tc.wantErrContains != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("%q: err=%v", tc.requested, err)
			}
			continue
		}
		if err != nil || p.headline != tc.headline || p.gate != tc.gate {
			t.Errorf("%q supported=%v: got %+v err=%v", tc.requested, tc.supported, p, err)
		}
	}
	unmeasured := SizeResult{Bytes: 2 << 20, AllocatedUnmeasured: true}
	if _, incomplete := onBasis(unmeasured, SizeBasisAllocated); !incomplete {
		t.Fatal("unmeasured allocation must be a lower bound")
	}
	if _, incomplete := onBasis(unmeasured, SizeBasisApparent); incomplete {
		t.Fatal("logical sum is complete")
	}
}

// Rows on a headline basis that differs from the gate basis are still all
// reported on the headline basis (no mixing within one plan).
func TestSizeCandidateJobs_SplitPolicyNeverMixesRows(t *testing.T) {
	root, target := cargoFixture(t)
	other := filepath.Join(root, "two", "target")
	writeFile(t, filepath.Join(root, "two", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(other, "debug", "b"), 100)
	// other holds 100 logical bytes in one allocated block: a 1000-byte
	// floor sits between the two sums, so the gate basis decides it.
	jobs := []CandidateJob{
		{Path: target, State: CandidateStatePrunable, ReclaimScope: ReclaimScopeWhole},
		{Path: other, State: CandidateStatePrunable, ReclaimScope: ReclaimScopeWhole, MinSize: 1000, EffectiveMinSize: "1000B"},
	}
	cands, warnings := sizeCandidateJobs(context.Background(), jobs, -1, sizePolicy{headline: SizeBasisApparent, gate: SizeBasisAllocated})
	if len(warnings) != 0 || len(cands) != 2 {
		t.Fatalf("cands=%+v warnings=%v", cands, warnings)
	}
	for _, c := range cands {
		if c.SizeBasis != SizeBasisApparent || c.Size != c.LogicalBytes {
			t.Fatalf("row not on the headline basis: %+v", c)
		}
		if c.Path != other {
			continue
		}
		wantGate := CandidateStatePrunable
		if !AllocatedSizesSupported {
			// Allocation unreadable: the allocated gate is a lower bound.
			wantGate = CandidateStateWithheld
		}
		if c.State != wantGate || c.Size >= 1000 {
			t.Fatalf("min_size must be decided on the gate basis (allocated %d), not the headline (%d): %+v", c.AllocatedBytes, c.Size, c)
		}
	}
}

func TestLogicalNote(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	for _, tc := range []struct {
		name       string
		basis      string
		headline   int64
		logical    *int64
		incomplete bool
		want       string
	}{
		{"exactly 10% apart is hidden", SizeBasisAllocated, 90, n(100), false, ""},
		{"just over 10% is shown", SizeBasisAllocated, 89 << 20, n(100 << 20), false, "  (100.0M logical)"},
		{"allocated above logical", SizeBasisAllocated, 125 << 20, n(100 << 20), false, "  (100.0M logical)"},
		{"lower bound marked", SizeBasisAllocated, 1 << 20, n(2 << 30), true, "  (≥2.0G logical)"},
		{"apparent rows never annotate", SizeBasisApparent, 1, n(1 << 30), false, ""},
		{"no logical carried", SizeBasisAllocated, 1, nil, false, ""},
		{"both zero", SizeBasisAllocated, 0, n(0), false, ""},
	} {
		if got := LogicalNote(tc.basis, tc.headline, tc.logical, tc.incomplete); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestExactPlanSizeBasis(t *testing.T) {
	alloc := &ExactBaseline{SizeBasis: SizeBasisAllocated}
	legacy := &ExactBaseline{}
	for _, tc := range []struct {
		name    string
		entries []ExactEntry
		want    string
		wantErr string
	}{
		{"none carried", []ExactEntry{{Path: "/a"}}, "", ""},
		{"legacy is apparent", []ExactEntry{{Path: "/a", Baseline: legacy}}, SizeBasisApparent, ""},
		{"allocated", []ExactEntry{{Path: "/a", Baseline: alloc}, {Path: "/b", Baseline: alloc}}, SizeBasisAllocated, ""},
		{"mixed refused", []ExactEntry{{Path: "/a", Baseline: alloc}, {Path: "/b", Baseline: legacy}}, "", "mixes size bases"},
		{"unknown refused", []ExactEntry{{Path: "/a", Baseline: &ExactBaseline{SizeBasis: "blocks"}}}, "", "invalid size_basis"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := exactPlanSizeBasis(tc.entries)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q err=%v", got, err)
			}
		})
	}
}

// A mixed-basis plan fails closed before anything is sized.
func TestPlanFromExact_MixedBasesFailClosed(t *testing.T) {
	root, target := cargoFixture(t)
	other := filepath.Join(root, "two", "target")
	writeFile(t, filepath.Join(root, "two", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(other, "debug", "b"), 100)
	now := time.Now()
	alloc := baselineOf(4096, now, time.RFC3339Nano)
	alloc.SizeBasis = SizeBasisAllocated
	_, err := PlanFromExact(context.Background(), ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{
		cargoEntry(target, FilterDisplayNone, alloc),
		cargoEntry(other, FilterDisplayNone, baselineOf(100, now, time.RFC3339Nano)),
	}})
	if err == nil || !strings.Contains(err.Error(), "mixes size bases") {
		t.Fatalf("err=%v", err)
	}
}
