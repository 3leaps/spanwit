package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
)

func TestPruneCommand_AllowlistScopesToExactPaths(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "keep", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "keep", "target", "debug", "a"), 2048)
	writeFile(t, filepath.Join(tmp, "other", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "other", "target", "debug", "b"), 4096)

	allow := filepath.Join(tmp, "keep", "target")
	sibling := filepath.Join(tmp, "other", "target")

	identity := &appidentity.Identity{
		BinaryName:  "spanwit",
		Vendor:      "fulmenhq",
		EnvPrefix:   "SPANWIT_",
		ConfigName:  "spanwit",
		Description: "Test tool",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatal(err)
	}
	configPath = ""
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--allowlist", allow})

	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("prune allowlist: %v", err)
		}
	})
	if !strings.Contains(stdout, allow) {
		t.Fatalf("expected allowlisted path in output:\n%s", stdout)
	}
	if strings.Contains(stdout, sibling) {
		t.Fatalf("sibling path must not appear:\n%s", stdout)
	}
	// Dry-run: path still exists.
	if _, err := os.Stat(allow); err != nil {
		t.Fatalf("dry-run deleted path: %v", err)
	}
	if !bytes.Contains([]byte(stdout), []byte("Prune candidates:")) {
		t.Fatalf("missing candidates header: %s", stdout)
	}
}

func TestPruneCommand_AllowlistRejectsUnverified(t *testing.T) {
	tmp := t.TempDir()
	// Name-shaped only — no Cargo.toml
	writeFile(t, filepath.Join(tmp, "other", "target", "debug", "b"), 2048)
	unverified := filepath.Join(tmp, "other", "target")

	identity := &appidentity.Identity{BinaryName: "spanwit", Description: "test"}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatal(err)
	}
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--allowlist", unverified})
	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("prune: %v", err)
		}
	})
	// Plan should have no prunable reclaimable candidates listed as sizes for unverified.
	// Text may show empty candidates section.
	if strings.Contains(stdout, "development.rust.cargo-target") && strings.Contains(stdout, unverified) {
		// If path appears it should not be as a prunable cargo signature line.
		// Empty plan is success; presence of signature for this path is failure.
		t.Fatalf("unverified must not be planned as cargo-target:\n%s", stdout)
	}
}

func TestPruneCommand_AllowlistRejectsReclaimScopeFlag(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)
	allow := filepath.Join(tmp, "app", "target")

	identity := &appidentity.Identity{BinaryName: "spanwit", Description: "test"}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatal(err)
	}
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--allowlist", allow, "--reclaim-scope", "incremental"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--reclaim-scope is not supported with --allowlist") {
		t.Fatalf("expected reclaim-scope rejection, got %v", err)
	}
}

func TestPruneCommand_FromSpaceReportRejectsReclaimScopeFlag(t *testing.T) {
	// Flag conflict is rejected before file open when Changed("reclaim-scope").
	identity := &appidentity.Identity{BinaryName: "spanwit", Description: "test"}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatal(err)
	}
	Initialize(context.Background(), identity, logger)

	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--from-space-report", "/nonexistent/report.json", "--reclaim-scope", "incremental"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--reclaim-scope is not valid with --from-space-report") {
		t.Fatalf("expected reclaim-scope rejection on from-space-report, got %v", err)
	}
}
