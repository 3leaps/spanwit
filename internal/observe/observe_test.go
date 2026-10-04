package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestHysteresis_NoAlertStorm(t *testing.T) {
	// Enter critical
	l := NextLevel(LevelOK, 5*gi, 96)
	if l != LevelCritical {
		t.Fatalf("enter critical: %s", l)
	}
	// Stay critical while still in band
	l = NextLevel(LevelCritical, 9*gi, 94)
	if l != LevelCritical {
		t.Fatalf("stay critical: %s", l)
	}
	// Leave critical only past leave band
	l = NextLevel(LevelCritical, 15*gi, 90)
	if l != LevelOK && l != LevelWarn {
		t.Fatalf("leave critical: %s", l)
	}
	if ShouldNotify(LevelCritical, LevelCritical) {
		t.Fatal("no notify when level unchanged")
	}
	if !ShouldNotify(LevelOK, LevelCritical) {
		t.Fatal("notify on change")
	}
}

func TestGrowth_PositiveAndIndeterminate(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	a := Sample{
		CapturedAt: base, VolumeID: "vol-1", Basis: BasisStatfs,
		AvailBytes: 100 * gi, SizeComplete: true, Coverage: CoverageComplete, Seq: 1,
	}
	b := Sample{
		CapturedAt: base.Add(time.Hour), VolumeID: "vol-1", Basis: BasisStatfs,
		AvailBytes: 90 * gi, SizeComplete: true, Coverage: CoverageComplete, Seq: 2,
	}
	g := ComputeGrowth(a, b)
	if g.Status != "measured" || g.RateBps == nil || *g.RateBps <= 0 {
		t.Fatalf("expected positive fill rate: %+v", g)
	}
	// identity mismatch
	b.VolumeID = "vol-2"
	g = ComputeGrowth(a, b)
	if g.Status != CoverageUnavailable {
		t.Fatalf("mismatch: %+v", g)
	}
	// zero interval
	b.VolumeID = "vol-1"
	b.CapturedAt = a.CapturedAt
	g = ComputeGrowth(a, b)
	if g.Status != CoverageUnavailable {
		t.Fatalf("zero interval: %+v", g)
	}
	// incomplete size
	b.CapturedAt = a.CapturedAt.Add(time.Hour)
	b.SizeComplete = false
	g = ComputeGrowth(a, b)
	if g.Status != CoverageIndeterminate {
		t.Fatalf("incomplete: %+v", g)
	}
	// non-complete coverage must never fabricate rate (fail-closed) — either sample
	for _, cov := range []string{CoveragePartial, CoverageDegraded, CoverageIncomparable, CoverageUnavailable, ""} {
		b.SizeComplete = true
		b.Coverage = cov
		g = ComputeGrowth(a, b)
		if g.Status != CoverageIndeterminate || g.RateBps != nil {
			t.Fatalf("newer coverage %q must be indeterminate without rate: %+v", cov, g)
		}
		b.Coverage = CoverageComplete
		a.Coverage = cov
		g = ComputeGrowth(a, b)
		if g.Status != CoverageIndeterminate || g.RateBps != nil {
			t.Fatalf("older coverage %q must be indeterminate without rate: %+v", cov, g)
		}
		a.Coverage = CoverageComplete
	}
	// basis mismatch
	b.Basis = "other_basis"
	g = ComputeGrowth(a, b)
	if g.Status != CoverageUnavailable || g.RateBps != nil {
		t.Fatalf("basis mismatch: %+v", g)
	}
}

func TestAliasChange_NoFalseMerge(t *testing.T) {
	clk := &FixedClock{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	// First sample at path A identity vol-1
	s1 := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi, UsedPercent: 50, Level: LevelOK, Coverage: CoverageComplete, SizeComplete: true}
	s2 := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 40 * gi, TotalBytes: 100 * gi, UsedBytes: 60 * gi, UsedPercent: 60, Level: LevelOK, Coverage: CoverageComplete, SizeComplete: true}
	// Path B later returns different identity → mismatch, not merge
	sMismatch := Sample{VolumeID: "vol-other", Basis: BasisStatfs, AvailBytes: 10 * gi, TotalBytes: 100 * gi, UsedBytes: 90 * gi, UsedPercent: 90, Level: LevelWarn, Coverage: CoverageComplete, SizeComplete: true}
	fake := &FakeSampler{ByPath: map[string][]Sample{
		"/mnt/a": {s1, s2},
		"/mnt/b": {sMismatch},
	}}
	eng := &Engine{Clock: clk, Sampler: fake, Store: store}
	ctx := context.Background()
	if _, err := eng.Register(ctx, "/mnt/a", "data"); err != nil {
		t.Fatal(err)
	}
	// Register alias path for same volume id later by path that still returns vol-1
	// Force second sample via ObserveOnce
	if _, err := eng.ObserveOnce(ctx, ObserveOpts{}); err != nil {
		t.Fatal(err)
	}
	// Point register path to /mnt/b which returns different id
	doc, _ := store.Load()
	doc.Volumes["vol-1"].Volume.RegisterPath = "/mnt/b"
	_ = store.Save(doc)
	res, err := eng.ObserveOnce(ctx, ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	v := store.Doc.Volumes["vol-1"]
	if v.Coverage != CoverageDegraded || v.LossReason != LossIdentityShift {
		t.Fatalf("expected identity loss, got coverage=%s loss=%s", v.Coverage, v.LossReason)
	}
	if v.LastSuccess != nil && v.LastSuccess.VolumeID != "vol-1" {
		t.Fatal("must not overwrite success with foreign identity")
	}
	_ = res
}

func TestLossReconcile_Once(t *testing.T) {
	clk := &FixedClock{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	sOK := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true}
	fake := &FakeSampler{ByPath: map[string][]Sample{"/v": {sOK, sOK, sOK}}}
	eng := &Engine{Clock: clk, Sampler: fake, Store: store}
	ctx := context.Background()
	if _, err := eng.Register(ctx, "/v", ""); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordLoss("vol-1", LossOverflow); err != nil {
		t.Fatal(err)
	}
	v := store.Doc.Volumes["vol-1"]
	if !v.ReconcileNeeded || v.Coverage != CoverageDegraded {
		t.Fatal("loss should degrade")
	}
	// One successful cycle clears reconcile
	if _, err := eng.ObserveOnce(ctx, ObserveOpts{}); err != nil {
		t.Fatal(err)
	}
	v = store.Doc.Volumes["vol-1"]
	if v.ReconcileNeeded || v.Coverage != CoverageComplete {
		t.Fatalf("reconcile should clear: needed=%v cov=%s", v.ReconcileNeeded, v.Coverage)
	}
}

func TestCorruptState_FailsToBaseline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &FileStore{Path: path}
	eng := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Sampler: &FakeSampler{ByPath: map[string][]Sample{}}, Store: store}
	// ObserveOnce should not crash; baseline empty
	res, err := eng.ObserveOnce(context.Background(), ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Health.Volumes) != 0 {
		t.Fatalf("expected empty volumes after corrupt reset, got %+v", res.Health.Volumes)
	}
	found := false
	for _, u := range res.Health.Unavailable {
		if strings.Contains(u, "corrupt_or_incompatible_state") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected corrupt_state signal in health.unavailable, got %v", res.Health.Unavailable)
	}
	text := HealthText(res.Health)
	if !strings.Contains(text, "corrupt_or_incompatible_state") {
		t.Fatalf("expected corrupt-state recovery in zero-volume text output, got %q", text)
	}
}

func TestSemanticCorruptState_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	// Valid JSON, wrong key vs id — must not drive growth.
	raw := `{"$schema":"` + StateSchemaID + `","version":1,"volumes":{"vol-a":{"volume":{"id":"vol-b","register_path":"/x"},"level":"ok","coverage":"complete","next_seq":2}}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &FileStore{Path: path}
	if _, err := store.Load(); err == nil {
		t.Fatal("expected semantic validation error")
	}
	eng := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Sampler: &FakeSampler{ByPath: map[string][]Sample{}}, Store: store}
	res, err := eng.ObserveOnce(context.Background(), ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Health.Volumes) != 0 {
		t.Fatalf("expected empty after semantic corrupt reset: %+v", res.Health.Volumes)
	}
	found := false
	for _, u := range res.Health.Unavailable {
		if strings.Contains(u, "corrupt_or_incompatible_state") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected corrupt_state signal after semantic reset, got %v", res.Health.Unavailable)
	}
}

func TestPersistedScanInFlight_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	doc := emptyState()
	doc.Volumes["vol-1"] = &VolumeRuntime{
		Volume: RegisteredVolume{ID: "vol-1", RegisterPath: "/v"},
		Level:  LevelOK, Coverage: CoverageComplete, NextSeq: 2, ScanInFlight: true,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &FileStore{Path: path}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "scan_in_flight") {
		t.Fatalf("expected runtime-only in-flight rejection, got %v", err)
	}
	eng := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Store: store}
	res, err := eng.ObserveOnce(context.Background(), ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Health.Volumes) != 0 {
		t.Fatalf("hostile in-flight state must not report stale complete volume: %+v", res.Health.Volumes)
	}
}

func TestSampleError_SetsPartialCoverage(t *testing.T) {
	clk := &FixedClock{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	sOK := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true}
	// register + baseline success + then fail
	fake := &FakeSampler{ByPath: map[string][]Sample{"/v": {sOK, sOK}}}
	eng := &Engine{Clock: clk, Sampler: fake, Store: store}
	ctx := context.Background()
	if _, err := eng.Register(ctx, "/v", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ObserveOnce(ctx, ObserveOpts{}); err != nil {
		t.Fatal(err)
	}
	if store.Doc.Volumes["vol-1"].LastSuccess == nil {
		t.Fatal("precondition: baseline success")
	}
	// Next cycle fails
	secretPath := "/Users/alice/private-project"
	fake.Err = fmt.Errorf("statfs %s: permission denied", secretPath)
	res, err := eng.ObserveOnce(ctx, ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	v := store.Doc.Volumes["vol-1"]
	if v.Coverage != CoveragePartial {
		t.Fatalf("sample error must set volume coverage partial, got %s", v.Coverage)
	}
	if !v.ReconcileNeeded {
		t.Fatal("sample error must set ReconcileNeeded (coverage gap)")
	}
	if v.LastSuccess == nil {
		t.Fatal("last success retained for display")
	}
	found := false
	for _, u := range res.Health.Unavailable {
		if strings.Contains(u, "sample_error") {
			found = true
		}
		if strings.Contains(u, secretPath) || strings.Contains(u, "permission denied") {
			t.Fatalf("health.unavailable must not leak raw path/error: %v", res.Health.Unavailable)
		}
	}
	if !found {
		t.Fatalf("sample_error code expected in health.unavailable, got %v", res.Health.Unavailable)
	}
	text := HealthText(res.Health)
	if strings.Contains(text, secretPath) {
		t.Fatal("HealthText leaked secret path")
	}
}

func TestLoss_NoGrowthAcrossReconcile(t *testing.T) {
	clk := &FixedClock{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	s1 := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 100 * gi, TotalBytes: 200 * gi, UsedBytes: 100 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true}
	s2 := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 200 * gi, UsedBytes: 150 * gi, UsedPercent: 75, Coverage: CoverageComplete, SizeComplete: true}
	s3 := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 40 * gi, TotalBytes: 200 * gi, UsedBytes: 160 * gi, UsedPercent: 80, Coverage: CoverageComplete, SizeComplete: true}
	fake := &FakeSampler{ByPath: map[string][]Sample{"/v": {s1, s2, s3}}}
	eng := &Engine{Clock: clk, Sampler: fake, Store: store, MaxConcurrent: 2}
	ctx := context.Background()
	if _, err := eng.Register(ctx, "/v", ""); err != nil {
		t.Fatal(err)
	}
	// Establish first success
	clk.Advance(time.Hour)
	res, err := eng.ObserveOnce(ctx, ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Health.Volumes[0].Growth == nil || res.Health.Volumes[0].Growth.Status != "measured" {
		// Register does not set LastSuccess; first cycle only baselines — OK either way
		// Force a second measured pair after baseline:
		clk.Advance(time.Hour)
	}
	// Loss between samples
	if err := eng.RecordLoss("vol-1", LossOverflow); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	res, err = eng.ObserveOnce(ctx, ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	v := store.Doc.Volumes["vol-1"]
	if v.ReconcileNeeded || v.Coverage != CoverageComplete {
		t.Fatalf("reconcile should clear: needed=%v cov=%s", v.ReconcileNeeded, v.Coverage)
	}
	// Must not report measured growth fabricated across the loss gap.
	for _, row := range res.Health.Volumes {
		if row.Growth != nil && row.Growth.Status == "measured" {
			t.Fatalf("must not measure growth across loss: %+v", row.Growth)
		}
	}
}

func TestForeground_ThrottlesConcurrency(t *testing.T) {
	clk := &FixedClock{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	// Two volumes
	sA := Sample{VolumeID: "vol-a", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true}
	sB := Sample{VolumeID: "vol-b", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true}
	fake := &FakeSampler{ByPath: map[string][]Sample{"/a": {sA, sA}, "/b": {sB, sB}}}
	eng := &Engine{Clock: clk, Sampler: fake, Store: store, MaxConcurrent: 4}
	ctx := context.Background()
	if _, err := eng.Register(ctx, "/a", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Register(ctx, "/b", "b"); err != nil {
		t.Fatal(err)
	}
	res, err := eng.ObserveOnce(ctx, ObserveOpts{Foreground: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Health.ForegroundActive {
		t.Fatal("foreground_active expected")
	}
	if res.SampleConcurrency != 1 {
		t.Fatalf("foreground must clamp sample concurrency to 1, got %d", res.SampleConcurrency)
	}
	// Cycle-scoped: must not stick in persisted state.
	for _, v := range store.Doc.Volumes {
		if v.Foreground {
			t.Fatalf("cycle-scoped foreground must not persist on volume %s", v.Volume.ID)
		}
	}
	// Next cycle without flag restores effective concurrency (clamped to volume count).
	res2, err := eng.ObserveOnce(ctx, ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// 2 volumes, MaxConcurrent=4 → effective min(4, hardMax, 2) = 2
	if res2.SampleConcurrency != 2 {
		t.Fatalf("without foreground, concurrency want 2 (volume count), got %d", res2.SampleConcurrency)
	}
	if res2.Health.ForegroundActive {
		t.Fatal("foreground_active must be false on later cycle without flag")
	}
}

func TestIncompleteReconcile_StaysPartial(t *testing.T) {
	clk := &FixedClock{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	sOK := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true}
	sPartial := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 40 * gi, TotalBytes: 100 * gi, UsedBytes: 60 * gi, UsedPercent: 60, Coverage: CoveragePartial, SizeComplete: false}
	fake := &FakeSampler{ByPath: map[string][]Sample{"/v": {sOK, sPartial}}}
	eng := &Engine{Clock: clk, Sampler: fake, Store: store}
	ctx := context.Background()
	if _, err := eng.Register(ctx, "/v", ""); err != nil {
		t.Fatal(err)
	}
	if err := eng.RecordLoss("vol-1", LossDroppedWork); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ObserveOnce(ctx, ObserveOpts{}); err != nil {
		t.Fatal(err)
	}
	v := store.Doc.Volumes["vol-1"]
	if !v.ReconcileNeeded {
		t.Fatal("partial reconcile must keep ReconcileNeeded")
	}
	if v.Coverage != CoveragePartial {
		t.Fatalf("coverage want partial, got %s", v.Coverage)
	}
}

func TestProposals_NoMutationSurface(t *testing.T) {
	clk := &FixedClock{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	// critical + free drop → generic notes only (no catalog recipes without evidence)
	s1 := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 30 * gi, TotalBytes: 100 * gi, UsedBytes: 70 * gi, UsedPercent: 70, Coverage: CoverageComplete, SizeComplete: true}
	s2 := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 5 * gi, TotalBytes: 100 * gi, UsedBytes: 95 * gi, UsedPercent: 95, Coverage: CoverageComplete, SizeComplete: true}
	fake := &FakeSampler{ByPath: map[string][]Sample{"/v": {s1, s2, s2}}}
	eng := &Engine{Clock: clk, Sampler: fake, Store: store}
	ctx := context.Background()
	if _, err := eng.Register(ctx, "/v", ""); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	res, err := eng.ObserveOnce(ctx, ObserveOpts{MutationContract: "open"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mutations) != 0 {
		t.Fatalf("mutations must be empty: %v", res.Mutations)
	}
	if res.Health.MutationContract != "open" {
		t.Fatalf("default unasserted contract want open, got %s", res.Health.MutationContract)
	}
	if len(res.Health.Proposals) == 0 {
		t.Fatal("expected proposals under critical pressure")
	}
	for _, p := range res.Health.Proposals {
		if p.Kind == "delete" || p.Kind == "execute" {
			t.Fatalf("forbidden proposal kind: %+v", p)
		}
		if p.RecipeID != "" {
			t.Fatalf("pressure-only must not emit catalog recipe ids: %+v", p)
		}
		if p.Kind != "note" {
			t.Fatalf("pressure-only proposals must be notes: %+v", p)
		}
	}
}

func TestCoverageGap_NoGrowthAcrossPartialThenComplete(t *testing.T) {
	clk := &FixedClock{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	s1 := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 100 * gi, TotalBytes: 200 * gi, UsedBytes: 100 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true}
	sPartial := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 80 * gi, TotalBytes: 200 * gi, UsedBytes: 120 * gi, UsedPercent: 60, Coverage: CoveragePartial, SizeComplete: false}
	s3 := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 200 * gi, UsedBytes: 150 * gi, UsedPercent: 75, Coverage: CoverageComplete, SizeComplete: true}
	s4 := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 40 * gi, TotalBytes: 200 * gi, UsedBytes: 160 * gi, UsedPercent: 80, Coverage: CoverageComplete, SizeComplete: true}
	// register + baseline complete + partial gap + post-gap completes
	fake := &FakeSampler{ByPath: map[string][]Sample{"/v": {s1, s1, sPartial, s3, s4}}}
	eng := &Engine{Clock: clk, Sampler: fake, Store: store}
	ctx := context.Background()
	if _, err := eng.Register(ctx, "/v", ""); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	// baseline complete
	if _, err := eng.ObserveOnce(ctx, ObserveOpts{}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Hour)
	// partial gap
	if _, err := eng.ObserveOnce(ctx, ObserveOpts{}); err != nil {
		t.Fatal(err)
	}
	v := store.Doc.Volumes["vol-1"]
	if !v.ReconcileNeeded || v.Coverage != CoveragePartial {
		t.Fatalf("gap must reconcile partial: needed=%v cov=%s", v.ReconcileNeeded, v.Coverage)
	}
	clk.Advance(time.Hour)
	// first complete after gap: baseline only
	res, err := eng.ObserveOnce(ctx, ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range res.Health.Volumes {
		if row.Growth != nil && row.Growth.Status == "measured" {
			t.Fatalf("no growth on first complete after gap: %+v", row.Growth)
		}
	}
	clk.Advance(time.Hour)
	// second complete pair may measure
	res, err = eng.ObserveOnce(ctx, ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	measured := false
	for _, row := range res.Health.Volumes {
		if row.Growth != nil && row.Growth.Status == "measured" {
			measured = true
		}
	}
	if !measured {
		t.Fatal("expected measured growth after two post-gap complete samples")
	}
}

func TestCycleLease_Exclusive(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state.json")
	leaseA := NewFileCycleLease(dir)
	leaseB := NewFileCycleLease(dir)
	storeA := &FileStore{Path: storePath}
	storeB := &FileStore{Path: storePath}

	// Blocking sampler for engine A
	block := make(chan struct{})
	samplerA := &BlockingSampler{Release: block, SampleOut: Sample{
		VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi,
		UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true,
	}}
	// Seed registration
	fake := &FakeSampler{ByPath: map[string][]Sample{"/v": {
		{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true},
	}}}
	seed := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Sampler: fake, Store: storeA}
	if _, err := seed.Register(context.Background(), "/v", ""); err != nil {
		t.Fatal(err)
	}

	engA := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Sampler: samplerA, Store: storeA, Lease: leaseA}
	engB := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Sampler: &FakeSampler{ByPath: map[string][]Sample{}}, Store: storeB, Lease: leaseB}

	started := make(chan struct{})
	doneA := make(chan error, 1)
	go func() {
		close(started)
		_, err := engA.ObserveOnce(context.Background(), ObserveOpts{})
		doneA <- err
	}()
	<-started
	// Wait until A holds lease (sampler blocked)
	time.Sleep(50 * time.Millisecond)

	ctxB, cancelB := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelB()
	_, errB := engB.ObserveOnce(ctxB, ObserveOpts{})
	if errB == nil || !IsCycleBusy(errB) {
		close(block)
		t.Fatalf("second engine must get cycle busy/cancel, got %v", errB)
	}
	close(block)
	if err := <-doneA; err != nil {
		t.Fatalf("engine A: %v", err)
	}
	// After release, B succeeds
	fake2 := &FakeSampler{ByPath: map[string][]Sample{"/v": {
		{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 40 * gi, TotalBytes: 100 * gi, UsedBytes: 60 * gi, UsedPercent: 60, Coverage: CoverageComplete, SizeComplete: true},
	}}}
	engB.Sampler = fake2
	if _, err := engB.ObserveOnce(context.Background(), ObserveOpts{}); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

func TestRegister_RespectsCycleLease(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state.json")
	leaseA := NewFileCycleLease(dir)
	leaseB := NewFileCycleLease(dir)
	storeA := &FileStore{Path: storePath}
	storeB := &FileStore{Path: storePath}

	block := make(chan struct{})
	samplerA := &BlockingSampler{Release: block, SampleOut: Sample{
		VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi,
		UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true,
	}}
	fake := &FakeSampler{ByPath: map[string][]Sample{"/v": {
		{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true},
	}}}
	seed := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Sampler: fake, Store: storeA, Lease: NewFileCycleLease(dir)}
	if _, err := seed.Register(context.Background(), "/v", ""); err != nil {
		t.Fatal(err)
	}

	engA := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Sampler: samplerA, Store: storeA, Lease: leaseA}
	engB := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Sampler: &FakeSampler{ByPath: map[string][]Sample{
		"/other": {{VolumeID: "vol-2", Basis: BasisStatfs, AvailBytes: 10, TotalBytes: 20, Coverage: CoverageComplete, SizeComplete: true}},
	}}, Store: storeB, Lease: leaseB}

	doneA := make(chan error, 1)
	go func() {
		_, err := engA.ObserveOnce(context.Background(), ObserveOpts{})
		doneA <- err
	}()
	time.Sleep(50 * time.Millisecond)

	ctxB, cancelB := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelB()
	_, errB := engB.Register(ctxB, "/other", "other")
	if errB == nil || !IsCycleBusy(errB) {
		close(block)
		t.Fatalf("Register during leased cycle must be busy/cancel, got %v", errB)
	}
	close(block)
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}
	// After release, register succeeds and persists
	if _, err := engB.Register(context.Background(), "/other", "other"); err != nil {
		t.Fatal(err)
	}
	doc, err := storeB.Load()
	if err != nil {
		t.Fatal(err)
	}
	if doc.Volumes["vol-1"] == nil || doc.Volumes["vol-2"] == nil {
		t.Fatalf("both volumes should persist: keys=%v", keysOf(doc.Volumes))
	}
}

func keysOf(m map[string]*VolumeRuntime) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSameEngine_ConcurrentObserveOnceSerializes(t *testing.T) {
	// Peer P1: leaseHeld bool let a second goroutine skip Acquire. opMu must serialize.
	clk := &FixedClock{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	block := make(chan struct{})
	var entries atomic.Int32
	var maxConcurrent atomic.Int32
	var inSample atomic.Int32
	sampler := &countingBlockSampler{
		Release: block,
		OnEnter: func() {
			n := inSample.Add(1)
			for {
				cur := maxConcurrent.Load()
				if n <= cur || maxConcurrent.CompareAndSwap(cur, n) {
					break
				}
			}
			entries.Add(1)
		},
		OnLeave: func() { inSample.Add(-1) },
		SampleOut: Sample{
			VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi,
			UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true,
		},
	}
	// Seed registration with a plain fake sample first
	fake := &FakeSampler{ByPath: map[string][]Sample{"/v": {
		{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true},
	}}}
	eng := &Engine{Clock: clk, Sampler: fake, Store: store, Lease: &MemoryCycleLease{}, MaxConcurrent: 4}
	if _, err := eng.Register(context.Background(), "/v", ""); err != nil {
		t.Fatal(err)
	}
	eng.Sampler = sampler

	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() { _, err := eng.ObserveOnce(context.Background(), ObserveOpts{}); done1 <- err }()
	time.Sleep(40 * time.Millisecond)
	go func() { _, err := eng.ObserveOnce(context.Background(), ObserveOpts{}); done2 <- err }()
	time.Sleep(80 * time.Millisecond)
	if maxConcurrent.Load() > 1 {
		close(block)
		t.Fatalf("same Engine allowed concurrent sampler entries: max=%d", maxConcurrent.Load())
	}
	close(block)
	if err := <-done1; err != nil {
		t.Fatal(err)
	}
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
	if entries.Load() < 2 {
		t.Fatalf("expected both cycles to sample eventually, entries=%d", entries.Load())
	}
}

func TestSameEngine_RegisterVsBlockedObserveOnceSerializes(t *testing.T) {
	// Companion to ConcurrentObserveOnceSerializes: Register is also a public RMW.
	// opMu must serialize Register against an in-flight ObserveOnce so registration
	// is not lost and sampler entries never overlap on the same Engine.
	clk := &FixedClock{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	block := make(chan struct{})
	var inSample atomic.Int32
	var maxConcurrent atomic.Int32
	seedSample := Sample{
		VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi,
		UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true,
	}
	fake := &FakeSampler{ByPath: map[string][]Sample{"/v": {seedSample}}}
	eng := &Engine{Clock: clk, Sampler: fake, Store: store, Lease: &MemoryCycleLease{}, MaxConcurrent: 4}
	if _, err := eng.Register(context.Background(), "/v", "primary"); err != nil {
		t.Fatal(err)
	}
	sampler := &pathAwareBlockSampler{
		BlockPath: "/v",
		Release:   block,
		OnEnter: func() {
			n := inSample.Add(1)
			for {
				cur := maxConcurrent.Load()
				if n <= cur || maxConcurrent.CompareAndSwap(cur, n) {
					break
				}
			}
		},
		OnLeave: func() { inSample.Add(-1) },
		ByPath: map[string]Sample{
			"/v": seedSample,
			"/other": {
				VolumeID: "vol-2", Basis: BasisStatfs, AvailBytes: 10 * gi, TotalBytes: 20 * gi,
				UsedBytes: 10 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true,
			},
		},
	}
	eng.Sampler = sampler

	// ObserveOnce will sample /v and block; Register(/other) must wait on opMu.
	observeDone := make(chan error, 1)
	go func() {
		_, err := eng.ObserveOnce(context.Background(), ObserveOpts{})
		observeDone <- err
	}()
	// Wait until observe holds the sampler for /v
	deadline := time.Now().Add(2 * time.Second)
	for inSample.Load() == 0 {
		if time.Now().After(deadline) {
			close(block)
			t.Fatal("observe never entered sample")
		}
		time.Sleep(5 * time.Millisecond)
	}

	registerDone := make(chan error, 1)
	go func() {
		_, err := eng.Register(context.Background(), "/other", "other")
		registerDone <- err
	}()
	// Register must not run its sample while ObserveOnce still holds opMu/sampler.
	time.Sleep(80 * time.Millisecond)
	if maxConcurrent.Load() > 1 {
		close(block)
		t.Fatalf("Register overlapped ObserveOnce sampling: maxConcurrent=%d", maxConcurrent.Load())
	}
	// Register still pending (blocked on opMu)
	select {
	case err := <-registerDone:
		close(block)
		t.Fatalf("Register completed while ObserveOnce held opMu: %v", err)
	default:
	}

	close(block)
	if err := <-observeDone; err != nil {
		t.Fatalf("ObserveOnce: %v", err)
	}
	if err := <-registerDone; err != nil {
		t.Fatalf("Register: %v", err)
	}
	if maxConcurrent.Load() > 1 {
		t.Fatalf("sampler concurrency >1: %d", maxConcurrent.Load())
	}
	doc, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if doc.Volumes["vol-1"] == nil || doc.Volumes["vol-2"] == nil {
		t.Fatalf("registration lost after serialized RMW: keys=%v", keysOf(doc.Volumes))
	}
	if doc.Volumes["vol-2"].Volume.Label != "other" {
		t.Fatalf("vol-2 label: %+v", doc.Volumes["vol-2"].Volume)
	}
}

// countingBlockSampler blocks until Release is closed; tracks concurrent entries.
type countingBlockSampler struct {
	Release   chan struct{}
	OnEnter   func()
	OnLeave   func()
	SampleOut Sample
}

func (c *countingBlockSampler) Sample(ctx context.Context, path string) (Sample, error) {
	if c.OnEnter != nil {
		c.OnEnter()
	}
	if c.OnLeave != nil {
		defer c.OnLeave()
	}
	select {
	case <-c.Release:
		return c.SampleOut, nil
	case <-ctx.Done():
		return Sample{}, ctx.Err()
	}
}

// pathAwareBlockSampler blocks only for BlockPath; other paths return immediately.
type pathAwareBlockSampler struct {
	BlockPath string
	Release   chan struct{}
	OnEnter   func()
	OnLeave   func()
	ByPath    map[string]Sample
}

func (p *pathAwareBlockSampler) Sample(ctx context.Context, path string) (Sample, error) {
	out, ok := p.ByPath[path]
	if !ok {
		return Sample{}, fmt.Errorf("no sample for %s", path)
	}
	if path != p.BlockPath {
		return out, nil
	}
	if p.OnEnter != nil {
		p.OnEnter()
	}
	if p.OnLeave != nil {
		defer p.OnLeave()
	}
	select {
	case <-p.Release:
		return out, nil
	case <-ctx.Done():
		return Sample{}, ctx.Err()
	}
}

func TestRegistrationCap(t *testing.T) {
	store := &MemoryStore{Doc: emptyState()}
	// Pre-fill at cap
	for i := 0; i < MaxRegisteredVolumes; i++ {
		id := VolumeID(fmt.Sprintf("vol-%d", i))
		store.Doc.Volumes[string(id)] = &VolumeRuntime{
			Volume: RegisteredVolume{ID: id, RegisterPath: fmt.Sprintf("/p%d", i)},
			Level:  LevelOK, Coverage: CoverageIncomparable, NextSeq: 1,
		}
	}
	fake := &FakeSampler{ByPath: map[string][]Sample{
		"/new": {{VolumeID: "vol-new", Basis: BasisStatfs, AvailBytes: 1, TotalBytes: 2, Coverage: CoverageComplete, SizeComplete: true}},
	}}
	eng := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Sampler: fake, Store: store}
	if _, err := eng.Register(context.Background(), "/new", ""); err == nil {
		t.Fatal("expected registration refusal at cap")
	}
}

func TestLoad_RejectsOverCapVolumes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	// Build well-formed state with MaxRegisteredVolumes+1 entries.
	vols := map[string]any{}
	for i := 0; i < MaxRegisteredVolumes+1; i++ {
		id := fmt.Sprintf("vol-%d", i)
		vols[id] = map[string]any{
			"volume":   map[string]any{"id": id, "register_path": "/p" + id},
			"level":    "ok",
			"coverage": "incomparable",
			"next_seq": 1,
		}
	}
	raw, _ := json.Marshal(map[string]any{"$schema": StateSchemaID, "version": 1, "volumes": vols})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &FileStore{Path: path}
	if _, err := store.Load(); err == nil {
		t.Fatal("expected load reject over-cap volumes")
	}
	// Engine fails closed to empty baseline
	eng := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Sampler: &FakeSampler{ByPath: map[string][]Sample{}}, Store: store}
	res, err := eng.ObserveOnce(context.Background(), ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Health.Volumes) != 0 {
		t.Fatalf("expected empty after over-cap reset: %+v", res.Health.Volumes)
	}
}

func TestLoad_RejectsOverlongLabel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	long := strings.Repeat("x", MaxLabelLen+1)
	raw := fmt.Sprintf(`{"$schema":%q,"version":1,"volumes":{"vol-1":{"volume":{"id":"vol-1","register_path":"/x","label":%q},"level":"ok","coverage":"complete","next_seq":2}}}`, StateSchemaID, long)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&FileStore{Path: path}).Load(); err == nil {
		t.Fatal("expected overlong label reject")
	}
}

func TestRegister_UTF8LabelBoundaryPersistsValidState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := &FileStore{Path: path}
	sampl := Sample{VolumeID: "vol-1", Basis: BasisStatfs, Coverage: CoverageComplete, SizeComplete: true}
	eng := &Engine{
		Clock:   &FixedClock{T: time.Now().UTC()},
		Sampler: &FakeSampler{ByPath: map[string][]Sample{"/v": {sampl}}},
		Store:   store,
	}

	if _, err := eng.Register(context.Background(), "/v", strings.Repeat("x", MaxLabelLen-1)+"é"); err != nil {
		t.Fatal(err)
	}
	doc, err := store.Load()
	if err != nil {
		t.Fatalf("persisted UTF-8 boundary label must reload: %v", err)
	}
	label := doc.Volumes["vol-1"].Volume.Label
	if !utf8.ValidString(label) || len(label) > MaxLabelLen || label != strings.Repeat("x", MaxLabelLen-1) {
		t.Fatalf("unexpected bounded label: valid=%v bytes=%d label=%q", utf8.ValidString(label), len(label), label)
	}
}

func TestMaxConcurrent_HardClamped(t *testing.T) {
	clk := &FixedClock{T: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)}
	store := &MemoryStore{}
	// One volume only
	s := Sample{VolumeID: "vol-1", Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true}
	fake := &FakeSampler{ByPath: map[string][]Sample{"/v": {s, s}}}
	eng := &Engine{Clock: clk, Sampler: fake, Store: store, MaxConcurrent: 1000}
	ctx := context.Background()
	if _, err := eng.Register(ctx, "/v", ""); err != nil {
		t.Fatal(err)
	}
	res, err := eng.ObserveOnce(ctx, ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	// One volume → concurrency 1 (useful work), never 1000
	if res.SampleConcurrency != 1 {
		t.Fatalf("want SampleConcurrency=1 for 1 volume, got %d", res.SampleConcurrency)
	}
	// Many volumes + high MaxConcurrent → hard max
	store2 := &MemoryStore{Doc: emptyState()}
	byPath := map[string][]Sample{}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("vol-%d", i)
		p := fmt.Sprintf("/p%d", i)
		samp := Sample{VolumeID: VolumeID(id), Basis: BasisStatfs, AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi, UsedPercent: 50, Coverage: CoverageComplete, SizeComplete: true}
		byPath[p] = []Sample{samp, samp}
		store2.Doc.Volumes[id] = &VolumeRuntime{
			Volume: RegisteredVolume{ID: VolumeID(id), RegisterPath: p},
			Level:  LevelOK, Coverage: CoverageIncomparable, NextSeq: 1,
		}
	}
	eng2 := &Engine{Clock: clk, Sampler: &FakeSampler{ByPath: byPath}, Store: store2, MaxConcurrent: 1000}
	res2, err := eng2.ObserveOnce(ctx, ObserveOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.SampleConcurrency != MaxConcurrentHardMax {
		t.Fatalf("want hard max %d, got %d", MaxConcurrentHardMax, res2.SampleConcurrency)
	}
}

// BlockingSampler blocks until Release is closed, then returns SampleOut.
type BlockingSampler struct {
	Release   chan struct{}
	SampleOut Sample
}

func (b *BlockingSampler) Sample(ctx context.Context, path string) (Sample, error) {
	select {
	case <-b.Release:
		return b.SampleOut, nil
	case <-ctx.Done():
		return Sample{}, ctx.Err()
	}
}

func TestFileStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "obs.json")
	st := &FileStore{Path: path}
	doc := emptyState()
	doc.Volumes["v"] = &VolumeRuntime{Volume: RegisteredVolume{ID: "v", RegisterPath: "/x"}, Level: LevelOK, Coverage: CoverageComplete, NextSeq: 2}
	if err := st.Save(doc); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Volumes["v"].NextSeq != 2 {
		t.Fatalf("%+v", got.Volumes["v"])
	}
	// version mismatch
	raw, _ := json.Marshal(map[string]any{"$schema": StateSchemaID, "version": 99, "volumes": map[string]any{}})
	_ = os.WriteFile(path, raw, 0o600)
	if _, err := st.Load(); err == nil {
		t.Fatal("expected version error")
	}
}

func TestFileStore_RejectsOversizedAndUnboundedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "obs.json")
	st := &FileStore{Path: path}
	if err := os.WriteFile(path, make([]byte, MaxStateFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(); err == nil || !strings.Contains(err.Error(), "exceeds max size") {
		t.Fatalf("expected pre-decode size rejection, got %v", err)
	}

	doc := emptyState()
	doc.Volumes["v"] = &VolumeRuntime{
		Volume: RegisteredVolume{ID: "v", RegisterPath: "/x"},
		Level:  LevelOK, Coverage: CoverageComplete, NextSeq: 1,
		LossReason: strings.Repeat("x", MaxStateStringLen+1),
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(); err == nil || !strings.Contains(err.Error(), "loss_reason") {
		t.Fatalf("expected bounded-string rejection, got %v", err)
	}
}

type cancelAfterSample struct {
	cancel context.CancelFunc
	sample Sample
	once   sync.Once
}

func (s *cancelAfterSample) Sample(context.Context, string) (Sample, error) {
	s.once.Do(s.cancel)
	return s.sample, nil
}

func TestObserveOnce_CancelMarksUnsampledVolumesPartial(t *testing.T) {
	store := &MemoryStore{Doc: emptyState()}
	for _, id := range []string{"a", "b"} {
		store.Doc.Volumes[id] = &VolumeRuntime{
			Volume: RegisteredVolume{ID: VolumeID(id), RegisterPath: "/" + id},
			Level:  LevelOK, Coverage: CoverageComplete, NextSeq: 2,
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	sampler := &cancelAfterSample{cancel: cancel, sample: Sample{
		VolumeID: "a", Basis: BasisStatfs, Coverage: CoverageComplete,
		AvailBytes: 50 * gi, TotalBytes: 100 * gi, UsedBytes: 50 * gi,
		UsedPercent: 50, SizeComplete: true,
	}}
	eng := &Engine{Clock: &FixedClock{T: time.Now().UTC()}, Sampler: sampler, Store: store, MaxConcurrent: 1}
	if _, err := eng.ObserveOnce(ctx, ObserveOpts{}); err != nil {
		t.Fatal(err)
	}
	v := store.Doc.Volumes["b"]
	if v.Coverage != CoveragePartial || !v.ReconcileNeeded || v.LossReason != LossDroppedWork {
		t.Fatalf("unsampled volume retained stale coverage: %+v", v)
	}
}

func TestObserveOnce_ContextCancel(t *testing.T) {
	clk := &FixedClock{T: time.Now().UTC()}
	// Sampler that blocks until cancel
	fake := &FakeSampler{ByPath: map[string][]Sample{}, Err: context.Canceled}
	eng := &Engine{Clock: clk, Sampler: fake, Store: &MemoryStore{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Register needs sample - will fail
	if _, err := eng.Register(ctx, "/x", ""); err == nil {
		t.Fatal("expected cancel on register")
	}
	_ = errors.New("ok")
}
