package space

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/engine"
)

func TestAnalyze_VerifiedCargoAndReadOnly(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "x.o"), 4096)
	// Name-shaped but not cargo-verified.
	writeFile(t, filepath.Join(tmp, "other", "target", "blob"), 2048)

	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1K",
		Top:               10,
		IncludeHomeCaches: false,
		Now:               time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if report.Schema != SchemaID || report.Version != 1 {
		t.Fatalf("schema/version: %#v", report)
	}
	if report.Pressure.Level == "" {
		t.Fatal("expected pressure level")
	}
	if report.Pressure.Role != PressureRolePrimaryWrite {
		t.Fatalf("primary pressure role: got %q", report.Pressure.Role)
	}
	if report.Verified.PrunableCandidates < 1 {
		t.Fatalf("expected verified prunable cargo target, got %#v", report.Verified)
	}
	foundCargo := false
	for _, c := range report.Verified.Candidates {
		if c.State != StatePrunable && c.State != StateWithheld {
			t.Fatalf("verified row has unexpected state: %#v", c)
		}
		if c.Signature == "development.rust.cargo-target" {
			foundCargo = true
		}
	}
	if !foundCargo {
		t.Fatalf("expected cargo-target signature in verified: %#v", report.Verified.Candidates)
	}
	if report.Unverified.Count < 1 {
		t.Fatalf("expected unverified name-shaped target, got %#v", report.Unverified)
	}
	for _, h := range report.Hotspots {
		if h.State != StateDiagnosticOnly {
			t.Fatalf("hotspot must be diagnostic-only: %#v", h)
		}
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
		t.Fatalf("schema: %v\n%s", err, raw)
	}
}

func TestAnalyze_TopTruncationDoesNotReclassifyAsUnknown(t *testing.T) {
	// Two immediate name-shaped dirs under root. With --top 1, both must stay
	// classified as unverified (count=2); neither may reappear as unknown.
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "build", "artifact"), 2048)
	writeFile(t, filepath.Join(tmp, "target", "blob"), 4096)

	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1K",
		Top:               1,
		IncludeHomeCaches: false,
		MaxDepth:          4,
		Now:               time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if report.Unverified.Count != 2 {
		t.Fatalf("unverified count: want 2, got %d (%#v)", report.Unverified.Count, report.Unverified)
	}
	if len(report.Unverified.Entries) != 1 {
		t.Fatalf("display rows: want 1, got %#v", report.Unverified.Entries)
	}
	for _, u := range report.Unknown {
		base := filepath.Base(u.Path)
		if base == "build" || base == "target" {
			t.Fatalf("name-shaped path reclassified as unknown under top truncation: %#v", u)
		}
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
		t.Fatalf("schema: %v\n%s", err, raw)
	}
}

func TestDirSizeContext_CancelDeterministic(t *testing.T) {
	tmp := t.TempDir()
	// Wide tree so cancel lands mid-walk under unlimited depth.
	for i := 0; i < 200; i++ {
		writeFile(t, filepath.Join(tmp, fmt.Sprintf("d%03d", i), "f"), 4096)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := engine.DirSizeContext(ctx, tmp, -1)
		done <- err
	}()
	time.Sleep(2 * time.Millisecond)
	cancel()
	err := <-done
	if err == nil {
		// Extremely fast machines may finish before cancel; force a second attempt
		// with pre-canceled context.
		ctx2, cancel2 := context.WithCancel(context.Background())
		cancel2()
		_, err2 := engine.DirSizeContext(ctx2, tmp, -1)
		if err2 == nil {
			t.Fatal("expected cancellation error from DirSizeContext")
		}
		return
	}
	if err != context.Canceled {
		// Accept wrapped cancel
		if ctx.Err() == nil {
			t.Fatalf("want context cancel, got %v", err)
		}
	}
}

func TestAnalyze_VerifiedSizeRespectsMaxDepth(t *testing.T) {
	// Synthetic reproduction from local review: deep file under cargo target must
	// not be counted when size is depth-bounded; size_incomplete must be set.
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "rust-app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "rust-app", "target", "shallow.bin"), 1)
	writeFile(t, filepath.Join(tmp, "rust-app", "target", "debug", "a", "b", "c", "deep.bin"), 1024*1024)

	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1",
		MaxDepth:          2, // discovers rust-app/target; sizes under target with bound 2
		Top:               10,
		IncludeHomeCaches: false,
		Now:               time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if report.Verified.PrunableCandidates < 1 {
		t.Fatalf("expected verified cargo target, got %#v", report.Verified)
	}
	c := report.Verified.Candidates[0]
	if c.SizeBytes >= 1024*1024 {
		t.Fatalf("deep file should not be fully counted under size bound; got %d", c.SizeBytes)
	}
	if c.SizeBytes < 1 {
		t.Fatalf("shallow file should be counted; got %d", c.SizeBytes)
	}
	if !c.SizeIncomplete || !report.Verified.SizeIncomplete {
		t.Fatalf("expected size_incomplete on partial verified size; entry=%#v section=%#v", c, report.Verified)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
		t.Fatalf("schema: %v\n%s", err, raw)
	}
}

func TestSameVolumeByVolumeID(t *testing.T) {
	a := Pressure{Path: "/home/u", VolumeID: "fsid:1:2", Mount: ""}
	b := Pressure{Path: "/home/u/dev", VolumeID: "fsid:1:2", Mount: ""}
	if !sameVolume(a, b) {
		t.Fatal("same VolumeID must be same volume even when paths differ")
	}
	c := Pressure{Path: "/mnt/other", VolumeID: "fsid:9:9", Mount: ""}
	if sameVolume(a, c) {
		t.Fatal("different VolumeID must not match")
	}
}

func TestDirSizeRespectsMaxDepth(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "shallow.txt"), 100)
	writeFile(t, filepath.Join(tmp, "nested", "deep.txt"), 1000)
	full, err := engine.DirSizeContext(context.Background(), tmp, -1)
	if err != nil {
		t.Fatal(err)
	}
	shallow, err := engine.DirSizeContext(context.Background(), tmp, 0)
	if err != nil {
		t.Fatal(err)
	}
	if shallow.Bytes >= full.Bytes {
		t.Fatalf("depth-0 size %d should be < full size %d", shallow.Bytes, full.Bytes)
	}
	if shallow.Bytes != 100 {
		t.Fatalf("depth-0 want 100, got %d", shallow.Bytes)
	}
	if !shallow.Incomplete {
		t.Fatal("depth-0 walk that skips nested dirs should mark Incomplete")
	}
	if full.Incomplete {
		t.Fatal("unlimited depth should not be Incomplete")
	}
}

func TestAnalyze_JSONMatchesSchemaEmptyish(t *testing.T) {
	tmp := t.TempDir()
	report, err := Analyze(context.Background(), Options{
		Path:              tmp,
		IncludeHomeCaches: false,
		Top:               5,
		Now:               time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
		t.Fatalf("schema: %v\n%s", err, raw)
	}
}

func TestPrimaryWritePathPrefersHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	got := primaryWritePath("/some/other/path")
	absHome, _ := filepath.Abs(home)
	if got != absHome && got != home {
		t.Fatalf("primaryWritePath: got %q want home %q", got, absHome)
	}
}

func TestPressureLevel(t *testing.T) {
	const gi = int64(1024 * 1024 * 1024)
	if pressureLevel(5*gi, 99) != PressureCritical {
		t.Fatal("expected critical")
	}
	if pressureLevel(15*gi, 90) != PressureWarn {
		t.Fatal("expected warn")
	}
	if pressureLevel(100*gi, 50) != PressureOK {
		t.Fatal("expected ok")
	}
}

func TestHumanSizeShared(t *testing.T) {
	if engine.HumanSize(4096) == "" {
		t.Fatal("human size empty")
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}
