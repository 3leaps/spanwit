package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"

	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/space"
)

func TestPruneFromSpaceReport_CustomSignatureParity(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "proj", "BUILD.marker"), 8)
	writeFile(t, filepath.Join(tmp, "proj", "build-out", "obj", "a"), 4096)

	// Additive config used by space
	cfgPath := filepath.Join(tmp, "custom.yaml")
	cfgYAML := []byte(`version: 1
defaults: {}
signatures:
  development:
    custom:
      build:
        candidate_patterns: ["**/build-out"]
        required_ancestor_files: ["BUILD.marker"]
        any_child_paths: ["obj"]
        confidence: high
        safe_to_prune: true
paths:
  - path: ` + tmp + `
    max_depth: 8
    targets:
      - signature: development.custom.build
`)
	if err := os.WriteFile(cfgPath, cfgYAML, 0o644); err != nil {
		t.Fatal(err)
	}

	identity := &appidentity.Identity{BinaryName: "spanwit", Description: "test"}
	logger, err := logging.NewCLI("spanwit")
	if err != nil {
		t.Fatal(err)
	}
	Initialize(context.Background(), identity, logger)

	// space Analyze with custom config
	userCfg, _, err := config.LoadConfig(context.Background(), identity, logger, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err := space.Analyze(context.Background(), space.Options{
		Path:              tmp,
		MinSize:           "1K",
		IncludeHomeCaches: false,
		Config:            userCfg,
		MaxDepth:          8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.PruneHandoff == nil || !report.PruneHandoff.Present {
		t.Fatalf("expected handoff: verified=%#v", report.Verified)
	}
	// Write report JSON
	reportPath := filepath.Join(tmp, "space.json")
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// Bare allowlist must miss custom
	bare, err := buildPrunePlanFromAllowlist(context.Background(), []string{
		filepath.Join(tmp, "proj", "build-out"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.Candidates) != 0 {
		t.Fatalf("bare allowlist should miss custom: %+v", bare.Candidates)
	}

	// from-space-report preserves custom signature
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].State != candidateStatePrunable {
		t.Fatalf("exact plan revalidate: %+v warnings=%v", plan.Candidates, plan.Warnings)
	}
	if plan.Candidates[0].Signature != "development.custom.build" {
		t.Fatal(plan.Candidates[0].Signature)
	}

	// CLI dry-run
	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--from-space-report", reportPath})
	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(stdout, "development.custom.build") {
		t.Fatalf("stdout missing custom sig: %s", stdout)
	}
}

func TestPruneAllowlist_ExecuteRefused(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	identity := &appidentity.Identity{BinaryName: "spanwit"}
	logger, _ := logging.NewCLI("spanwit")
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--allowlist", filepath.Join(tmp, "app", "target"), "--execute"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "dry-run only") {
		t.Fatalf("expected execute refusal, got %v", err)
	}
}

func TestExecuteGuard_SymlinkAncestorRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix")
	}
	tmp := t.TempDir()
	outside := filepath.Join(tmp, "outside", "app", "target")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "root", "link")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), link); err != nil {
		t.Fatal(err)
	}
	via := filepath.Join(link, "target")
	// The shared execute guard rejects a symlink path component even with valid
	// provenance and a boundary that lexically contains the target.
	_, err := engine.AuthorizeDeletion(engine.DeletionRequest{
		Target:     via,
		Boundary:   filepath.Join(tmp, "root"),
		Provenance: engine.ProvenanceConfigProfile,
	})
	if err == nil {
		t.Fatal("expected symlink ancestor rejection")
	}
}

func TestPruneFromSpaceReport_JSONRoundTrip(t *testing.T) {
	// Ensure schema-valid space report with exact_plan unmarshals for prune.
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	report, err := space.Analyze(context.Background(), space.Options{
		Path: tmp, MinSize: "1K", IncludeHomeCaches: false, MaxDepth: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "r.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := buildPrunePlanFromSpaceReport(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("candidates=%d", len(plan.Candidates))
	}
	_ = bytes.Buffer{}
}
