package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
)

func TestResolvePath_Precedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Pin XDG so the default path resolves to ~/.config (gofulmen/config honors XDG_CONFIG_HOME).
	t.Setenv("XDG_CONFIG_HOME", "")

	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		Vendor:     "fulmenhq",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}

	if got := ResolvePath(identity, "~/explicit.yaml"); got != filepath.Join(home, "explicit.yaml") {
		t.Fatalf("expected explicit path to win, got %s", got)
	}

	t.Setenv("SPANWIT_CONFIG_PATH", "~/from-env.yaml")
	if got := ResolvePath(identity, ""); got != filepath.Join(home, "from-env.yaml") {
		t.Fatalf("expected env path, got %s", got)
	}

	t.Setenv("SPANWIT_CONFIG_PATH", "")
	if got := ResolvePath(identity, ""); got != filepath.Join(home, ".config", "spanwit", "config.yaml") {
		t.Fatalf("expected default path, got %s", got)
	}
}

func TestLoadConfig_StrictFile(t *testing.T) {
	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		Vendor:     "fulmenhq",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`version: 1
defaults:
  min_size: 10M
  min_age: 30d
paths:
  - path: ~/dev
    enabled: true
    max_depth: 4
    targets:
      - pattern: "**/target"
        min_size: 1G
    ignores:
      - "**/.git/**"
`)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, loadedPath, err := LoadConfig(context.Background(), identity, logger, configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if loadedPath != configPath {
		t.Fatalf("expected loaded path %s, got %s", configPath, loadedPath)
	}
	if cfg.Defaults.MinSize != "10M" {
		t.Fatalf("expected min_size 10M, got %s", cfg.Defaults.MinSize)
	}
	if len(cfg.Paths) != 1 || len(cfg.Paths[0].Targets) != 1 {
		t.Fatalf("expected one path with one target, got %#v", cfg.Paths)
	}
	if !cfg.Paths[0].IsEnabled() {
		t.Fatalf("expected path to be enabled")
	}
}

func TestLoadConfig_RejectsUnknownFields(t *testing.T) {
	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("version: 1\nunknown: true\npaths:\n  - path: .\n")
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, _, err := LoadConfig(context.Background(), identity, logger, configPath); err == nil {
		t.Fatalf("expected unknown field to be rejected")
	}
}

func TestLoadConfig_AcceptsSignatureTargets(t *testing.T) {
	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`version: 1
defaults: {}
signatures:
  development:
    dbt:
      target:
        candidate_patterns:
          - "**/target"
        required_ancestor_files:
          - dbt_project.yml
        confidence: high
        safe_to_prune: true
paths:
  - path: .
    targets:
      - signature: development.rust.cargo-target
      - signature: development.dbt.target
`)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, _, err := LoadConfig(context.Background(), identity, logger, configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if len(cfg.Paths[0].Targets) != 2 {
		t.Fatalf("expected two signature targets, got %#v", cfg.Paths[0].Targets)
	}
	if cfg.Defaults.MinSize != "" || cfg.Defaults.MaxAge != "" {
		t.Fatalf("empty defaults: must not inject size/age gates, got %#v", cfg.Defaults)
	}
}

func TestLoadConfig_OmittedAgeAndSizeStayUnset(t *testing.T) {
	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	// Partial defaults: only min_size — must not inherit a ghost age floor.
	data := []byte(`version: 1
defaults:
  min_size: 10M
paths:
  - path: .
    targets:
      - signature: development.rust.cargo-target
`)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, _, err := LoadConfig(context.Background(), identity, logger, configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.Defaults.MinSize != "10M" {
		t.Fatalf("expected explicit min_size 10M, got %q", cfg.Defaults.MinSize)
	}
	if cfg.Defaults.MaxAge != "" {
		t.Fatalf("expected omitted max_age to stay unset, got %q", cfg.Defaults.MaxAge)
	}
}

func TestLoadConfig_EmptyDefaultsMeansNoGates(t *testing.T) {
	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`version: 1
defaults: {}
paths:
  - path: .
    targets:
      - signature: development.rust.cargo-target
`)
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, _, err := LoadConfig(context.Background(), identity, logger, configPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.Defaults.MinSize != "" {
		t.Fatalf("expected no min_size gate, got %q", cfg.Defaults.MinSize)
	}
	if cfg.Defaults.MaxAge != "" {
		t.Fatalf("expected no max_age gate (no ghost 90d), got %q", cfg.Defaults.MaxAge)
	}
}
