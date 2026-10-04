package cmd

import (
	"context"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
)

func TestDoctorCommand_Succeeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

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

	cmd := newDoctorCmd(identity)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor command returned error: %v", err)
	}
}
