package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
)

func TestVersionCommand_Default(t *testing.T) {
	SetVersionInfo("1.2.3", "abc123", "2025-01-01T00:00:00Z")

	identity := &appidentity.Identity{
		BinaryName: "test-tool",
	}

	cmd := newVersionCmd(identity)
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("version command returned error: %v", err)
		}
	})

	expected := "test-tool 1.2.3\n"
	if out != expected {
		t.Fatalf("expected version output %q, got %q", expected, out)
	}
}

func TestVersionCommand_Extended(t *testing.T) {
	SetVersionInfo("2.0.0", "deadbeef", "2025-02-02T12:00:00Z")

	identity := &appidentity.Identity{
		BinaryName: "test-tool",
	}

	cmd := newVersionCmd(identity)
	cmd.SetArgs([]string{"--extended"})

	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("version --extended returned error: %v", err)
		}
	})

	if !bytes.Contains([]byte(out), []byte("Version:         2.0.0")) {
		t.Fatalf("expected extended output to include version, got %q", out)
	}
	if !bytes.Contains([]byte(out), []byte("Commit:          deadbeef")) {
		t.Fatalf("expected commit in extended output")
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdout = w

	fn()

	_ = w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()
	return buf.String()
}

// spanwit --version prints the same line as `spanwit version`.
func TestRootVersionFlag(t *testing.T) {
	SetVersionInfo("1.2.3", "abc123", "2025-01-01T00:00:00Z")
	identity := &appidentity.Identity{
		BinaryName: "spanwit", Vendor: "fulmenhq", EnvPrefix: "SPANWIT_",
		ConfigName: "spanwit", Description: "Test tool",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}
	Initialize(context.Background(), identity, logger)
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"--version"})
	defer rootCmd.SetArgs(nil)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("--version returned error: %v", err)
	}
	if got := buf.String(); got != "spanwit 1.2.3\n" {
		t.Fatalf("--version output %q", got)
	}
}
