package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
)

func TestSetupLogging(t *testing.T) {
	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		Vendor:     "fulmenhq",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}

	logger := SetupLogging(identity)
	if logger == nil {
		t.Fatalf("expected logger, got nil")
	}

	// Should allow basic logging without panic
	logger.Info("logging initialized for test")
}

// An error log is one line on stderr: no stack trace.
func TestSetupLoggingErrorHasNoStackTrace(t *testing.T) {
	out := filepath.Join(t.TempDir(), "stderr")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stderr
	os.Stderr = f
	t.Cleanup(func() { os.Stderr = prev })

	logger := SetupLogging(&appidentity.Identity{BinaryName: "spanwit"})
	logger.Error("refused for a test")
	_ = logger.Sync()
	_ = f.Close()

	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(b))
	if !strings.Contains(got, "refused for a test") {
		t.Fatalf("error not logged to stderr: %q", got)
	}
	if strings.Count(got, "\n") != 0 || strings.Contains(got, ".go:") {
		t.Fatalf("error log carries a stack trace: %q", got)
	}
}
