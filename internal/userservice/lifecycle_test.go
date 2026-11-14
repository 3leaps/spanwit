//go:build darwin || linux

package userservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	schema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/daemon"
	"github.com/3leaps/spanwit/internal/serviceexec"
)

type fakeManagerRunner struct {
	mu          sync.Mutex
	platform    string
	loaded      bool
	running     bool
	pid         int
	requests    [][]string
	startDaemon func() error
	stopDaemon  func() error
	override    func(serviceexec.Request) (serviceexec.Result, bool)
}

func (f *fakeManagerRunner) Run(_ context.Context, req serviceexec.Request) serviceexec.Result {
	f.mu.Lock()
	argv := append([]string{req.Program}, req.Args...)
	f.requests = append(f.requests, argv)
	override := f.override
	f.mu.Unlock()
	if override != nil {
		if res, ok := override(req); ok {
			return res
		}
	}
	switch f.platform {
	case PlatformDarwin:
		return f.runLaunchd(req)
	case PlatformLinux:
		return f.runSystemd(req)
	default:
		return serviceexec.Result{Outcome: serviceexec.Failed, Started: true, Uncertain: true, ExitCode: 1}
	}
}

func (f *fakeManagerRunner) runLaunchd(req serviceexec.Request) serviceexec.Result {
	if req.Program != launchctlProgram || len(req.Args) == 0 {
		return serviceexec.Result{Outcome: serviceexec.NotStarted, Err: errors.New("unexpected launchd program")}
	}
	switch req.Args[0] {
	case "print":
		f.mu.Lock()
		loaded, running, pid := f.loaded, f.running, f.pid
		f.mu.Unlock()
		if !loaded {
			return serviceexec.Result{
				Outcome:   serviceexec.Failed,
				Started:   true,
				Uncertain: true,
				ExitCode:  113,
				Stderr:    []byte("Could not find service in domain\n"),
			}
		}
		if running {
			out := fmt.Sprintf("gui/1000/test = {\n\tactive count = 1\n\tpath = /Users/test/Library/LaunchAgents/test.plist\n\ttype = LaunchAgent\n\tstate = running\n\tprogram = /opt/tools/spanwit\n\targuments = {\n\t\t/opt/tools/spanwit\n\t\tdaemon\n\t\trun\n\t}\n\tpid = %d\n\tlast exit code = (never exited)\n\tresource coalition = {\n\t\tID = 42\n\t\ttype = resource\n\t\tstate = active\n\t\tactive count = 1\n\t}\n\tjetsam coalition = {\n\t\tID = 43\n\t\ttype = jetsam\n\t\tstate = active\n\t\tactive count = 1\n\t}\n}\n", pid)
			return serviceexec.Result{Outcome: serviceexec.Success, Started: true, ExitCode: 0, Stdout: []byte(out)}
		}
		out := "gui/1000/test = {\n\tstate = waiting\n\tlast exit code = 0\n\tresource coalition = {\n\t\tstate = active\n\t}\n\tjetsam coalition = {\n\t\tstate = active\n\t}\n}\n"
		return serviceexec.Result{Outcome: serviceexec.Success, Started: true, ExitCode: 0, Stdout: []byte(out)}
	case "bootstrap":
		f.mu.Lock()
		f.loaded = true
		f.mu.Unlock()
		return serviceexec.Result{Outcome: serviceexec.Success, Started: true, ExitCode: 0}
	case "kickstart":
		if f.startDaemon != nil {
			if err := f.startDaemon(); err != nil {
				return serviceexec.Result{Outcome: serviceexec.Failed, Started: true, Uncertain: true, ExitCode: 1, Err: err}
			}
		}
		f.mu.Lock()
		f.loaded = true
		f.running = true
		f.mu.Unlock()
		return serviceexec.Result{Outcome: serviceexec.Success, Started: true, ExitCode: 0}
	case "bootout":
		if f.stopDaemon != nil {
			if err := f.stopDaemon(); err != nil {
				return serviceexec.Result{Outcome: serviceexec.Failed, Started: true, Uncertain: true, ExitCode: 1, Err: err}
			}
		}
		f.mu.Lock()
		f.loaded = false
		f.running = false
		f.pid = 0
		f.mu.Unlock()
		return serviceexec.Result{Outcome: serviceexec.Success, Started: true, ExitCode: 0}
	default:
		return serviceexec.Result{Outcome: serviceexec.Failed, Started: true, Uncertain: true, ExitCode: 1}
	}
}

func (f *fakeManagerRunner) runSystemd(req serviceexec.Request) serviceexec.Result {
	if req.Program != systemctlProgram || len(req.Args) < 2 || req.Args[0] != "--user" {
		return serviceexec.Result{Outcome: serviceexec.NotStarted, Err: errors.New("unexpected systemd program")}
	}
	switch req.Args[1] {
	case "show":
		f.mu.Lock()
		loaded, running, pid := f.loaded, f.running, f.pid
		f.mu.Unlock()
		loadState := "not-found"
		if loaded {
			loadState = "loaded"
		}
		activeState := "inactive"
		subState := "dead"
		mainPID := 0
		if running {
			activeState = "active"
			subState = "running"
			mainPID = pid
		}
		out := fmt.Sprintf("LoadState=%s\nActiveState=%s\nSubState=%s\nMainPID=%d\nExecMainStatus=0\n", loadState, activeState, subState, mainPID)
		return serviceexec.Result{Outcome: serviceexec.Success, Started: true, ExitCode: 0, Stdout: []byte(out)}
	case "daemon-reload":
		return serviceexec.Result{Outcome: serviceexec.Success, Started: true, ExitCode: 0}
	case "start":
		if f.startDaemon != nil {
			if err := f.startDaemon(); err != nil {
				return serviceexec.Result{Outcome: serviceexec.Failed, Started: true, Uncertain: true, ExitCode: 1, Err: err}
			}
		}
		f.mu.Lock()
		f.loaded = true
		f.running = true
		f.mu.Unlock()
		return serviceexec.Result{Outcome: serviceexec.Success, Started: true, ExitCode: 0}
	case "stop":
		if f.stopDaemon != nil {
			if err := f.stopDaemon(); err != nil {
				return serviceexec.Result{Outcome: serviceexec.Failed, Started: true, Uncertain: true, ExitCode: 1, Err: err}
			}
		}
		f.mu.Lock()
		f.running = false
		f.pid = 0
		f.mu.Unlock()
		return serviceexec.Result{Outcome: serviceexec.Success, Started: true, ExitCode: 0}
	default:
		return serviceexec.Result{Outcome: serviceexec.Failed, Started: true, Uncertain: true, ExitCode: 1}
	}
}

func canonicalTestDir(t *testing.T) string {
	t.Helper()
	raw, err := os.MkdirTemp("/tmp", "swus-")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		_ = os.RemoveAll(raw)
		t.Fatal(err)
	}
	if err := os.Chmod(resolved, 0o700); err != nil {
		_ = os.RemoveAll(resolved)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(resolved)
	})
	return resolved
}

func createInstalledExecutableFixture(t *testing.T, baseDir, name, content string) string {
	t.Helper()
	binDir := filepath.Join(baseDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	execPath := filepath.Join(binDir, name)
	if err := os.WriteFile(execPath, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return execPath
}

func validateStatusReportContract(t *testing.T, r Report) {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateJSON(schema.SpanwitUserServiceStatusV0, raw); err != nil {
		t.Fatalf("report failed schema validation: %v\n%s", err, string(raw))
	}
}

func TestManagedLifecycleInstallStartStatusStopUninstallAndUpgrade(t *testing.T) {
	for _, platform := range []string{PlatformDarwin, PlatformLinux} {
		t.Run(platform, func(t *testing.T) {
			rootDir := canonicalTestDir(t)
			home := filepath.Join(rootDir, "h")
			stateDir := filepath.Join(rootDir, "s")
			samplePath := filepath.Join(rootDir, "p")
			for _, d := range []string{home, samplePath} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			// Pre-create standard 0755 service unit directories (e.g. ~/Library/LaunchAgents or ~/.config/systemd/user).
			unitParent := filepath.Join(home, "Library", "LaunchAgents")
			if platform == PlatformLinux {
				unitParent = filepath.Join(home, ".config", "systemd", "user")
			}
			if err := os.MkdirAll(unitParent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(unitParent, 0o755); err != nil {
				t.Fatal(err)
			}
			execPath := createInstalledExecutableFixture(t, rootDir, "spanwit", "#!/bin/sh\n# v1\n")

			var daemonCancel context.CancelFunc
			var daemonDone chan error
			stopRunningDaemon := func() error {
				if daemonCancel != nil {
					daemonCancel()
					err := <-daemonDone
					daemonCancel = nil
					daemonDone = nil
					return err
				}
				return nil
			}
			t.Cleanup(func() { _ = stopRunningDaemon() })

			runner := &fakeManagerRunner{
				platform: platform,
				pid:      os.Getpid(),
			}
			cfg := Config{
				Platform:   platform,
				Executable: execPath,
				Home:       home,
				StateDir:   stateDir,
				Path:       samplePath,
				Interval:   time.Minute,
				BinaryName: "spanwit",
				Vendor:     "3leaps",
				UID:        os.Getuid(),
			}
			runner.startDaemon = func() error {
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() {
					done <- daemon.RunWithConfig(ctx, daemon.RunConfig{
						StateDir:       stateDir,
						Path:           samplePath,
						Interval:       time.Minute,
						ExecutablePath: execPath,
					})
				}()
				daemonCancel = cancel
				daemonDone = done
				runner.mu.Lock()
				runner.pid = os.Getpid()
				runner.mu.Unlock()
				return nil
			}
			runner.stopDaemon = stopRunningDaemon

			opts := Options{
				StateDir:         stateDir,
				Platform:         platform,
				Home:             home,
				BinaryName:       "spanwit",
				Vendor:           "3leaps",
				UID:              os.Getuid(),
				Runner:           runner,
				HealthTimeout:    3 * time.Second,
				PollInterval:     20 * time.Millisecond,
				ProcessGoneProbe: func(int) (bool, error) { return true, nil },
			}

			// 1. Observational status before install must not create stateDir.
			before, err := Status(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			validateStatusReportContract(t, before)
			if before.OverallState != "not_installed" || before.Installation.State != "not_installed" {
				t.Fatalf("unexpected pre-install status: %+v", before)
			}
			if _, statErr := os.Stat(stateDir); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("observational status created stateDir: %v", statErr)
			}
			if os.Geteuid() == 0 {
				if _, err := Install(context.Background(), cfg, opts); err == nil || !strings.Contains(err.Error(), "run as an ordinary user") {
					t.Fatalf("expected root install refusal, got %v", err)
				}
				return
			}

			// 2. Install writes definition + receipt without starting the service.
			installed, err := Install(context.Background(), cfg, opts)
			if err != nil {
				t.Fatal(err)
			}
			validateStatusReportContract(t, installed)
			if installed.Outcome != "installed" || !installed.DefinitionWritten || installed.CommandsExecuted || installed.OverallState != "installed_stopped" {
				t.Fatalf("unexpected install report: %+v", installed)
			}

			// 3. Start invokes fixed manager verbs and confirms bound instance health.
			started, err := Start(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			validateStatusReportContract(t, started)
			if started.Outcome != "started" || !started.Healthy || started.OverallState != "healthy" || !started.CommandsExecuted {
				t.Fatalf("unexpected start report: %+v", started)
			}
			if !started.Daemon.PIDMatchesManager || !started.Daemon.ConfigMatchesReceipt || !started.Daemon.ExecutableMatchesReceipt || !started.Daemon.ExecutableMatchesDisk {
				t.Fatalf("daemon binding flags not set: %+v", started.Daemon)
			}

			// 4. Staged executable replacement while running is reported as drifted.
			tmpExec := execPath + ".staged"
			if err := os.WriteFile(tmpExec, []byte("#!/bin/sh\n# v2-upgraded\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(tmpExec, execPath); err != nil {
				t.Fatal(err)
			}
			drifted, err := Status(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			validateStatusReportContract(t, drifted)
			if drifted.Healthy || drifted.OverallState != "drifted" || drifted.Installation.State != "executable_changed" {
				t.Fatalf("expected executable drift while running: %+v", drifted)
			}
			if !drifted.Daemon.ExecutableMatchesReceipt || drifted.Daemon.ExecutableMatchesDisk {
				t.Fatalf("expected running daemon to match receipt but not disk: %+v", drifted.Daemon)
			}

			// 4b. Install refuses while service is running.
			refusedLiveInstall, liveInstallErr := Install(context.Background(), cfg, opts)
			if liveInstallErr == nil || refusedLiveInstall.Outcome != "refused" || refusedLiveInstall.DefinitionWritten {
				t.Fatalf("expected install refusal while service is running: %+v err=%v", refusedLiveInstall, liveInstallErr)
			}
			validateStatusReportContract(t, refusedLiveInstall)

			// 5. Stop terminates the running daemon and preserves journal state.
			stopped, err := Stop(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			validateStatusReportContract(t, stopped)
			if stopped.Outcome != "stopped" || !stopped.CommandsExecuted || stopped.Daemon.SocketPresent {
				t.Fatalf("unexpected stop report: %+v", stopped)
			}
			if _, statErr := os.Stat(filepath.Join(stateDir, "journal")); statErr != nil {
				t.Fatalf("stop removed journal: %v", statErr)
			}

			// 6. Start refuses while executable on disk differs from receipt until Install updates receipt.
			refusedStart, startErr := Start(context.Background(), opts)
			if startErr == nil || refusedStart.Outcome != "refused" || refusedStart.CommandsExecuted {
				t.Fatalf("expected start refusal on changed executable: %+v err=%v", refusedStart, startErr)
			}
			validateStatusReportContract(t, refusedStart)

			// 7. Re-running Install updates receipt for v2 executable, then Start succeeds.
			reinstalled, err := Install(context.Background(), cfg, opts)
			if err != nil {
				t.Fatal(err)
			}
			validateStatusReportContract(t, reinstalled)
			restarted, err := Start(context.Background(), opts)
			if err != nil || !restarted.Healthy {
				t.Fatalf("restart after upgrade failed: %+v err=%v", restarted, err)
			}
			validateStatusReportContract(t, restarted)

			// 8. Uninstall refuses while service is running, succeeds after Stop, and keeps journal.
			refusedUninstall, uninstErr := Uninstall(context.Background(), opts)
			if uninstErr == nil || refusedUninstall.Outcome != "refused" {
				t.Fatalf("expected uninstall refusal while running: %+v err=%v", refusedUninstall, uninstErr)
			}
			validateStatusReportContract(t, refusedUninstall)

			if _, err := Stop(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			uninstalled, err := Uninstall(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			validateStatusReportContract(t, uninstalled)
			if uninstalled.Outcome != "uninstalled" || !uninstalled.DefinitionRemoved || uninstalled.Installation.State != "not_installed" {
				t.Fatalf("unexpected uninstall report: %+v", uninstalled)
			}
			if _, statErr := os.Stat(filepath.Join(stateDir, "journal")); statErr != nil {
				t.Fatalf("uninstall removed journal: %v", statErr)
			}
		})
	}
}

func TestCrashConsistencyAndForeignDefinitionProtection(t *testing.T) {
	rootDir := canonicalTestDir(t)
	home := filepath.Join(rootDir, "h")
	stateDir := filepath.Join(rootDir, "s")
	samplePath := filepath.Join(rootDir, "p")
	for _, d := range []string{home, samplePath} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	execPath := createInstalledExecutableFixture(t, rootDir, "spanwit", "#!/bin/sh\n")
	runner := &fakeManagerRunner{platform: PlatformDarwin}
	cfg := Config{
		Platform:   PlatformDarwin,
		Executable: execPath,
		Home:       home,
		StateDir:   stateDir,
		Path:       samplePath,
		Interval:   time.Minute,
		BinaryName: "spanwit",
		Vendor:     "3leaps",
		UID:        os.Getuid(),
	}
	opts := Options{
		StateDir:   stateDir,
		Platform:   PlatformDarwin,
		Home:       home,
		BinaryName: "spanwit",
		Vendor:     "3leaps",
		UID:        os.Getuid(),
		Runner:     runner,
	}

	// 1. Pre-existing foreign definition without receipt is refused by Install and Uninstall.
	plan, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(plan.DefinitionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	foreignBytes := []byte("foreign plist content\n")
	if err := os.WriteFile(plan.DefinitionPath, foreignBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		foreignStatus, err := Status(context.Background(), opts)
		if err != nil || foreignStatus.Installation.State != "foreign_definition" {
			t.Fatalf("expected foreign_definition status under root: %+v err=%v", foreignStatus, err)
		}
		validateStatusReportContract(t, foreignStatus)
		if _, err := Install(context.Background(), cfg, opts); err == nil || !strings.Contains(err.Error(), "run as an ordinary user") {
			t.Fatalf("expected root install refusal, got %v", err)
		}
		if _, err := Uninstall(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "run as an ordinary user") {
			t.Fatalf("expected root uninstall refusal, got %v", err)
		}
		return
	}
	refusedInstall, err := Install(context.Background(), cfg, opts)
	if err == nil || refusedInstall.Outcome != "refused" || refusedInstall.Installation.State != "foreign_definition" {
		t.Fatalf("expected foreign_definition refusal on install: %+v err=%v", refusedInstall, err)
	}
	validateStatusReportContract(t, refusedInstall)
	refusedUninstall, err := Uninstall(context.Background(), opts)
	if err == nil || refusedUninstall.Outcome != "refused" {
		t.Fatalf("expected foreign_definition refusal on uninstall: %+v err=%v", refusedUninstall, err)
	}
	validateStatusReportContract(t, refusedUninstall)
	gotForeign, err := os.ReadFile(plan.DefinitionPath)
	if err != nil || string(gotForeign) != string(foreignBytes) {
		t.Fatalf("foreign definition modified: %v %q", err, string(gotForeign))
	}
	if err := os.Remove(plan.DefinitionPath); err != nil {
		t.Fatal(err)
	}

	// 2. Interrupted install after writing definition before final receipt commit fails closed on Start and reconciles on retry.
	interruptedOpts := opts
	interruptedOpts.AfterWriteDef = func() error { return errors.New("simulated crash after definition write") }
	failedInstall, err := Install(context.Background(), cfg, interruptedOpts)
	if err == nil || failedInstall.Installation.State != "interrupted_install" || failedInstall.OverallState != "interrupted" {
		t.Fatalf("expected interrupted_install state: %+v err=%v", failedInstall, err)
	}
	validateStatusReportContract(t, failedInstall)
	refusedStart, err := Start(context.Background(), opts)
	if err == nil || refusedStart.Outcome != "refused" {
		t.Fatalf("expected start refusal on interrupted_install: %+v err=%v", refusedStart, err)
	}
	validateStatusReportContract(t, refusedStart)

	reconciled, err := Install(context.Background(), cfg, opts)
	if err != nil || reconciled.Installation.State != "installed" {
		t.Fatalf("reconcile install failed: %+v err=%v", reconciled, err)
	}
	validateStatusReportContract(t, reconciled)

	// 3. External edit of owned definition is detected as definition_drifted and never overwritten or deleted.
	if err := os.WriteFile(plan.DefinitionPath, []byte("drifted content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	driftedStatus, err := Status(context.Background(), opts)
	if err != nil || driftedStatus.Installation.State != "definition_drifted" || driftedStatus.OverallState != "drifted" {
		t.Fatalf("expected definition_drifted status: %+v err=%v", driftedStatus, err)
	}
	validateStatusReportContract(t, driftedStatus)
	if _, err := Install(context.Background(), cfg, opts); err == nil {
		t.Fatal("overwrote drifted definition")
	}
	if _, err := Uninstall(context.Background(), opts); err == nil {
		t.Fatal("deleted drifted definition")
	}

	// 4. Tampered receipt with foreign DefinitionPath or non-hex digest is rejected by validateReceipt.
	receiptFile := filepath.Join(stateDir, "service-receipt.json")
	rawReceipt, err := os.ReadFile(receiptFile)
	if err != nil {
		t.Fatal(err)
	}
	var tampered map[string]any
	if err := json.Unmarshal(rawReceipt, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered["definition_path"] = filepath.Join(home, "other.plist")
	tamperedRaw, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptFile, tamperedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	invalidStatus, err := Status(context.Background(), opts)
	if err != nil || invalidStatus.Installation.State != "invalid_receipt" {
		t.Fatalf("expected invalid_receipt on tampered definition_path: %+v err=%v", invalidStatus, err)
	}
	validateStatusReportContract(t, invalidStatus)
}

func TestUnmanagedProcessConflictAndUncertainOutcomes(t *testing.T) {
	rootDir := canonicalTestDir(t)
	home := filepath.Join(rootDir, "h")
	stateDir := filepath.Join(rootDir, "s")
	samplePath := filepath.Join(rootDir, "p")
	for _, d := range []string{home, samplePath} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	execPath := createInstalledExecutableFixture(t, rootDir, "spanwit", "#!/bin/sh\n")
	runner := &fakeManagerRunner{platform: PlatformLinux}
	cfg := Config{
		Platform:   PlatformLinux,
		Executable: execPath,
		Home:       home,
		StateDir:   stateDir,
		Path:       samplePath,
		Interval:   time.Minute,
		BinaryName: "spanwit",
		Vendor:     "3leaps",
		UID:        os.Getuid(),
	}
	opts := Options{
		StateDir:      stateDir,
		Platform:      PlatformLinux,
		Home:          home,
		BinaryName:    "spanwit",
		Vendor:        "3leaps",
		UID:           os.Getuid(),
		Runner:        runner,
		HealthTimeout: 300 * time.Millisecond,
		PollInterval:  20 * time.Millisecond,
	}
	if os.Geteuid() == 0 {
		if _, err := Start(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "run as an ordinary user") {
			t.Fatalf("expected root start refusal, got %v", err)
		}
		if _, err := Stop(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "run as an ordinary user") {
			t.Fatalf("expected root stop refusal, got %v", err)
		}
		return
	}
	if _, err := Install(context.Background(), cfg, opts); err != nil {
		t.Fatal(err)
	}

	// Start an unmanaged foreground daemon on the same stateDir while manager reports not_loaded.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- daemon.RunWithConfig(ctx, daemon.RunConfig{
			StateDir:       stateDir,
			Path:           samplePath,
			Interval:       time.Minute,
			ExecutablePath: execPath,
		})
	}()
	var stopOnce sync.Once
	stopUnmanaged := func() {
		stopOnce.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}
	defer stopUnmanaged()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if present, _ := socketExistsReadOnly(stateDir); present {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	conflictStatus, err := Status(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	validateStatusReportContract(t, conflictStatus)
	if conflictStatus.Healthy || conflictStatus.OverallState != "conflict" || conflictStatus.Daemon.PIDMatchesManager {
		t.Fatalf("expected unmanaged daemon conflict: %+v", conflictStatus)
	}
	if _, err := Start(context.Background(), opts); err == nil {
		t.Fatal("started over unmanaged socket holder")
	}
	if _, err := Stop(context.Background(), opts); err == nil {
		t.Fatal("stopped unmanaged socket holder")
	}
	stopUnmanaged()

	// Generic nonzero exit from manager show/print must classify as unknown, never stopped/not_loaded.
	runner.override = func(req serviceexec.Request) (serviceexec.Result, bool) {
		if len(req.Args) >= 2 && req.Args[1] == "show" {
			return serviceexec.Result{
				Outcome:   serviceexec.Failed,
				Started:   true,
				Uncertain: true,
				ExitCode:  1,
				Stderr:    []byte("Failed to connect to bus: No medium found\n"),
			}, true
		}
		return serviceexec.Result{}, false
	}
	unknownStatus, err := Status(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	validateStatusReportContract(t, unknownStatus)
	if unknownStatus.Manager.State != "unknown" || !unknownStatus.Uncertain || unknownStatus.OverallState != "unknown" {
		t.Fatalf("generic nonzero show was not classified as unknown: %+v", unknownStatus)
	}
	if _, err := Start(context.Background(), opts); err == nil {
		t.Fatal("started when manager state was unknown")
	}

	// Stop when socket disappears but verified PID is still alive must report unconfirmed.
	runner.override = nil
	runner.loaded = true
	runner.running = true
	runner.pid = os.Getpid()
	runner.stopDaemon = func() error { return nil }
	aliveOpts := opts
	aliveOpts.HealthTimeout = 100 * time.Millisecond
	aliveOpts.PollInterval = 20 * time.Millisecond
	aliveOpts.ProcessGoneProbe = func(int) (bool, error) { return false, nil }
	unconfirmedStop, stopErr := Stop(context.Background(), aliveOpts)
	if stopErr == nil || unconfirmedStop.Outcome != "unconfirmed" || !unconfirmedStop.Uncertain {
		t.Fatalf("expected unconfirmed stop when PID remains alive: %+v err=%v", unconfirmedStop, stopErr)
	}
	validateStatusReportContract(t, unconfirmedStop)
	if !strings.Contains(unconfirmedStop.Error, "pid_gone=false") {
		t.Fatalf("expected pid_gone=false in unconfirmed error: %s", unconfirmedStop.Error)
	}
}
