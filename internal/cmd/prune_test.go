package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

func TestPruneCommand_DryRunPrintsPlan(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "project", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "project", "target", "debug", "app.o"), 2048)
	mustChtimes(t, filepath.Join(tmp, "project", "target"), time.Now().Add(-48*time.Hour))

	configFile := filepath.Join(tmp, "config.yaml")
	configData := []byte(`version: 1
defaults:
  min_size: 1K
  min_age: 24h
paths:
  - path: ` + tmp + `
    max_depth: 3
    targets:
      - signature: development.rust.cargo-target
`)
	if err := os.WriteFile(configFile, configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	identity := &appidentity.Identity{
		BinaryName:  "spanwit",
		Vendor:      "fulmenhq",
		EnvPrefix:   "SPANWIT_",
		ConfigName:  "spanwit",
		Description: "Test tool",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}
	configPath = ""
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--config", configFile})

	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("prune command returned error: %v", err)
		}
	})

	if !bytes.Contains([]byte(stdout), []byte("Prune candidates:")) {
		t.Fatalf("expected candidate header, got %q", stdout)
	}
	if !bytes.Contains([]byte(stdout), []byte("development.rust.cargo-target")) {
		t.Fatalf("expected signature in output, got %q", stdout)
	}
	if !bytes.Contains([]byte(stdout), []byte(filepath.Join(tmp, "project", "target"))) {
		t.Fatalf("expected target path in output, got %q", stdout)
	}
}

func TestPruneCommand_JSONOutputMatchesSchema(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "project", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "project", "target", "debug", "app.o"), 2048)
	mustChtimes(t, filepath.Join(tmp, "project", "target"), time.Now().Add(-48*time.Hour))

	configFile := filepath.Join(tmp, "config.yaml")
	configData := []byte(`version: 1
defaults:
  min_size: 1K
  min_age: 24h
paths:
  - path: ` + tmp + `
    max_depth: 3
    targets:
      - signature: development.rust.cargo-target
`)
	if err := os.WriteFile(configFile, configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	identity := &appidentity.Identity{
		BinaryName:  "spanwit",
		Vendor:      "fulmenhq",
		EnvPrefix:   "SPANWIT_",
		ConfigName:  "spanwit",
		Description: "Test tool",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}
	configPath = ""
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--config", configFile, "--format", "json"})

	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("prune command returned error: %v", err)
		}
	})

	if err := contract.ValidateJSON(spanwitschema.SpanwitPrunePlanV1, []byte(stdout)); err != nil {
		t.Fatalf("json output failed schema validation: %v\n%s", err, stdout)
	}

	var payload prunePlanOutput
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("json output is not decodable: %v", err)
	}
	if payload.Mode != "dry-run" {
		t.Fatalf("expected dry-run mode, got %q", payload.Mode)
	}
	if payload.Summary.Candidates != 1 || len(payload.Candidates) != 1 {
		t.Fatalf("expected one candidate, got %#v", payload)
	}
	if payload.Candidates[0].Signature != "development.rust.cargo-target" {
		t.Fatalf("expected signature in json output, got %#v", payload.Candidates[0])
	}
}

func TestPruneCommand_ExecuteDeletesCandidates(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "project", "Cargo.toml"), 64)
	targetPath := filepath.Join(tmp, "project", "target")
	writeFile(t, filepath.Join(targetPath, "debug", "app.o"), 2048)
	mustChtimes(t, targetPath, time.Now().Add(-48*time.Hour))

	configFile := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configFile, []byte(`version: 1
defaults:
  min_size: 1K
  min_age: 24h
paths:
  - path: `+tmp+`
    max_depth: 3
    targets:
      - signature: development.rust.cargo-target
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}
	configPath = ""
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--config", configFile, "--execute"})

	_ = captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("prune command returned error: %v", err)
		}
	})
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("expected target path to be deleted, stat err=%v", err)
	}
}

func TestPruneCommand_ExecuteNeverDeletesWithheld(t *testing.T) {
	tmp := t.TempDir()
	// Safe, signature-backed match (built-in cargo-target) -> deleted.
	writeFile(t, filepath.Join(tmp, "rust-app", "Cargo.toml"), 64)
	safePath := filepath.Join(tmp, "rust-app", "target")
	writeFile(t, filepath.Join(safePath, "debug", "app.o"), 2048)
	mustChtimes(t, safePath, time.Now().Add(-48*time.Hour))
	// Not-safe, signature-backed match -> withheld, must survive --execute.
	withheldPath := filepath.Join(tmp, "etl", "node_modules")
	writeFile(t, filepath.Join(withheldPath, "dep", "index.js"), 2048)
	mustChtimes(t, withheldPath, time.Now().Add(-48*time.Hour))

	configFile := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configFile, []byte(`version: 1
defaults:
  min_size: 1K
  min_age: 24h
signatures:
  development:
    node:
      modules:
        candidate_patterns: ["**/node_modules"]
        confidence: medium
        safe_to_prune: false
paths:
  - path: `+tmp+`
    max_depth: 3
    targets:
      - signature: development.rust.cargo-target
      - signature: development.node.modules
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}
	configPath = ""
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--config", configFile, "--execute"})

	_ = captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("prune command returned error: %v", err)
		}
	})

	if _, err := os.Stat(safePath); !os.IsNotExist(err) {
		t.Fatalf("expected safe target to be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(withheldPath); err != nil {
		t.Fatalf("expected withheld match to survive --execute, stat err=%v", err)
	}
}

func TestPruneCommand_WithheldJSONReportsState(t *testing.T) {
	tmp := t.TempDir()
	withheldPath := filepath.Join(tmp, "etl", "node_modules")
	writeFile(t, filepath.Join(withheldPath, "dep", "index.js"), 2048)
	mustChtimes(t, withheldPath, time.Now().Add(-48*time.Hour))

	configFile := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configFile, []byte(`version: 1
defaults:
  min_size: 1K
  min_age: 24h
signatures:
  development:
    node:
      modules:
        candidate_patterns: ["**/node_modules"]
        confidence: medium
        safe_to_prune: false
paths:
  - path: `+tmp+`
    max_depth: 3
    targets:
      - signature: development.node.modules
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}
	configPath = ""
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--config", configFile, "--format", "json"})

	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("prune command returned error: %v", err)
		}
	})

	if err := contract.ValidateJSON(spanwitschema.SpanwitPrunePlanV1, []byte(stdout)); err != nil {
		t.Fatalf("json output failed schema validation: %v\n%s", err, stdout)
	}
	var payload prunePlanOutput
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("json output is not decodable: %v", err)
	}
	if payload.Summary.WithheldCandidates != 1 || payload.Summary.PrunableCandidates != 0 {
		t.Fatalf("expected 1 withheld / 0 prunable, got %#v", payload.Summary)
	}
	if len(payload.Candidates) != 1 {
		t.Fatalf("expected withheld match reported, got %#v", payload.Candidates)
	}
	if payload.Candidates[0].State != candidateStateWithheld {
		t.Fatalf("expected withheld state, got %q", payload.Candidates[0].State)
	}
	if payload.Candidates[0].WithheldReason != withheldReasonSafeToPrune {
		t.Fatalf("expected reason %q, got %q", withheldReasonSafeToPrune, payload.Candidates[0].WithheldReason)
	}
}

func TestPruneCommand_JSONExecuteOutputMatchesSchema(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "project", "Cargo.toml"), 64)
	targetPath := filepath.Join(tmp, "project", "target")
	writeFile(t, filepath.Join(targetPath, "debug", "app.o"), 2048)
	mustChtimes(t, targetPath, time.Now().Add(-48*time.Hour))

	configFile := filepath.Join(tmp, "config.yaml")
	configData := []byte(`version: 1
defaults:
  min_size: 1K
  min_age: 24h
paths:
  - path: ` + tmp + `
    max_depth: 3
    targets:
      - signature: development.rust.cargo-target
`)
	if err := os.WriteFile(configFile, configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}
	configPath = ""
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--config", configFile, "--format", "json", "--execute"})

	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("prune command returned error: %v", err)
		}
	})

	if err := contract.ValidateJSON(spanwitschema.SpanwitPrunePlanV1, []byte(stdout)); err != nil {
		t.Fatalf("json output failed schema validation: %v\n%s", err, stdout)
	}
	var payload prunePlanOutput
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("json output is not decodable: %v", err)
	}
	if payload.Mode != "execute" {
		t.Fatalf("expected execute mode, got %q", payload.Mode)
	}
	if payload.Execution == nil || payload.Execution.DeletedCandidates != 1 {
		t.Fatalf("expected one deleted candidate, got %#v", payload.Execution)
	}
	if len(payload.Candidates) != 1 || payload.Candidates[0].Deleted == nil || !*payload.Candidates[0].Deleted {
		t.Fatalf("expected deleted candidate in output, got %#v", payload.Candidates)
	}
}
