package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
)

// A command that fails after its flags and arguments parse prints no usage
// and no "Error:" line (main prints the error once). Misuse still shows usage.
func TestRootErrorOutput(t *testing.T) {
	identity := &appidentity.Identity{BinaryName: "spanwit", EnvPrefix: "SPANWIT_", ConfigName: "spanwit"}
	run := func(args ...string) (string, error) {
		logger, err := logging.NewCLI(identity.BinaryName)
		if err != nil {
			t.Fatal(err)
		}
		Initialize(context.Background(), identity, logger)
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetArgs(args)
		err = rootCmd.Execute()
		return out.String(), err
	}

	missing := filepath.Join(t.TempDir(), "absent.schema.json")
	out, err := run("validate", "--schema", missing, "--meta-only")
	if err == nil {
		t.Fatal("validate of a missing schema succeeded")
	}
	if strings.Contains(out, "Usage:") || strings.Contains(out, "Error:") {
		t.Fatalf("runtime failure printed usage or an Error line:\n%s", out)
	}

	out, err = run("validate", "--no-such-flag")
	if err == nil || !strings.Contains(out, "Usage:") {
		t.Fatalf("misuse should show usage: err=%v\n%s", err, out)
	}
}
