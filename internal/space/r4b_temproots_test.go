package space

import (
	"context"
	"github.com/3leaps/spanwit/internal/config"
	"path/filepath"
	"testing"
	"time"
)

// R4b S3 fixtures for temp walk roots (S4, split after grype and .gocache).
// Today temp roots are observation-only temp planes; S4 adds the per-user temp
// root as a verified walk root, so a Cargo target in a temp checkout becomes a
// prunable candidate. Shared /tmp stays outside the verified walk.

func r4bTempAnalyze(t *testing.T, analysisRoot string, planes []tempPlaneCandidate) Report {
	t.Helper()
	report, err := Analyze(context.Background(), Options{
		Path:                analysisRoot,
		MinSize:             "1K",
		Top:                 10,
		IncludeHomeCaches:   false,
		Now:                 time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
		tempPlaneCandidates: planes,
	})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return report
}

func verifiedPaths(r Report) []string {
	out := make([]string, 0, len(r.Verified.Candidates))
	for _, c := range r.Verified.Candidates {
		out = append(out, c.Path)
	}
	return out
}

// The per-user temp root is walked for verified candidates: a Cargo target in
// a temp checkout is reclaimable like any other.
func TestR4bTemp_UserTempCargoTargetIsVerified(t *testing.T) {
	t.Skip("pending R4b S4: per-user $TMPDIR as a verified walk root (shared /tmp excluded)")
	analysis := t.TempDir()
	userTemp := t.TempDir()
	writeFile(t, filepath.Join(userTemp, "checkout", "Cargo.toml"), 64)
	target := filepath.Join(userTemp, "checkout", "target")
	writeFile(t, filepath.Join(target, "debug", "x.o"), 4096)

	r := r4bTempAnalyze(t, analysis, []tempPlaneCandidate{{Path: userTemp, Kind: TempPlaneKindUser}})
	found := false
	for _, p := range verifiedPaths(r) {
		if p == target {
			found = true
		}
	}
	if !found {
		t.Fatalf("cargo target in the per-user temp root should be verified: %v", verifiedPaths(r))
	}
	// Temp checkouts are not exempt from the age window: with a min_age the
	// fresh target is withheld, never prunable.
	report, err := Analyze(context.Background(), Options{
		Path: analysis, MinSize: "1K", Top: 10, IncludeHomeCaches: false,
		Now:                 time.Now(),
		Config:              minAgeConfig(t, "7d"),
		tempPlaneCandidates: []tempPlaneCandidate{{Path: userTemp, Kind: TempPlaneKindUser}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range report.Verified.Candidates {
		if c.Path == target && c.State == StatePrunable {
			t.Fatalf("a fresh temp target must not be prunable under min_age: %+v", c)
		}
	}
}

// Shared temp is never a verified walk root: it may hold other users' trees.
// True today (temp roots are not walked); S4 must keep shared temp excluded.
func TestR4bTemp_SharedTempIsNotVerified(t *testing.T) {
	analysis := t.TempDir()
	shared := t.TempDir()
	writeFile(t, filepath.Join(shared, "checkout", "Cargo.toml"), 64)
	target := filepath.Join(shared, "checkout", "target")
	writeFile(t, filepath.Join(target, "debug", "x.o"), 4096)

	r := r4bTempAnalyze(t, analysis, []tempPlaneCandidate{{Path: shared, Kind: TempPlaneKindShared}})
	for _, p := range verifiedPaths(r) {
		if p == target {
			t.Fatalf("shared temp must not be a verified walk root: %v", verifiedPaths(r))
		}
	}
}

func minAgeConfig(t *testing.T, minAge string) *config.Config {
	t.Helper()
	return &config.Config{Version: 1, Defaults: config.Defaults{MinAge: minAge}}
}
