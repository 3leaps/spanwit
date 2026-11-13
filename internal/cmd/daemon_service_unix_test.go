//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/3leaps/spanwit/internal/serviceexec"
	"github.com/3leaps/spanwit/internal/userservice"
	"github.com/fulmenhq/gofulmen/appidentity"
)

type fakeCLIManagerRunner struct {
	platform string
}

func (f fakeCLIManagerRunner) Run(_ context.Context, req serviceexec.Request) serviceexec.Result {
	if f.platform == "darwin" {
		return serviceexec.Result{
			Outcome:   serviceexec.Failed,
			Started:   true,
			Uncertain: true,
			ExitCode:  113,
			Stderr:    []byte("Could not find service in domain\n"),
		}
	}
	return serviceexec.Result{
		Outcome:  serviceexec.Success,
		Started:  true,
		ExitCode: 0,
		Stdout:   []byte("LoadState=not-found\nActiveState=inactive\nSubState=dead\nMainPID=0\nExecMainStatus=0\n"),
	}
}

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

func TestDaemonServiceCapabilitiesAndInstallLifecycle(t *testing.T) {
	resetPolicyForTest(t)
	rawHome := t.TempDir()
	home, err := filepath.EvalSymlinks(rawHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	execPath := filepath.Join(binDir, "testtool")
	if err := os.WriteFile(execPath, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(home, "state")

	root := newDaemonCmd(&appidentity.Identity{BinaryName: "testtool", Vendor: "testvendor"})
	for _, tc := range []struct {
		sub  string
		want string
	}{
		{"preview", CapabilityObservational},
		{"status", CapabilityObservational},
		{"install", CapabilityMutating},
		{"start", CapabilityMutating},
		{"stop", CapabilityMutating},
		{"uninstall", CapabilityMutating},
	} {
		cmd, _, findErr := root.Find([]string{"service", tc.sub})
		if findErr != nil {
			t.Fatalf("find service %s: %v", tc.sub, findErr)
		}
		if got := resolveInvocationCapability(cmd); got != tc.want {
			t.Fatalf("service %s capability=%q want=%q", tc.sub, got, tc.want)
		}
	}

	// Inject a fake service command runner so tests never touch the live host manager.
	prevRunner := serviceCommandRunner
	serviceCommandRunner = fakeCLIManagerRunner{platform: runtime.GOOS}
	t.Cleanup(func() { serviceCommandRunner = prevRunner })

	// Execute status before install and install via CLI.
	var statusOut bytes.Buffer
	root.SetOut(&statusOut)
	root.SetErr(&statusOut)
	root.SetArgs([]string{"--state-dir", stateDir, "service", "status"})
	if err := root.Execute(); err != nil {
		t.Fatalf("service status failed: %v", err)
	}
	var statusReport userservice.Report
	if err := json.Unmarshal(statusOut.Bytes(), &statusReport); err != nil {
		t.Fatalf("decode status report: %v", err)
	}
	if statusReport.Installation.State != "not_installed" {
		t.Fatalf("unexpected pre-install status: %+v", statusReport)
	}

	// Verify read-only assertion allows observational commands (status, preview) and refuses mutating ones (install, start, stop, uninstall).
	t.Setenv("SPANWIT_READ_ONLY", "true")
	for _, sub := range []string{"status", "preview"} {
		leaf, _, findErr := root.Find([]string{"service", sub})
		if findErr != nil {
			t.Fatalf("find service %s: %v", sub, findErr)
		}
		if err := enforceMutationPolicy(leaf); err != nil {
			t.Fatalf("expected read-only assertion to allow service %s: %v", sub, err)
		}
	}
	for _, sub := range []string{"install", "start", "stop", "uninstall"} {
		leaf, _, findErr := root.Find([]string{"service", sub})
		if findErr != nil {
			t.Fatalf("find service %s: %v", sub, findErr)
		}
		if err := enforceMutationPolicy(leaf); err == nil {
			t.Fatalf("expected read-only assertion refusal for service %s", sub)
		}
	}
	t.Setenv("SPANWIT_READ_ONLY", "")

	var installOut bytes.Buffer
	root = newDaemonCmd(&appidentity.Identity{BinaryName: "testtool", Vendor: "testvendor"})
	root.SetOut(&installOut)
	root.SetErr(&installOut)
	root.SetArgs([]string{"--state-dir", stateDir, "service", "install", "--executable", execPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("service install failed: %v", err)
	}
	var installReport userservice.Report
	if err := json.Unmarshal(installOut.Bytes(), &installReport); err != nil {
		t.Fatalf("decode install report: %v", err)
	}
	if installReport.Outcome != "installed" || !installReport.DefinitionWritten || installReport.CommandsExecuted {
		t.Fatalf("unexpected install report: %+v", installReport)
	}

	var uninstallOut bytes.Buffer
	root = newDaemonCmd(&appidentity.Identity{BinaryName: "testtool", Vendor: "testvendor"})
	root.SetOut(&uninstallOut)
	root.SetErr(&uninstallOut)
	root.SetArgs([]string{"--state-dir", stateDir, "service", "uninstall"})
	if err := root.Execute(); err != nil {
		t.Fatalf("service uninstall failed: %v", err)
	}
	var uninstallReport userservice.Report
	if err := json.Unmarshal(uninstallOut.Bytes(), &uninstallReport); err != nil {
		t.Fatalf("decode uninstall report: %v", err)
	}
	if uninstallReport.Outcome != "uninstalled" || !uninstallReport.DefinitionRemoved {
		t.Fatalf("unexpected uninstall report: %+v", uninstallReport)
	}
}
