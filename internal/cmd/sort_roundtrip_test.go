package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"

	"github.com/3leaps/spanwit/internal/space"
)

func TestFromSpaceReport_InheritsCarrierSortMode(t *testing.T) {
	tmp := t.TempDir()
	// Two cargo targets with inverse size vs activity for sort distinction.
	coldBig := filepath.Join(tmp, "cold")
	hotSmall := filepath.Join(tmp, "hot")
	writeFile(t, filepath.Join(coldBig, "Cargo.toml"), 32)
	writeFile(t, filepath.Join(coldBig, "target", "debug", "lib"), 8192)
	writeFile(t, filepath.Join(hotSmall, "Cargo.toml"), 32)
	writeFile(t, filepath.Join(hotSmall, "target", "debug", "lib"), 1024)
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(filepath.Join(coldBig, "target", "debug", "lib"), old, old)

	// Default space is idle-ranked.
	report, err := space.Analyze(context.Background(), space.Options{
		Path: tmp, MinSize: "1B", IncludeHomeCaches: false, MaxDepth: 8,
		Now: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.SortMode != "idle" {
		t.Fatalf("space default sort=%s", report.SortMode)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(tmp, "space.json")
	if err := os.WriteFile(reportPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	identity := &appidentity.Identity{BinaryName: "spanwit", Description: "test"}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatal(err)
	}
	Initialize(context.Background(), identity, logger)

	// Default from-space-report (no --sort) must inherit carrier idle.
	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--from-space-report", reportPath, "--format", "json"})
	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("prune: %v", err)
		}
	})
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if out["sort_mode"] != "idle" {
		t.Fatalf("expected inherited idle, got %v", out["sort_mode"])
	}

	// Explicit --sort size overrides advisory carrier.
	cmd2 := newPruneCmd(identity)
	cmd2.SetArgs([]string{"--from-space-report", reportPath, "--format", "json", "--sort", "size"})
	stdout2 := captureStdout(t, func() {
		if err := cmd2.Execute(); err != nil {
			t.Fatalf("prune: %v", err)
		}
	})
	var out2 map[string]any
	if err := json.Unmarshal([]byte(stdout2), &out2); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout2)
	}
	if out2["sort_mode"] != "size" {
		t.Fatalf("expected explicit size override, got %v", out2["sort_mode"])
	}
}

func TestFromSpaceReport_EmptyReportSchemaValid(t *testing.T) {
	// Zero-candidate space report must replay as a schema-valid empty prune plan
	// retaining carrier sort_mode/reclaim_scope.
	tmp := t.TempDir()
	emptyRoot := filepath.Join(tmp, "empty-root")
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := space.Analyze(context.Background(), space.Options{
		Path: emptyRoot, IncludeHomeCaches: false, MaxDepth: 2,
		SortMode: "idle", ReclaimScope: "whole",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.PruneHandoff != nil && report.PruneHandoff.Present {
		t.Fatalf("expected no present handoff, got present=%v count=%d",
			report.PruneHandoff.Present, report.PruneHandoff.PrunableCount)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(tmp, "empty-space.json")
	if err := os.WriteFile(reportPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	identity := &appidentity.Identity{BinaryName: "spanwit", Description: "test"}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatal(err)
	}
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--from-space-report", reportPath, "--format", "json"})
	var execErr error
	stdout := captureStdout(t, func() {
		execErr = cmd.Execute()
	})
	if execErr != nil {
		t.Fatalf("empty report replay failed: %v\nstdout=%s", execErr, stdout)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if out["sort_mode"] != "idle" {
		t.Fatalf("expected carrier idle, got %v", out["sort_mode"])
	}
	if out["reclaim_scope"] != "whole" {
		t.Fatalf("expected carrier whole, got %v", out["reclaim_scope"])
	}
	cands, _ := out["candidates"].([]any)
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(cands))
	}
	eff, _ := out["effective_filters"].(map[string]any)
	for _, k := range []string{"min_size", "min_age", "max_age"} {
		v, _ := eff[k].(string)
		if v == "" {
			t.Fatalf("effective_filters.%s empty", k)
		}
	}

	// Explicit sort override still works on empty carrier.
	cmd2 := newPruneCmd(identity)
	cmd2.SetArgs([]string{"--from-space-report", reportPath, "--format", "json", "--sort", "size"})
	stdout2 := captureStdout(t, func() {
		if err := cmd2.Execute(); err != nil {
			t.Fatalf("empty report override failed: %v", err)
		}
	})
	var out2 map[string]any
	if err := json.Unmarshal([]byte(stdout2), &out2); err != nil {
		t.Fatal(err)
	}
	if out2["sort_mode"] != "size" {
		t.Fatalf("expected explicit size override, got %v", out2["sort_mode"])
	}
}

func TestFromSpaceReport_SpaceSizeCarriesThrough(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	report, err := space.Analyze(context.Background(), space.Options{
		Path: tmp, MinSize: "1B", IncludeHomeCaches: false, MaxDepth: 6,
		SortMode: "size",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(tmp, "space.json")
	if err := os.WriteFile(reportPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	identity := &appidentity.Identity{BinaryName: "spanwit", Description: "test"}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatal(err)
	}
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--from-space-report", reportPath, "--format", "json"})
	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("prune: %v", err)
		}
	})
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatal(err)
	}
	if out["sort_mode"] != "size" {
		t.Fatalf("expected size carrier, got %v", out["sort_mode"])
	}
}
