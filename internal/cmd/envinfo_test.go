package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
)

func TestEnvinfoCommand_Executes(t *testing.T) {
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

	cmd := newEnvinfoCmd(identity)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("envinfo command returned error: %v", err)
	}
}
