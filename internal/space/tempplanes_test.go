//go:build unix

package space

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/contract"
)

func probeFixture(t *testing.T, dir string, depth int, deadline time.Duration) (string, int64) {
	t.Helper()
	id, ok := statDirIdentity(dir)
	if !ok {
		t.Fatalf("stat %s", dir)
	}
	return probeTempPlane(context.Background(), dir, id.dev, depth, deadline)
}

func TestProbeTempPlane_MeasuredIsExactApparentBytes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a", "f1"), 1000)
	writeFile(t, filepath.Join(dir, "a", "b", "f2"), 234)
	status, bytes := probeFixture(t, dir, tempPlaneProbeDepth, 5*time.Second)
	if status != TempPlaneSizeMeasured || bytes != 1234 {
		t.Fatalf("got %s %d, want measured 1234", status, bytes)
	}
}

func TestProbeTempPlane_DepthCapIsPartialLowerBound(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "shallow"), 100)
	writeFile(t, filepath.Join(dir, "1", "2", "3", "4", "5", "deep"), 900)
	status, bytes := probeFixture(t, dir, 2, 5*time.Second)
	if status != TempPlaneSizePartial {
		t.Fatalf("status %s, want partial", status)
	}
	if bytes != 100 {
		t.Fatalf("lower bound %d, want 100 (deep file beyond cap)", bytes)
	}
}

// A symlink inside the plane must not be followed: its target's bytes are not
// the plane's, and following it would walk outside the admitted root.
func TestProbeTempPlane_DoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "big"), 5000)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "small"), 10)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	status, bytes := probeFixture(t, dir, tempPlaneProbeDepth, 5*time.Second)
	if status != TempPlaneSizeMeasured || bytes != 10 {
		t.Fatalf("got %s %d, want measured 10", status, bytes)
	}
}

func TestProbeTempPlane_UnreadableRootIsUnavailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "f"), 10)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	status, bytes := probeFixture(t, dir, tempPlaneProbeDepth, 5*time.Second)
	if status != TempPlaneSizeUnavailable || bytes != 0 {
		t.Fatalf("got %s %d, want unavailable 0", status, bytes)
	}
}

// The deadline bounds the call even when the walk cannot finish in time; the
// result is never reported as measured.
func TestProbeTempPlane_DeadlineNeverClaimsMeasured(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 400; i++ {
		writeFile(t, filepath.Join(dir, "d", string(rune('a'+i%26)), "f"+strings.Repeat("x", i%7)+string(rune('0'+i%10))), 1)
	}
	start := time.Now()
	status, _ := probeFixture(t, dir, tempPlaneProbeDepth, time.Nanosecond)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("probe took %s past a 1ns deadline", elapsed)
	}
	if status == TempPlaneSizeMeasured {
		t.Fatal("deadline-cut probe must not report measured")
	}
}

func TestCollectTempPlanes_DedupsByInodeNotPathString(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	writeFile(t, filepath.Join(real, "plane", "f"), 10)
	if err := os.Symlink(real, filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	analysis := t.TempDir()
	cov := collectTempPlaneCoverage(context.Background(), analysis, PressureCritical, []tempPlaneCandidate{
		{Path: filepath.Join(real, "plane"), Kind: TempPlaneKindShared},
		{Path: filepath.Join(base, "alias", "plane"), Kind: TempPlaneKindUser},
	}, 5*time.Second)
	if cov == nil || len(cov.Planes) != 1 {
		t.Fatalf("want one plane after inode dedup, got %+v", cov)
	}
}

// A candidate whose final component is a symlink is refused, never resolved.
func TestCollectTempPlanes_RefusesSymlinkedCandidate(t *testing.T) {
	target := t.TempDir()
	base := t.TempDir()
	link := filepath.Join(base, "tmp-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	cov := collectTempPlaneCoverage(context.Background(), t.TempDir(), PressureCritical,
		[]tempPlaneCandidate{{Path: link, Kind: TempPlaneKindShared}}, 5*time.Second)
	if cov != nil {
		t.Fatalf("symlinked candidate admitted: %+v", cov)
	}
}

func TestCollectTempPlanes_OmitsPlanesOverlappingAnalysisRoot(t *testing.T) {
	parent := t.TempDir()
	plane := filepath.Join(parent, "plane")
	writeFile(t, filepath.Join(plane, "inner", "f"), 10)
	for name, root := range map[string]string{
		"root is plane":        plane,
		"root contains plane":  parent,
		"root is inside plane": filepath.Join(plane, "inner"),
	} {
		cov := collectTempPlaneCoverage(context.Background(), root, PressureCritical,
			[]tempPlaneCandidate{{Path: plane, Kind: TempPlaneKindShared}}, 5*time.Second)
		if cov != nil {
			t.Errorf("%s: plane not omitted: %+v", name, cov)
		}
	}
}

// Disclosure ceiling: the coverage record names the root only — never child
// names such as session ids or snapshot SHAs.
func TestCollectTempPlanes_DisclosesRootOnly(t *testing.T) {
	plane := t.TempDir()
	child := "session-7f3a9c2e-review-ebb80fe"
	writeFile(t, filepath.Join(plane, child, "target", "blob"), 64)
	cov := collectTempPlaneCoverage(context.Background(), t.TempDir(), PressureWarn,
		[]tempPlaneCandidate{{Path: plane, Kind: TempPlaneKindShared}}, 5*time.Second)
	if cov == nil || len(cov.Planes) != 1 {
		t.Fatalf("want one plane, got %+v", cov)
	}
	raw := string(mustJSON(t, cov))
	if strings.Contains(raw, child) || strings.Contains(raw, "ebb80fe") {
		t.Fatalf("coverage leaks child name: %s", raw)
	}
	p := cov.Planes[0]
	if p.FollowUp != "spanwit space "+ShellQuote(p.Root) {
		t.Fatalf("follow-up must be root-level, got %q", p.FollowUp)
	}
}

func TestCanonicalTempPath_TrustedAliasesOnly(t *testing.T) {
	if runtime.GOOS != "darwin" {
		if got := canonicalTempPath("/tmp/"); got != "/tmp" {
			t.Fatalf("non-darwin must only clean: %q", got)
		}
		return
	}
	for in, want := range map[string]string{
		"/tmp":              "/private/tmp",
		"/var/folders/x/T/": "/private/var/folders/x/T",
		"/private/tmp":      "/private/tmp",
		"/tmpfoo":           "/tmpfoo",
		"/Users/me/tmp":     "/Users/me/tmp",
	} {
		if got := canonicalTempPath(in); got != want {
			t.Errorf("canonicalTempPath(%q)=%q want %q", in, got, want)
		}
	}
}

func TestAnalyze_TempPlaneCoverageOnlyUnderPressure(t *testing.T) {
	analysis := t.TempDir()
	plane := t.TempDir()
	writeFile(t, filepath.Join(plane, "x", "f"), 321)
	cands := []tempPlaneCandidate{{Path: plane, Kind: TempPlaneKindShared}}

	for _, tc := range []struct {
		level   string
		disable bool
		want    bool
	}{
		{PressureOK, false, false},
		{PressureWarn, false, true},
		{PressureCritical, false, true},
		{PressureCritical, true, false},
	} {
		report, err := Analyze(context.Background(), Options{
			Path: analysis, IncludeHomeCaches: false, MaxDepth: 3,
			ForcePressureLevel: tc.level, DisableTempPlanes: tc.disable,
			tempPlaneCandidates: cands, tempPlaneDeadline: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		got := report.TempPlaneCoverage != nil
		if got != tc.want {
			t.Fatalf("level=%s disable=%v: coverage present=%v want %v", tc.level, tc.disable, got, tc.want)
		}
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
			t.Fatalf("level=%s schema: %v", tc.level, err)
		}
		if got {
			p := report.TempPlaneCoverage.Planes[0]
			if report.TempPlaneCoverage.Trigger != tc.level || p.SizeStatus != TempPlaneSizeMeasured ||
				p.SizeBytes == nil || *p.SizeBytes != 321 {
				t.Fatalf("level=%s plane=%+v", tc.level, p)
			}
			// Observation only: a temp plane never enters the prune handoff.
			if report.PruneHandoff != nil {
				for _, c := range report.PruneHandoff.Candidates {
					if pathWithin(c.Path, plane) {
						t.Fatalf("temp plane leaked into prune handoff: %s", c.Path)
					}
				}
			}
		}
	}
}

func TestTempPlaneSchema_RejectsBytesWhenUnavailableAndMissingWhenMeasured(t *testing.T) {
	report := validMinimalReport(t)
	n := int64(5)
	report.TempPlaneCoverage = &TempPlaneCoverage{Trigger: PressureCritical, Planes: []TempPlane{{
		Root: "/private/tmp", Kind: TempPlaneKindShared, SizeStatus: TempPlaneSizeUnavailable,
		SizeBytes: &n, SizeBasis: "apparent", FollowUp: "spanwit space /private/tmp",
	}}}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, mustJSON(t, report)); err == nil {
		t.Fatal("schema must reject size_bytes on unavailable")
	}
	report.TempPlaneCoverage.Planes[0].SizeStatus = TempPlaneSizeMeasured
	report.TempPlaneCoverage.Planes[0].SizeBytes = nil
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, mustJSON(t, report)); err == nil {
		t.Fatal("schema must require size_bytes on measured")
	}
	report.TempPlaneCoverage.Trigger = PressureOK
	report.TempPlaneCoverage.Planes[0].SizeBytes = &n
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, mustJSON(t, report)); err == nil {
		t.Fatal("schema must reject trigger=ok")
	}
}

// space --config imports signatures/targets only; a path profile in the config
// (for example an enabled temp plane) is never walked by space. Only the
// analysis root is. This is what keeps a copied crisis profile's temp entry
// from widening a space run.
func TestAnalyze_ConfigPathProfilesAreNotActivated(t *testing.T) {
	analysis := t.TempDir()
	elsewhere := t.TempDir()
	writeFile(t, filepath.Join(elsewhere, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(elsewhere, "app", "target", "debug", "blob"), 4096)
	enabled := true
	cfg := &config.Config{
		Version: 1,
		Paths: []config.PathProfile{{
			Path: elsewhere, Enabled: &enabled, MaxDepth: 6,
			Targets: []config.Target{{Signature: "development.rust.cargo-target"}},
		}},
	}
	report, err := Analyze(context.Background(), Options{
		Path: analysis, MaxDepth: 6, IncludeHomeCaches: false, Config: cfg,
		ForcePressureLevel: PressureOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range report.Verified.Candidates {
		if pathWithin(c.Path, elsewhere) {
			t.Fatalf("space walked a config path profile: %s", c.Path)
		}
	}
	if report.PruneHandoff != nil && len(report.PruneHandoff.Candidates) > 0 {
		t.Fatalf("handoff populated from config path profile: %+v", report.PruneHandoff.Candidates)
	}

	// Control: the fixture is detectable when it is the analysis root, so the
	// empty result above is not vacuous.
	control, err := Analyze(context.Background(), Options{
		Path: elsewhere, MaxDepth: 6, IncludeHomeCaches: false, Config: cfg,
		ForcePressureLevel: PressureOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range control.Verified.Candidates {
		found = found || pathWithin(c.Path, elsewhere)
	}
	if !found {
		t.Fatal("control: fixture target not detected at its own root")
	}
}
