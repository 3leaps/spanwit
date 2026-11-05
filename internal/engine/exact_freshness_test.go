package engine

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cargoFixture builds root/app/{Cargo.toml,target/debug/a} and returns the
// target path. The tree is fresh (mtime now) until aged.
func cargoFixture(t *testing.T) (root, target string) {
	t.Helper()
	root = t.TempDir()
	writeFile(t, filepath.Join(root, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(root, "app", "target", "debug", "a"), 4096)
	return root, filepath.Join(root, "app", "target")
}

// setTreeTime sets every entry under dir (and dir) to at.
func setTreeTime(t *testing.T, dir string, at time.Time) {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Deepest first so a parent's mtime is not moved by later child updates.
	for i := len(paths) - 1; i >= 0; i-- {
		if err := os.Chtimes(paths[i], at, at); err != nil {
			t.Fatal(err)
		}
	}
}

// baselineOf records what a scan at this moment would carry.
func baselineOf(size int64, last time.Time, layout string) *ExactBaseline {
	return &ExactBaseline{
		SizeBytes:      size,
		LastActivityAt: last.UTC().Format(layout),
		ActivityBasis:  ActivityBasisDescendantMtime,
	}
}

func cargoEntry(target, minAge string, b *ExactBaseline) ExactEntry {
	return ExactEntry{
		Path: target, Signature: "development.rust.cargo-target",
		EffectiveMinSize: FilterDisplayNone, EffectiveMinAge: minAge, EffectiveMaxAge: FilterDisplayNone,
		Baseline: b,
	}
}

func planOne(t *testing.T, in ExactPlanInput) (PrunePlan, PruneCandidate) {
	t.Helper()
	plan, err := PlanFromExact(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("want 1 candidate, got %d: %+v warnings=%v", len(plan.Candidates), plan.Candidates, plan.Warnings)
	}
	return plan, plan.Candidates[0]
}

func treeSize(t *testing.T, dir string) int64 {
	t.Helper()
	r, err := DirSizeContext(context.Background(), dir, -1)
	if err != nil {
		t.Fatal(err)
	}
	return r.Bytes
}

func wantState(t *testing.T, c PruneCandidate, state, reason string) {
	t.Helper()
	if c.State != state || c.WithheldReason != reason {
		t.Fatalf("want %s/%q, got %s/%q (%+v)", state, reason, c.State, c.WithheldReason, c)
	}
}

func wantDrift(t *testing.T, c PruneCandidate, size, activity string) {
	t.Helper()
	if c.Drift == nil {
		t.Fatalf("drift not set: %+v", c)
	}
	if c.Drift.Size != size || c.Drift.Activity != activity {
		t.Fatalf("want drift size=%s activity=%s, got size=%s activity=%s", size, activity, c.Drift.Size, c.Drift.Activity)
	}
}

func TestFreshness_DefaultFloorWithholdsFreshNoneCarrier(t *testing.T) {
	root, target := cargoFixture(t)
	now := time.Now()
	plan, c := planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{
		cargoEntry(target, FilterDisplayNone, baselineOf(treeSize(t, target), now, time.RFC3339Nano)),
	}})
	wantState(t, c, CandidateStateWithheld, WithheldReasonAge)
	if c.EffectiveMinAge != HandoffAgeFloor {
		t.Fatalf("effective_min_age=%q, want handoff floor %q", c.EffectiveMinAge, HandoffAgeFloor)
	}
	if plan.Freshness == nil || !plan.Freshness.HandoffFloorApplied || plan.Freshness.HandoffFloor != "1h" {
		t.Fatalf("freshness=%+v", plan.Freshness)
	}
}

func TestFreshness_AgedUnchangedIsPrunable(t *testing.T) {
	root, target := cargoFixture(t)
	at := time.Now().Add(-3 * time.Hour)
	setTreeTime(t, target, at)
	_, c := planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{
		cargoEntry(target, FilterDisplayNone, baselineOf(treeSize(t, target), at, time.RFC3339Nano)),
	}})
	wantState(t, c, CandidateStatePrunable, "")
	wantDrift(t, c, DriftUnchanged, DriftUnchanged)
	if c.Drift.SizeDeltaBytes == nil || *c.Drift.SizeDeltaBytes != 0 {
		t.Fatalf("delta=%v", c.Drift.SizeDeltaBytes)
	}
}

// Acceptance: touched after plan generation -> withheld under the default
// floor, and the drift facts survive the age withholding.
func TestFreshness_TouchedAfterPlanWithheldAndMarked(t *testing.T) {
	root, target := cargoFixture(t)
	at := time.Now().Add(-3 * time.Hour)
	setTreeTime(t, target, at)
	b := baselineOf(treeSize(t, target), at, time.RFC3339Nano)
	writeFile(t, filepath.Join(target, "debug", "new-object"), 512)
	plan, c := planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{cargoEntry(target, FilterDisplayNone, b)}})
	wantState(t, c, CandidateStateWithheld, WithheldReasonAge)
	wantDrift(t, c, DriftChanged, DriftChanged)
	if *c.Drift.SizeDeltaBytes != 512 {
		t.Fatalf("delta=%d", *c.Drift.SizeDeltaBytes)
	}
	if plan.Freshness.ActivityAfterPlan != 1 || plan.Freshness.SizeDrift != 1 {
		t.Fatalf("freshness=%+v", plan.Freshness)
	}
}

// Activity after the plan withholds even when the write is older than every floor.
func TestFreshness_ActivityAfterPlanWithholdsPastFloor(t *testing.T) {
	root, target := cargoFixture(t)
	at := time.Now().Add(-5 * time.Hour)
	setTreeTime(t, target, at)
	b := baselineOf(treeSize(t, target), at, time.RFC3339Nano)
	later := time.Now().Add(-2 * time.Hour)
	f := filepath.Join(target, "debug", "a")
	if err := os.Chtimes(f, later, later); err != nil {
		t.Fatal(err)
	}
	_, c := planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{cargoEntry(target, FilterDisplayNone, b)}})
	wantState(t, c, CandidateStateWithheld, WithheldReasonActivityAfterPlan)
	wantDrift(t, c, DriftUnchanged, DriftChanged)
}

// A timestamp-preserving rewrite: mtime alone cannot see it, size drift can.
func TestFreshness_SizeDriftWithPreservedMtimeWithholds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int
		delta int64
	}{
		{"growth", 8192, 4096},
		{"shrink", 1024, -3072},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, target := cargoFixture(t)
			at := time.Now().Add(-5 * time.Hour)
			setTreeTime(t, target, at)
			b := baselineOf(treeSize(t, target), at, time.RFC3339Nano)
			f := filepath.Join(target, "debug", "a")
			if err := os.WriteFile(f, make([]byte, tc.size), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(f, at, at); err != nil {
				t.Fatal(err)
			}
			plan, c := planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{cargoEntry(target, FilterDisplayNone, b)}})
			wantState(t, c, CandidateStateWithheld, WithheldReasonSizeDrift)
			wantDrift(t, c, DriftChanged, DriftUnchanged)
			if *c.Drift.SizeDeltaBytes != tc.delta {
				t.Fatalf("delta=%d want %d", *c.Drift.SizeDeltaBytes, tc.delta)
			}
			if plan.Freshness.SizeDrift != 1 || plan.Freshness.ActivityAfterPlan != 0 {
				t.Fatalf("freshness=%+v", plan.Freshness)
			}
		})
	}
}

// Fractional mtime: a full-precision baseline round-trips as unchanged, and a
// whole-second legacy baseline of the same tree must not read as drift.
func TestFreshness_TimestampPrecision(t *testing.T) {
	at := time.Now().Add(-3 * time.Hour).Truncate(time.Second).Add(123456789 * time.Nanosecond)
	t.Run("nano round trip unchanged", func(t *testing.T) {
		root, target := cargoFixture(t)
		setTreeTime(t, target, at)
		_, c := planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{
			cargoEntry(target, FilterDisplayNone, baselineOf(treeSize(t, target), at, time.RFC3339Nano)),
		}})
		wantState(t, c, CandidateStatePrunable, "")
		wantDrift(t, c, DriftUnchanged, DriftUnchanged)
	})
	t.Run("legacy second baseline unchanged tree", func(t *testing.T) {
		root, target := cargoFixture(t)
		setTreeTime(t, target, at)
		b := baselineOf(treeSize(t, target), at, time.RFC3339)
		if strings.Contains(b.LastActivityAt, ".") {
			t.Fatalf("legacy fixture must be whole seconds: %s", b.LastActivityAt)
		}
		_, c := planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{cargoEntry(target, FilterDisplayNone, b)}})
		wantState(t, c, CandidateStatePrunable, "")
		wantDrift(t, c, DriftUnchanged, DriftUnchanged)
	})
	t.Run("legacy second baseline later whole-second write", func(t *testing.T) {
		root, target := cargoFixture(t)
		setTreeTime(t, target, at)
		b := baselineOf(treeSize(t, target), at, time.RFC3339)
		later := at.Add(time.Second)
		if err := os.Chtimes(filepath.Join(target, "debug", "a"), later, later); err != nil {
			t.Fatal(err)
		}
		_, c := planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{cargoEntry(target, FilterDisplayNone, b)}})
		wantState(t, c, CandidateStateWithheld, WithheldReasonActivityAfterPlan)
		wantDrift(t, c, DriftUnchanged, DriftChanged)
	})
}

func TestFreshness_IncompleteOrMissingBaselineIsUnknown(t *testing.T) {
	for name, b := range map[string]*ExactBaseline{
		"nil":            nil,
		"incomplete":     {SizeBytes: 1, SizeIncomplete: true, LastActivityAt: "2020-01-01T00:00:00Z", ActivityBasis: ActivityBasisDescendantMtime, ActivityIncomplete: true},
		"basis unknown":  {SizeBytes: 1, SizeIncomplete: true, LastActivityAt: "2020-01-01T00:00:00Z", ActivityBasis: ActivityBasisUnknown},
		"no timestamp":   {SizeBytes: 1, SizeIncomplete: true, ActivityBasis: ActivityBasisDescendantMtime},
		"unparseable ts": {SizeBytes: 1, SizeIncomplete: true, LastActivityAt: "yesterday", ActivityBasis: ActivityBasisDescendantMtime},
	} {
		t.Run(name, func(t *testing.T) {
			root, target := cargoFixture(t)
			setTreeTime(t, target, time.Now().Add(-3*time.Hour))
			plan, c := planOne(t, ExactPlanInput{AnalysisRoot: root, Entries: []ExactEntry{cargoEntry(target, FilterDisplayNone, b)}})
			wantState(t, c, CandidateStatePrunable, "")
			wantDrift(t, c, DriftUnknown, DriftUnknown)
			if c.Drift.SizeDeltaBytes != nil {
				t.Fatalf("unknown size must not carry a delta: %d", *c.Drift.SizeDeltaBytes)
			}
			if plan.Freshness.DriftUnknown != 1 {
				t.Fatalf("freshness=%+v", plan.Freshness)
			}
		})
	}
}

func TestFreshness_RequestedNoneDropsOnlyHandoffFloor(t *testing.T) {
	t.Run("fresh tree no other floor is prunable", func(t *testing.T) {
		root, target := cargoFixture(t)
		now := time.Now()
		setTreeTime(t, target, now)
		plan, c := planOne(t, ExactPlanInput{RequestedMinAge: RequestedMinAgeNone, AnalysisRoot: root, Entries: []ExactEntry{
			cargoEntry(target, FilterDisplayNone, baselineOf(treeSize(t, target), now, time.RFC3339Nano)),
		}})
		wantState(t, c, CandidateStatePrunable, "")
		if plan.Freshness.HandoffFloorApplied {
			t.Fatal("handoff floor must be reported as dropped")
		}
	})
	t.Run("carried floor kept", func(t *testing.T) {
		root, target := cargoFixture(t)
		at := time.Now().Add(-5 * time.Hour)
		setTreeTime(t, target, at)
		_, c := planOne(t, ExactPlanInput{RequestedMinAge: RequestedMinAgeNone, AnalysisRoot: root, Entries: []ExactEntry{
			cargoEntry(target, "48h", baselineOf(treeSize(t, target), at, time.RFC3339Nano)),
		}})
		wantState(t, c, CandidateStateWithheld, WithheldReasonAge)
	})
	t.Run("temp floor kept", func(t *testing.T) {
		root, target := cargoFixture(t)
		now := time.Now()
		setTreeTime(t, target, now)
		_, c := planOne(t, ExactPlanInput{RequestedMinAge: RequestedMinAgeNone, TempRoot: true, AnalysisRoot: root, Entries: []ExactEntry{
			cargoEntry(target, FilterDisplayNone, baselineOf(treeSize(t, target), now, time.RFC3339Nano)),
		}})
		wantState(t, c, CandidateStateWithheld, WithheldReasonAge)
	})
	t.Run("drift still withholds", func(t *testing.T) {
		root, target := cargoFixture(t)
		at := time.Now().Add(-5 * time.Hour)
		setTreeTime(t, target, at)
		b := baselineOf(treeSize(t, target), at, time.RFC3339Nano)
		writeFile(t, filepath.Join(target, "debug", "new-object"), 64)
		_, c := planOne(t, ExactPlanInput{RequestedMinAge: RequestedMinAgeNone, AnalysisRoot: root, Entries: []ExactEntry{cargoEntry(target, FilterDisplayNone, b)}})
		wantState(t, c, CandidateStateWithheld, WithheldReasonActivityAfterPlan)
	})
}

func TestFreshness_RequestedMinAgeRaiseOnly(t *testing.T) {
	t.Run("raises none carrier", func(t *testing.T) {
		root, target := cargoFixture(t)
		at := time.Now().Add(-5 * time.Hour)
		setTreeTime(t, target, at)
		_, c := planOne(t, ExactPlanInput{RequestedMinAge: "48h", AnalysisRoot: root, Entries: []ExactEntry{
			cargoEntry(target, FilterDisplayNone, baselineOf(treeSize(t, target), at, time.RFC3339Nano)),
		}})
		wantState(t, c, CandidateStateWithheld, WithheldReasonAge)
		if c.EffectiveMinAge != "48h" {
			t.Fatalf("effective_min_age=%q", c.EffectiveMinAge)
		}
	})
	t.Run("never lowers carried", func(t *testing.T) {
		root, target := cargoFixture(t)
		at := time.Now().Add(-5 * time.Hour)
		setTreeTime(t, target, at)
		plan, c := planOne(t, ExactPlanInput{RequestedMinAge: "2h", AnalysisRoot: root, Entries: []ExactEntry{
			cargoEntry(target, "48h", baselineOf(treeSize(t, target), at, time.RFC3339Nano)),
		}})
		wantState(t, c, CandidateStateWithheld, WithheldReasonAge)
		if c.EffectiveMinAge != "48h" {
			t.Fatalf("effective_min_age=%q", c.EffectiveMinAge)
		}
		if plan.Freshness.RequestedBelowFloor != 1 {
			t.Fatalf("freshness=%+v", plan.Freshness)
		}
		var warned bool
		for _, w := range plan.Warnings {
			warned = warned || strings.Contains(w.Error(), "raise-only")
		}
		if !warned {
			t.Fatalf("want raise-only warning, got %v", plan.Warnings)
		}
	})
}

func TestValidateRequestedMinAge(t *testing.T) {
	for _, ok := range []string{"none", "1s", "90m", "48h", "7d", "2w"} {
		if err := ValidateRequestedMinAge(ok); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"", "0", "0h", "00h", "-1h", "+1h", "1.5h", "abc", "48", "h", "1H", "None", " 1h", "1h ", "1h2m", "99999999999999w"} {
		if err := ValidateRequestedMinAge(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func TestPlanFromExact_InvalidRequestedMinAgeFailsClosed(t *testing.T) {
	root, target := cargoFixture(t)
	_, err := PlanFromExact(context.Background(), ExactPlanInput{RequestedMinAge: "0h", AnalysisRoot: root, Entries: []ExactEntry{
		cargoEntry(target, FilterDisplayNone, nil),
	}})
	if err == nil {
		t.Fatal("want error for invalid requested min age")
	}
}
