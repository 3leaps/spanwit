package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
)

func TestExampleCommand_FiltersIncludeExclude(t *testing.T) {
	tmp := t.TempDir()
	// create files
	if err := os.WriteFile(filepath.Join(tmp, "keep.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write keep.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "vendor"), 0o755); err != nil {
		t.Fatalf("mkdir vendor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "vendor", "skip.txt"), []byte("skip"), 0o644); err != nil {
		t.Fatalf("write vendor/skip.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "ignore.md"), []byte("nope"), 0o644); err != nil {
		t.Fatalf("write ignore.md: %v", err)
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
	Initialize(context.Background(), identity, logger)

	cmd := newExampleCmd(identity)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--root", tmp, "--include", "*.txt", "--max-depth", "2"})

	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("example command returned error: %v", err)
		}
	})

	got := out
	if !bytes.Contains([]byte(out), []byte("keep.txt")) {
		t.Fatalf("expected output to include keep.txt, got %q", got)
	}
	if bytes.Contains([]byte(out), []byte("vendor/skip.txt")) {
		t.Fatalf("expected vendor/skip.txt to be excluded by default, got %q", got)
	}
	if bytes.Contains([]byte(out), []byte("ignore.md")) {
		t.Fatalf("expected ignore.md to be excluded by include filter, got %q", got)
	}
}
