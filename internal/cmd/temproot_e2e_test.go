package cmd

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/space"
)

// End-to-end S4b gates through space -> report -> prune --from-space-report:
// one exact plan per root (G1), re-admission at prune time (A1/R3), and the
// writable-directory rule at revalidation (R4).

func e2eSkip(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("temp roots are darwin and linux only")
	}
	if os.Geteuid() == 0 {
		t.Skip("euid 0 never admits a temp root")
	}
}

func e2eAge(t *testing.T, path string) {
	t.Helper()
	when := time.Now().Add(-48 * time.Hour)
	if err := filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, when, when)
	}); err != nil {
		t.Fatal(err)
	}
}

func e2eCargo(t *testing.T, dir string) string {
	t.Helper()
	for _, f := range []string{"Cargo.toml", "target/debug/x.o"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 4096), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "target")
}

// e2eTempRoot makes an exactly-0700 temp root under a private parent and
// returns its canonical path.
func e2eTempRoot(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "p")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "T")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" && strings.HasPrefix(root, "/var/") {
		root = "/private" + root
	}
	return root
}

// e2eReport runs space --include-temp over analysis with temp as the user
// temp root, writes the JSON report and returns its path plus the report.
func e2eReport(t *testing.T, analysis, temp string) (string, space.Report) {
	t.Helper()
	// The production path: os.TempDir() reads TMPDIR, so space and prune both
	// see this root as the user's temp directory.
	t.Setenv("TMPDIR", temp)
	r, err := space.Analyze(context.Background(), space.Options{
		Path: analysis, MinSize: "1K", Top: 20, IncludeTemp: true,
	})
	if err != nil {
		t.Fatalf("space: %v", err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, r
}

func candidatePaths(p PrunePlan) map[string]string {
	out := map[string]string{}
	for _, c := range p.Candidates {
		out[c.Path] = c.State
	}
	return out
}

// G1: a report spanning the analysis root and a temp root carries one exact
// plan per root, and prune revalidates and admits candidates from both.
func TestS4bE2E_OnePlanPerRootRevalidatesBoth(t *testing.T) {
	e2eSkip(t)
	analysis := t.TempDir()
	temp := e2eTempRoot(t)
	inAnalysis := e2eCargo(t, filepath.Join(analysis, "app"))
	inTemp := e2eCargo(t, filepath.Join(temp, "checkout"))
	e2eAge(t, analysis)
	e2eAge(t, temp)

	path, r := e2eReport(t, analysis, temp)
	h := r.PruneHandoff
	if h == nil || h.ExactPlan == nil || len(h.TempExactPlans) != 1 {
		t.Fatalf("want a primary plan and one temp plan, got %+v", h)
	}
	if h.TempExactPlans[0].AnalysisRoot != temp {
		t.Fatalf("temp plan root = %q, want %q", h.TempExactPlans[0].AnalysisRoot, temp)
	}
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	got := candidatePaths(plan)
	for _, want := range []string{inAnalysis, inTemp} {
		if got[want] != "prunable" {
			t.Fatalf("%s should revalidate prunable under its own root, got %v", want, got)
		}
	}
}

// A1/R3: a temp root admitted when space ran but changed before prune (mode
// 0777) is re-checked at prune time, and its plan is dropped.
func TestS4bE2E_ReadmissionAtPruneTime(t *testing.T) {
	e2eSkip(t)
	analysis := t.TempDir()
	temp := e2eTempRoot(t)
	inTemp := e2eCargo(t, filepath.Join(temp, "checkout"))
	e2eAge(t, temp)
	path, _ := e2eReport(t, analysis, temp)

	if err := os.Chmod(temp, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(temp, 0o700) })
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, ok := candidatePaths(plan)[inTemp]; ok {
		t.Fatalf("a temp root that is no longer 0700 must not revalidate: %v", candidatePaths(plan))
	}
	refused := false
	for _, w := range plan.Warnings {
		if strings.Contains(w.Error(), "no longer admitted") {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("expected a re-admission refusal warning, got %v", plan.Warnings)
	}
}

// R3: re-admission refuses a carried boundary that is not an admissible temp
// root. The plan's analysis_root is moved to an ancestor of the real root (a
// directory the user owns but that is not exactly 0700 / shares a shared
// ancestor), which the report still loads; only prune-time admission refuses.
func TestS4bE2E_CarriedSharedBoundaryRefused(t *testing.T) {
	e2eSkip(t)
	analysis := t.TempDir()
	temp := e2eTempRoot(t)
	inTemp := e2eCargo(t, filepath.Join(temp, "checkout"))
	e2eAge(t, temp)
	path, _ := e2eReport(t, analysis, temp)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// The temp root's parent is a real directory containing the candidate, so
	// containment still holds and the report still loads, but the parent is not
	// itself an admissible temp root.
	parent := filepath.Dir(temp)
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	h := doc["prune_handoff"].(map[string]any)
	h["temp_exact_plans"].([]any)[0].(map[string]any)["analysis_root"] = parent
	tampered, _ := json.Marshal(doc)
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path)
	if err != nil {
		t.Fatalf("the tampered report should still load so re-admission decides: %v", err)
	}
	if _, ok := candidatePaths(plan)[inTemp]; ok {
		t.Fatalf("a carried boundary that is not an admissible temp root must not revalidate: %v", candidatePaths(plan))
	}
	refused := false
	for _, w := range plan.Warnings {
		if strings.Contains(w.Error(), "no longer admitted") {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("expected the re-admission refusal, got %v", plan.Warnings)
	}
}

// R4 at revalidation: a candidate that sits under a directory made group- or
// other-writable after space ran is refused by prune.
func TestS4bE2E_WritableDirAtRevalidationRefused(t *testing.T) {
	e2eSkip(t)
	analysis := t.TempDir()
	temp := e2eTempRoot(t)
	mid := filepath.Join(temp, "work")
	inTemp := e2eCargo(t, filepath.Join(mid, "checkout"))
	e2eAge(t, temp)
	path, _ := e2eReport(t, analysis, temp)

	if err := os.Chmod(mid, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(mid, 0o700) })
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, ok := candidatePaths(plan)[inTemp]; ok {
		t.Fatalf("a candidate under a now-writable directory must be refused: %v", candidatePaths(plan))
	}
}

// R4 at revalidation, the candidate itself: a target directory made group- or
// other-writable after space ran is refused, not only its parents.
func TestS4bE2E_WritableCandidateAtRevalidationRefused(t *testing.T) {
	e2eSkip(t)
	analysis := t.TempDir()
	temp := e2eTempRoot(t)
	inTemp := e2eCargo(t, filepath.Join(temp, "checkout"))
	e2eAge(t, temp)
	path, _ := e2eReport(t, analysis, temp)

	if err := os.Chmod(inTemp, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(inTemp, 0o700) })
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if st, ok := candidatePaths(plan)[inTemp]; ok {
		t.Fatalf("a candidate made writable after space must be refused, got state %q", st)
	}
}

// B1: a report rewritten to name another private, user-owned 0700 directory
// as a temp plan root is dropped at prune: the root must be the user's
// os.TempDir() recomputed now, not whatever the report claims.
func TestS4bE2E_RewrittenToOtherPrivateDirRefused(t *testing.T) {
	e2eSkip(t)
	analysis := t.TempDir()
	temp := e2eTempRoot(t)
	e2eCargo(t, filepath.Join(temp, "checkout"))
	e2eAge(t, temp)
	path, _ := e2eReport(t, analysis, temp)

	// A different 0700 directory the user owns (an ~/.ssh-shaped target),
	// holding a cargo tree the report is rewritten to point at.
	other := e2eTempRoot(t)
	victim := e2eCargo(t, filepath.Join(other, "keys"))
	e2eAge(t, other)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := strings.ReplaceAll(string(raw), temp, other)
	if err := os.WriteFile(path, []byte(rewritten), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path)
	if err != nil {
		t.Fatalf("the rewritten report should still load so re-admission decides: %v", err)
	}
	if _, ok := candidatePaths(plan)[victim]; ok {
		t.Fatalf("a report pointed at another private dir must not revalidate there: %v", candidatePaths(plan))
	}
	named := false
	for _, w := range plan.Warnings {
		if strings.Contains(w.Error(), "not_user_temp") && strings.Contains(w.Error(), other) {
			named = true
		}
	}
	if !named {
		t.Fatalf("expected a not_user_temp warning naming the plan root, got %v", plan.Warnings)
	}
}

// T1: the 1h temp floor holds at revalidation. A temp candidate younger than
// 1h, carried with effective_min_age "none", is present exactly once and
// withheld for age at prune.
func TestS4bE2E_TempFloorAtRevalidation(t *testing.T) {
	e2eSkip(t)
	analysis := t.TempDir()
	temp := e2eTempRoot(t)
	target := e2eCargo(t, filepath.Join(temp, "checkout"))
	e2eAge(t, temp)
	path, _ := e2eReport(t, analysis, temp)

	// The carried temp plan claims no age floor; revalidation must not trust it.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	tp := doc["prune_handoff"].(map[string]any)["temp_exact_plans"].([]any)[0].(map[string]any)
	carried := 0
	for _, c := range tp["candidates"].([]any) {
		cm := c.(map[string]any)
		if cm["path"] == target {
			cm["effective_min_age"] = "none"
			carried++
		}
	}
	if carried != 1 {
		t.Fatalf("the temp plan should carry the target once (got %d)", carried)
	}
	tampered, _ := json.Marshal(doc)
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}

	// After space: the target becomes fresh (an in-flight build).
	now := time.Now()
	if err := os.Chtimes(filepath.Join(target, "debug", "x.o"), now, now); err != nil {
		t.Fatal(err)
	}
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	n := 0
	for _, c := range plan.Candidates {
		if c.Path != target {
			continue
		}
		n++
		if c.State != "withheld" || c.WithheldReason != "age" {
			t.Fatalf("a fresh temp candidate must be withheld for age at prune, got %+v", c)
		}
	}
	if n != 1 {
		t.Fatalf("the fresh temp candidate must be present exactly once at prune (got %d)", n)
	}
}
