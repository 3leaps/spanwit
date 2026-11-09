package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"

	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/space"
)

// freshnessReport scans an aged cargo tree with the real space analyzer and
// writes the report, so the carried baseline is exactly what production
// serializes (RFC3339Nano activity, complete size).
func freshnessReport(t *testing.T) (reportPath, target string) {
	t.Helper()
	analysis := t.TempDir()
	target = e2eCargo(t, filepath.Join(analysis, "app"))
	// Sub-second component on purpose: the carrier must round-trip it.
	e2eAge(t, analysis)
	r, err := space.Analyze(context.Background(), space.Options{Path: analysis, MinSize: "1K", Top: 20})
	if err != nil {
		t.Fatalf("space: %v", err)
	}
	if r.PruneHandoff == nil || r.PruneHandoff.ExactPlan == nil || len(r.PruneHandoff.ExactPlan.Candidates) != 1 {
		t.Fatalf("want one handoff candidate, got %+v", r.PruneHandoff)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	reportPath = filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(reportPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return reportPath, target
}

func onlyCandidate(t *testing.T, p PrunePlan) PruneCandidate {
	t.Helper()
	if len(p.Candidates) != 1 {
		t.Fatalf("want 1 candidate, got %+v (warnings %v)", p.Candidates, p.Warnings)
	}
	return p.Candidates[0]
}

func TestFreshnessE2E_UnchangedRoundTripIsPrunable(t *testing.T) {
	e2eSkip(t)
	path, _ := freshnessReport(t)
	raw, _ := os.ReadFile(path)
	if !regexp.MustCompile(`"last_activity_at":"[^"]*\.[0-9]+Z"`).Match(raw) {
		t.Fatalf("space must carry sub-second activity: %s", raw)
	}
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	c := onlyCandidate(t, plan)
	if c.State != candidateStatePrunable || c.Drift == nil || c.Drift.Size != engine.DriftUnchanged || c.Drift.Activity != engine.DriftUnchanged {
		t.Fatalf("unchanged aged tree must stay prunable with clean drift: %+v drift=%+v", c, c.Drift)
	}
	if c.EffectiveMinAge != engine.HandoffAgeFloor {
		t.Fatalf("none carrier must get the handoff floor, got %q", c.EffectiveMinAge)
	}
}

func TestFreshnessE2E_LegacySecondReportUnchangedTree(t *testing.T) {
	e2eSkip(t)
	path, _ := freshnessReport(t)
	raw, _ := os.ReadFile(path)
	// Rewrite to what a pre-change carrier wrote: whole-second RFC3339.
	legacy := regexp.MustCompile(`("last_activity_at":"[^".]*)\.[0-9]+Z"`).ReplaceAll(raw, []byte(`${1}Z"`))
	if bytes.Equal(legacy, raw) {
		t.Fatal("fixture did not rewrite any timestamp")
	}
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	c := onlyCandidate(t, plan)
	if c.State != candidateStatePrunable || c.Drift.Activity != engine.DriftUnchanged {
		t.Fatalf("legacy whole-second baseline must not flag an unchanged tree: %+v drift=%+v", c, c.Drift)
	}
}

func TestFreshnessE2E_WriteAfterPlanWithheld(t *testing.T) {
	e2eSkip(t)
	path, target := freshnessReport(t)
	if err := os.WriteFile(filepath.Join(target, "debug", "y.o"), make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	c := onlyCandidate(t, plan)
	if c.State != candidateStateWithheld || c.WithheldReason != withheldReasonAge {
		t.Fatalf("fresh write must be withheld by the handoff floor: %+v", c)
	}
	if c.Drift.Activity != engine.DriftChanged || c.Drift.Size != engine.DriftChanged {
		t.Fatalf("drift facts must survive age withholding: %+v", c.Drift)
	}
	if plan.Freshness == nil || plan.Freshness.ActivityAfterPlan != 1 || plan.Freshness.SizeDrift != 1 {
		t.Fatalf("freshness=%+v", plan.Freshness)
	}
	var out bytes.Buffer
	writePlanText(&out, plan)
	// The delta is on the plan's size basis: on the allocated basis a 1K
	// write reads as the blocks it took, which depends on the filesystem.
	if c.Drift.SizeDeltaBytes == nil || *c.Drift.SizeDeltaBytes < 1024 {
		t.Fatalf("delta=%v, want at least the 1K written", c.Drift.SizeDeltaBytes)
	}
	delta := "[drift size=changed(+" + humanSize(*c.Drift.SizeDeltaBytes) + ") activity=changed]"
	for _, want := range []string{"Freshness: handoff floor 1h", "1 activity_after_plan, 1 size_drift", delta} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("text missing %q:\n%s", want, out.String())
		}
	}
}

// The 11-04 repro: a none carrier with rows idle about a day. --min-age 48h
// must withhold them; before this change prune had no way to ask.
func TestFreshnessE2E_MinAgeRaisesNoneCarrier(t *testing.T) {
	e2eSkip(t)
	path, target := freshnessReport(t)
	day := time.Now().Add(-26 * time.Hour)
	e2eAgeAt(t, filepath.Dir(target), day)
	// Re-scan so the baseline matches the day-old tree.
	r, err := space.Analyze(context.Background(), space.Options{Path: filepath.Dir(filepath.Dir(target)), MinSize: "1K", Top: 20})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path, "48h")
	if err != nil {
		t.Fatal(err)
	}
	c := onlyCandidate(t, plan)
	if c.State != candidateStateWithheld || c.WithheldReason != withheldReasonAge || c.EffectiveMinAge != "48h" {
		t.Fatalf("--min-age 48h must withhold a day-old row: %+v", c)
	}
	if plan.Freshness.RequestedMinAge != "48h" {
		t.Fatalf("freshness=%+v", plan.Freshness)
	}
}

func e2eAgeAt(t *testing.T, path string, when time.Time) {
	t.Helper()
	var paths []string
	_ = filepath.WalkDir(path, func(p string, _ os.DirEntry, err error) error {
		if err == nil {
			paths = append(paths, p)
		}
		return nil
	})
	for i := len(paths) - 1; i >= 0; i-- {
		if err := os.Chtimes(paths[i], when, when); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFreshness_EmptyReplayCarriesPolicyAndValidates(t *testing.T) {
	plan := emptyPlanFromSpaceReport(space.Report{}, "48h")
	var buf bytes.Buffer
	if err := writePrunePlanJSON(&buf, plan, "from-space-report:x.json", false, time.Now()); err != nil {
		t.Fatalf("schema: %v", err)
	}
	var got struct {
		Freshness map[string]any `json:"freshness"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Freshness["handoff_floor"] != "1h" || got.Freshness["requested_min_age"] != "48h" || got.Freshness["handoff_floor_applied"] != true {
		t.Fatalf("freshness=%v", got.Freshness)
	}
}

func TestFreshness_DriftJSONValidates(t *testing.T) {
	delta := int64(-3072)
	plan := PrunePlan{
		Freshness: engine.NewFreshness(engine.RequestedMinAgeNone),
		Candidates: []PruneCandidate{{
			Path: "/x/target", Size: 1024, State: candidateStateWithheld, WithheldReason: engine.WithheldReasonSizeDrift,
			Confidence: "high", EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none",
			Drift: &engine.CandidateDrift{Size: engine.DriftChanged, SizeDeltaBytes: &delta, Activity: engine.DriftUnknown},
		}},
	}
	var buf bytes.Buffer
	if err := writePrunePlanJSON(&buf, plan, "from-exact-plan:x.json", false, time.Now()); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if !strings.Contains(buf.String(), `"size_delta_bytes": -3072`) || !strings.Contains(buf.String(), `"handoff_floor_applied": false`) {
		t.Fatalf("json:\n%s", buf.String())
	}
}

func freshnessPruneCmd(t *testing.T, args ...string) error {
	t.Helper()
	identity := &appidentity.Identity{
		BinaryName: "spanwit", Vendor: "fulmenhq", EnvPrefix: "SPANWIT_", ConfigName: "spanwit", Description: "Test tool",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatal(err)
	}
	configPath = ""
	Initialize(context.Background(), identity, logger)
	cmd := newPruneCmd(identity)
	cmd.SetArgs(args)
	var runErr error
	_ = captureStdout(t, func() { runErr = cmd.Execute() })
	return runErr
}

func TestFreshness_ReplayRejectsExplicitConfig(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(cfg, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"--from-space-report", "--from-exact-plan"} {
		for _, flag := range []string{"-c", "--config"} {
			err := freshnessPruneCmd(t, mode, "/nonexistent.json", flag, cfg)
			if err == nil || !strings.Contains(err.Error(), "--config/-c is not valid") {
				t.Fatalf("%s %s: want config rejection, got %v", mode, flag, err)
			}
		}
	}
}

func TestFreshness_MinAgeFlagValidation(t *testing.T) {
	for _, mode := range []string{"--from-space-report", "--from-exact-plan"} {
		for _, bad := range []string{"0", "0h", "-1h", "1.5h", "abc"} {
			err := freshnessPruneCmd(t, mode, "/nonexistent.json", "--min-age", bad)
			if err == nil || !strings.Contains(err.Error(), "invalid --min-age") {
				t.Fatalf("%s --min-age %q: want rejection, got %v", mode, bad, err)
			}
		}
	}
	err := freshnessPruneCmd(t, "--min-age", "48h")
	if err == nil || !strings.Contains(err.Error(), "--min-age is only valid with") {
		t.Fatalf("config mode --min-age: want rejection, got %v", err)
	}
}

func TestFreshness_ConfigEnvNotedNotRefused(t *testing.T) {
	t.Setenv("SPANWIT_CONFIG_PATH", "/somewhere/spanwit.yaml")
	report := filepath.Join(t.TempDir(), "r.json")
	raw, _ := json.Marshal(space.Report{})
	_ = os.WriteFile(report, raw, 0o600)
	orig := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	err := freshnessPruneCmd(t, "--from-space-report", report)
	_ = w.Close()
	os.Stderr = orig
	stderr, _ := io.ReadAll(r)
	if err != nil && strings.Contains(err.Error(), "--config/-c is not valid") {
		t.Fatalf("ambient env must not be refused: %v", err)
	}
	if !strings.Contains(string(stderr), "SPANWIT_CONFIG_PATH is set; config not used in replay mode") {
		t.Fatalf("want env note on stderr, got:\n%s", stderr)
	}
}
