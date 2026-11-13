//go:build darwin || linux

package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/3leaps/spanwit/internal/userservice"
	"github.com/fulmenhq/gofulmen/appidentity"
)

func TestDaemonServicePreviewDoesNotInstall(t *testing.T) {
	resetPolicyForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "custom-config"))
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			root := newDaemonCmd(&appidentity.Identity{BinaryName: "testtool", Vendor: "testvendor"})
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"--state-dir", filepath.Join(home, "state"), "service", "preview", "--platform", platform, "--executable", "/opt/tools/testtool", "--root", filepath.Join(home, "cache")})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			var plan userservice.Plan
			if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
				t.Fatal(err)
			}
			if plan.DefinitionWritten || plan.CommandsExecuted || plan.Content == "" {
				t.Fatalf("invalid preview: %+v", plan)
			}
			if platform == "linux" && filepath.Dir(plan.DefinitionPath) != filepath.Join(home, "custom-config", "systemd", "user") {
				t.Fatalf("ignored XDG_CONFIG_HOME: %s", plan.DefinitionPath)
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatalf("preview wrote files: %v %v", entries, err)
			}
			command, _, err := root.Find([]string{"service", "preview"})
			if err != nil || resolveInvocationCapability(command) != CapabilityObservational {
				t.Fatalf("preview capability: %v", err)
			}
		})
	}
}

func TestDaemonServicePreviewRequiresExecutable(t *testing.T) {
	root := newDaemonCmd(&appidentity.Identity{BinaryName: "testtool", Vendor: "testvendor"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"service", "preview"})
	if err := root.Execute(); err == nil {
		t.Fatal("accepted unspecified installed executable")
	}
}
