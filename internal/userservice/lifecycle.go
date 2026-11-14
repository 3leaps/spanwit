//go:build darwin || linux

package userservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/3leaps/spanwit/internal/daemon"
	"github.com/3leaps/spanwit/internal/serviceexec"
)

const (
	StatusSchema         = "https://schemas.3leaps.dev/spanwit/user-service-status/v0.json"
	defaultHealthTimeout = 5 * time.Second
	defaultPollInterval  = 50 * time.Millisecond
)

// Options configures lifecycle observation and execution boundaries without
// exposing arbitrary shell or command overrides.
type Options struct {
	StateDir          string
	Platform          string
	Home              string
	ConfigHome        string
	BinaryName        string
	Vendor            string
	UID               int
	Runner            serviceexec.Runner
	HealthTimeout     time.Duration
	PollInterval      time.Duration
	ProcessGoneProbe  func(int) (bool, error)
	AfterWritePending func() error
	AfterWriteDef     func() error
}

type Report struct {
	Schema            string              `json:"$schema"`
	Version           int                 `json:"version"`
	Action            string              `json:"action"`
	Outcome           string              `json:"outcome"`
	Uncertain         bool                `json:"uncertain"`
	Healthy           bool                `json:"healthy"`
	OverallState      string              `json:"overall_state"`
	DefinitionWritten bool                `json:"definition_written"`
	DefinitionRemoved bool                `json:"definition_removed"`
	CommandsExecuted  bool                `json:"commands_executed"`
	Installation      InstallationSummary `json:"installation"`
	Manager           ManagerSummary      `json:"manager"`
	Daemon            DaemonSummary       `json:"daemon"`
	Commands          []CommandSummary    `json:"commands,omitempty"`
	Error             string              `json:"error,omitempty"`
}

type InstallationSummary struct {
	State                   string   `json:"state"`
	Manager                 string   `json:"manager,omitempty"`
	Label                   string   `json:"label,omitempty"`
	StateDir                string   `json:"state_dir"`
	DefinitionPath          string   `json:"definition_path,omitempty"`
	ExecutablePath          string   `json:"executable_path,omitempty"`
	SamplePath              string   `json:"sample_path,omitempty"`
	Interval                string   `json:"interval,omitempty"`
	Roots                   []string `json:"roots,omitempty"`
	ConfigSHA256            string   `json:"config_sha256,omitempty"`
	DefinitionSHA256        string   `json:"definition_sha256,omitempty"`
	ExecutableSHA256        string   `json:"executable_sha256,omitempty"`
	CurrentExecutableSHA256 string   `json:"current_executable_sha256,omitempty"`
	OnDiskDefinitionMatches bool     `json:"on_disk_definition_matches"`
	OnDiskExecutableMatches bool     `json:"on_disk_executable_matches"`
	Detail                  string   `json:"detail,omitempty"`
}

type ManagerSummary struct {
	Manager  string `json:"manager,omitempty"`
	Label    string `json:"label,omitempty"`
	State    string `json:"state"`
	PID      int    `json:"pid,omitempty"`
	SubState string `json:"sub_state,omitempty"`
	ExitCode *int   `json:"last_exit_code,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type DaemonSummary struct {
	SocketPresent            bool   `json:"socket_present"`
	Reachable                bool   `json:"reachable"`
	State                    string `json:"state"`
	InstanceID               string `json:"instance_id,omitempty"`
	PID                      int    `json:"pid,omitempty"`
	StartedAt                string `json:"started_at,omitempty"`
	ExecutablePath           string `json:"executable_path,omitempty"`
	ExecutableSHA256         string `json:"executable_sha256,omitempty"`
	ConfigSHA256             string `json:"config_sha256,omitempty"`
	PIDMatchesManager        bool   `json:"pid_matches_manager"`
	ConfigMatchesReceipt     bool   `json:"config_matches_receipt"`
	ExecutableMatchesReceipt bool   `json:"executable_matches_receipt"`
	ExecutableMatchesDisk    bool   `json:"executable_matches_disk"`
	Detail                   string `json:"detail,omitempty"`
}

type CommandSummary struct {
	Argv      []string            `json:"argv"`
	Outcome   serviceexec.Outcome `json:"outcome"`
	Started   bool                `json:"started"`
	Uncertain bool                `json:"uncertain"`
	ExitCode  int                 `json:"exit_code"`
	Detail    string              `json:"detail,omitempty"`
}

// Status observes definition/receipt integrity, service-manager state, and
// daemon socket health without creating directories or acquiring any lock.
func Status(ctx context.Context, opts Options) (Report, error) {
	if err := validatePath("state directory", opts.StateDir, maxPathBytes); err != nil {
		return Report{}, err
	}
	report, _ := observeState(ctx, opts)
	report.Action = "status"
	report.Outcome = "observed"
	return finalizeReport(report)
}

// Install validates the executable and configuration, refuses foreign or
// drifted definitions, and publishes the definition and receipt in two phases
// under a dedicated service-control lock without starting the service.
func Install(ctx context.Context, config Config, opts Options) (Report, error) {
	if os.Geteuid() == 0 || config.UID == 0 {
		return Report{}, fmt.Errorf("run as an ordinary user")
	}
	if config.UID != os.Getuid() {
		return Report{}, fmt.Errorf("user-service UID must equal current UID")
	}
	opts.StateDir = config.StateDir
	opts.Platform = config.Platform
	opts.Home = config.Home
	opts.ConfigHome = config.ConfigHome
	opts.BinaryName = config.BinaryName
	opts.Vendor = config.Vendor
	opts.UID = config.UID
	plan, err := Render(config)
	if err != nil {
		return Report{}, err
	}
	execID, err := daemon.InspectExecutable(config.Executable)
	if err != nil {
		report, _ := observeState(ctx, opts)
		return failReport(report, "install", "refused", false, fmt.Errorf("invalid installed executable: %w", err))
	}
	configDigest, err := daemon.RuntimeConfigDigest(config.StateDir, config.Path, config.Interval, config.Roots)
	if err != nil {
		return Report{}, err
	}
	lock, err := acquireServiceControlLock(config.StateDir)
	if err != nil {
		report, _ := observeState(ctx, opts)
		return failReport(report, "install", "refused", false, fmt.Errorf("acquire service-control lock: %w", err))
	}
	defer func() { _ = lock.Close() }()

	priorReceipt, hasReceipt, receiptErr := readReceiptReadOnly(config.StateDir)
	if receiptErr != nil {
		report, _ := observeState(ctx, opts)
		return failReport(report, "install", "refused", false, fmt.Errorf("existing service receipt invalid: %w", receiptErr))
	}
	preReport, _ := observeState(ctx, opts)
	if preReport.Manager.State == "unknown" {
		return failReport(preReport, "install", "refused", true, fmt.Errorf("service manager state is unknown (%s); refusing to modify installation", preReport.Manager.Detail))
	}
	if preReport.Manager.State == "running" {
		return failReport(preReport, "install", "refused", false, fmt.Errorf("service is currently running (pid=%d); stop service before installing or upgrading", preReport.Manager.PID))
	}
	if preReport.Daemon.SocketPresent {
		return failReport(preReport, "install", "refused", false, fmt.Errorf("control socket is present in %s; stop running daemon before installing", config.StateDir))
	}
	defID, hasDef, defErr := inspectDefinitionFile(plan.DefinitionPath)
	if defErr != nil {
		report, _ := observeState(ctx, opts)
		return failReport(report, "install", "refused", false, fmt.Errorf("existing service definition unsafe: %w", defErr))
	}
	var prevDefSHA string
	if hasDef {
		if !hasReceipt {
			report, _ := observeState(ctx, opts)
			report.Installation.State = "foreign_definition"
			report.Installation.DefinitionPath = plan.DefinitionPath
			report.OverallState = "conflict"
			return failReport(report, "install", "refused", false, fmt.Errorf("foreign service definition exists at %s without ownership receipt", plan.DefinitionPath))
		}
		if priorReceipt.DefinitionPath != plan.DefinitionPath {
			report, _ := observeState(ctx, opts)
			return failReport(report, "install", "refused", false, fmt.Errorf("existing receipt binds %s, refusing to overwrite %s", priorReceipt.DefinitionPath, plan.DefinitionPath))
		}
		if !receiptOwnsDefinition(*priorReceipt, defID.SHA256) {
			report, _ := observeState(ctx, opts)
			return failReport(report, "install", "refused", false, fmt.Errorf("existing service definition at %s was modified outside spanwit", plan.DefinitionPath))
		}
		prevDefSHA = defID.SHA256
	} else if hasReceipt && priorReceipt.DefinitionPath != plan.DefinitionPath {
		if _, oldExists, oldErr := inspectDefinitionFile(priorReceipt.DefinitionPath); oldErr != nil || oldExists {
			report, _ := observeState(ctx, opts)
			return failReport(report, "install", "refused", false, fmt.Errorf("uninstall previous definition %s before changing definition path", priorReceipt.DefinitionPath))
		}
	}

	contentBytes := []byte(plan.Content)
	contentSum := sha256.Sum256(contentBytes)
	pendingDefSHA := hex.EncodeToString(contentSum[:])
	cleanedRoots := make([]string, len(config.Roots))
	copy(cleanedRoots, config.Roots)

	pendingReceipt := serviceReceipt{
		State:                    "installing",
		Platform:                 config.Platform,
		Manager:                  plan.Manager,
		Label:                    plan.Label,
		UID:                      config.UID,
		Home:                     config.Home,
		ConfigHome:               config.ConfigHome,
		StateDir:                 config.StateDir,
		SamplePath:               config.Path,
		Interval:                 config.Interval.String(),
		Roots:                    cleanedRoots,
		BinaryName:               config.BinaryName,
		Vendor:                   config.Vendor,
		DefinitionPath:           plan.DefinitionPath,
		PendingDefinitionSHA256:  pendingDefSHA,
		PreviousDefinitionSHA256: prevDefSHA,
		ExecutablePath:           execID.Path,
		ExecutableSHA256:         execID.SHA256,
		ExecutableSize:           execID.Size,
		ExecutableDev:            execID.Dev,
		ExecutableIno:            execID.Ino,
		ConfigSHA256:             configDigest,
	}
	if err := writeReceiptAtomic(config.StateDir, pendingReceipt); err != nil {
		report, _ := observeState(ctx, opts)
		return failReport(report, "install", "failed", false, fmt.Errorf("write pending service receipt: %w", err))
	}
	if opts.AfterWritePending != nil {
		if err := opts.AfterWritePending(); err != nil {
			report, _ := observeState(ctx, opts)
			return failReport(report, "install", "failed", false, fmt.Errorf("interrupted after pending receipt: %w", err))
		}
	}
	writtenDef, err := writeDefinitionAtomic(plan.DefinitionPath, contentBytes)
	if err != nil {
		report, _ := observeState(ctx, opts)
		return failReport(report, "install", "failed", false, fmt.Errorf("write service definition: %w", err))
	}
	if opts.AfterWriteDef != nil {
		if err := opts.AfterWriteDef(); err != nil {
			report, _ := observeState(ctx, opts)
			report.DefinitionWritten = true
			return failReport(report, "install", "failed", false, fmt.Errorf("interrupted after writing definition: %w", err))
		}
	}
	installedReceipt := pendingReceipt
	installedReceipt.State = "installed"
	installedReceipt.DefinitionSHA256 = writtenDef.SHA256
	installedReceipt.DefinitionDev = writtenDef.Dev
	installedReceipt.DefinitionIno = writtenDef.Ino
	installedReceipt.PendingDefinitionSHA256 = ""
	installedReceipt.PreviousDefinitionSHA256 = ""
	if err := writeReceiptAtomic(config.StateDir, installedReceipt); err != nil {
		report, _ := observeState(ctx, opts)
		report.DefinitionWritten = true
		return failReport(report, "install", "failed", false, fmt.Errorf("commit installed service receipt: %w", err))
	}
	report, _ := observeState(ctx, opts)
	report.Action = "install"
	report.Outcome = "installed"
	report.DefinitionWritten = true
	return finalizeReport(report)
}

// Start verifies the owned installation and executable, invokes fixed manager
// start verbs without blind retries, and confirms the running daemon instance
// matches the manager PID and installed receipt.
func Start(ctx context.Context, opts Options) (Report, error) {
	if os.Geteuid() == 0 {
		return Report{}, fmt.Errorf("run as an ordinary user")
	}
	if err := validatePath("state directory", opts.StateDir, maxPathBytes); err != nil {
		return Report{}, err
	}
	lock, err := acquireServiceControlLock(opts.StateDir)
	if err != nil {
		report, _ := observeState(ctx, opts)
		return failReport(report, "start", "refused", false, fmt.Errorf("acquire service-control lock: %w", err))
	}
	defer func() { _ = lock.Close() }()

	report, receipt := observeState(ctx, opts)
	if receipt == nil || report.Installation.State != "installed" || !report.Installation.OnDiskDefinitionMatches || !report.Installation.OnDiskExecutableMatches {
		return failReport(report, "start", "refused", false, fmt.Errorf("service installation is %s; require verified installed definition and executable before start", report.Installation.State))
	}
	if report.Manager.State == "unknown" {
		return failReport(report, "start", "refused", true, fmt.Errorf("service manager state is unknown (%s); refusing to start blindly", report.Manager.Detail))
	}
	if report.Manager.State == "running" {
		if report.Healthy {
			report.Action = "start"
			report.Outcome = "already_running"
			return finalizeReport(report)
		}
		return failReport(report, "start", "refused", report.Uncertain, fmt.Errorf("service manager already reports running (pid=%d) but instance health is %s", report.Manager.PID, report.OverallState))
	}
	if report.Daemon.SocketPresent {
		return failReport(report, "start", "refused", false, fmt.Errorf("control socket already exists while service manager reports %s; inspect unmanaged process or stale socket before starting", report.Manager.State))
	}

	steps, err := startStepRequests(*receipt, report.Manager.State)
	if err != nil {
		return failReport(report, "start", "refused", false, err)
	}
	runner := opts.Runner
	if runner == nil {
		runner = serviceexec.ExecRunner{}
	}
	var cmdReceipts []CommandSummary
	executedAny := false
	for _, req := range steps {
		res := runner.Run(ctx, req)
		if res.Started {
			executedAny = true
		}
		cmdReceipts = append(cmdReceipts, summarizeCommand(req, res))
		if res.Outcome != serviceexec.Success || res.Uncertain {
			after, _ := observeState(ctx, opts)
			after.CommandsExecuted = executedAny
			after.Commands = cmdReceipts
			outcome := "failed"
			if res.Started || res.Uncertain {
				outcome = "unconfirmed"
			}
			return failReport(after, "start", outcome, res.Started || res.Uncertain || after.Uncertain, fmt.Errorf("start command %v did not succeed cleanly: %s", req.Args, summarizeCommand(req, res).Detail))
		}
	}

	timeout := opts.HealthTimeout
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	deadline := time.Now().Add(timeout)
	for {
		after, _ := observeState(ctx, opts)
		after.CommandsExecuted = executedAny
		after.Commands = cmdReceipts
		if after.Healthy {
			after.Action = "start"
			after.Outcome = "started"
			return finalizeReport(after)
		}
		if after.OverallState == "conflict" || after.OverallState == "drifted" {
			return failReport(after, "start", "failed", after.Uncertain, fmt.Errorf("started service failed instance/config verification (%s)", after.OverallState))
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return failReport(after, "start", "unconfirmed", true, fmt.Errorf("service did not reach verified healthy state within %s (overall=%s manager=%s daemon=%s)", timeout, after.OverallState, after.Manager.State, after.Daemon.State))
		}
		time.Sleep(poll)
	}
}

// Stop stops the owned service and requires independent confirmation that the
// manager state is inactive, the socket is absent, and any verified daemon PID
// has exited. It never removes the socket or signals a PID directly.
func Stop(ctx context.Context, opts Options) (Report, error) {
	if os.Geteuid() == 0 {
		return Report{}, fmt.Errorf("run as an ordinary user")
	}
	if err := validatePath("state directory", opts.StateDir, maxPathBytes); err != nil {
		return Report{}, err
	}
	lock, err := acquireServiceControlLock(opts.StateDir)
	if err != nil {
		report, _ := observeState(ctx, opts)
		return failReport(report, "stop", "refused", false, fmt.Errorf("acquire service-control lock: %w", err))
	}
	defer func() { _ = lock.Close() }()

	report, receipt := observeState(ctx, opts)
	if receipt == nil || (receipt.State != "installed" && receipt.State != "uninstalling" && receipt.State != "installing") {
		return failReport(report, "stop", "refused", false, fmt.Errorf("no owned installed service receipt in %s (state=%s)", opts.StateDir, report.Installation.State))
	}
	if report.Manager.State == "unknown" {
		return failReport(report, "stop", "refused", true, fmt.Errorf("service manager state is unknown (%s); refusing to stop blindly", report.Manager.Detail))
	}
	if report.Manager.State == "not_loaded" || (receipt.Platform == PlatformLinux && report.Manager.State == "stopped") {
		if !report.Daemon.SocketPresent {
			report.Action = "stop"
			report.Outcome = "already_stopped"
			return finalizeReport(report)
		}
		return failReport(report, "stop", "refused", false, fmt.Errorf("service manager reports %s but control socket is still present; refusing to remove unverified socket", report.Manager.State))
	}
	if report.Manager.State == "running" && report.Daemon.Reachable && !report.Daemon.PIDMatchesManager {
		return failReport(report, "stop", "refused", false, fmt.Errorf("control socket PID (%d) does not match manager PID (%d)", report.Daemon.PID, report.Manager.PID))
	}
	if report.Manager.State != "running" && report.Daemon.SocketPresent {
		return failReport(report, "stop", "refused", false, fmt.Errorf("service manager reports %s while control socket is present; refusing to stop unverified socket holder", report.Manager.State))
	}

	verifiedPID := 0
	if report.Manager.State == "running" && report.Manager.PID > 0 {
		verifiedPID = report.Manager.PID
	}

	req, err := stopStepRequest(*receipt)
	if err != nil {
		return failReport(report, "stop", "refused", false, err)
	}
	runner := opts.Runner
	if runner == nil {
		runner = serviceexec.ExecRunner{}
	}
	res := runner.Run(ctx, req)
	cmdReceipts := []CommandSummary{summarizeCommand(req, res)}
	executed := res.Started
	if res.Outcome != serviceexec.Success || res.Uncertain {
		after, _ := observeState(ctx, opts)
		after.CommandsExecuted = executed
		after.Commands = cmdReceipts
		outcome := "failed"
		if res.Started || res.Uncertain {
			outcome = "unconfirmed"
		}
		return failReport(after, "stop", outcome, res.Started || res.Uncertain || after.Uncertain, fmt.Errorf("stop command %v did not succeed cleanly: %s", req.Args, cmdReceipts[0].Detail))
	}

	probeGone := opts.ProcessGoneProbe
	if probeGone == nil {
		probeGone = defaultProcessGoneProbe
	}
	timeout := opts.HealthTimeout
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	deadline := time.Now().Add(timeout)
	for {
		after, _ := observeState(ctx, opts)
		after.CommandsExecuted = executed
		after.Commands = cmdReceipts
		managerStopped := after.Manager.State == "not_loaded"
		if receipt.Platform == PlatformLinux {
			managerStopped = after.Manager.State == "not_loaded" || ((after.Manager.State == "stopped" || after.Manager.State == "failed") && after.Manager.PID == 0)
		}
		pidGone := true
		var probeErr error
		if verifiedPID > 0 {
			pidGone, probeErr = probeGone(verifiedPID)
		}
		if managerStopped && !after.Daemon.SocketPresent && pidGone && probeErr == nil {
			after.Action = "stop"
			after.Outcome = "stopped"
			return finalizeReport(after)
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return failReport(after, "stop", "unconfirmed", true, fmt.Errorf("stop termination unconfirmed within %s (manager=%s socket_present=%v pid_gone=%v probe_err=%v)", timeout, after.Manager.State, after.Daemon.SocketPresent, pidGone, probeErr))
		}
		time.Sleep(poll)
	}
}

// Uninstall removes the owned service definition and receipt only after
// confirming the service is stopped and the socket is absent. Journal and
// coordinator state are preserved.
func Uninstall(ctx context.Context, opts Options) (Report, error) {
	if os.Geteuid() == 0 {
		return Report{}, fmt.Errorf("run as an ordinary user")
	}
	if err := validatePath("state directory", opts.StateDir, maxPathBytes); err != nil {
		return Report{}, err
	}
	lock, err := acquireServiceControlLock(opts.StateDir)
	if err != nil {
		report, _ := observeState(ctx, opts)
		return failReport(report, "uninstall", "refused", false, fmt.Errorf("acquire service-control lock: %w", err))
	}
	defer func() { _ = lock.Close() }()

	report, receipt := observeState(ctx, opts)
	if receipt == nil {
		if report.Installation.State == "foreign_definition" {
			return failReport(report, "uninstall", "refused", false, fmt.Errorf("refusing to remove foreign definition at %s without ownership receipt", report.Installation.DefinitionPath))
		}
		if report.Installation.State == "invalid_receipt" {
			return failReport(report, "uninstall", "refused", false, fmt.Errorf("refusing to uninstall with invalid receipt: %s", report.Installation.Detail))
		}
		report.Action = "uninstall"
		report.Outcome = "already_uninstalled"
		return finalizeReport(report)
	}
	if report.Manager.State == "unknown" {
		return failReport(report, "uninstall", "refused", true, fmt.Errorf("service manager state is unknown (%s); confirm service is stopped before uninstall", report.Manager.Detail))
	}
	managerInactive := report.Manager.State == "not_loaded"
	if receipt.Platform == PlatformLinux {
		managerInactive = report.Manager.State == "not_loaded" || ((report.Manager.State == "stopped" || report.Manager.State == "failed") && report.Manager.PID == 0)
	}
	if !managerInactive {
		return failReport(report, "uninstall", "refused", false, fmt.Errorf("service manager state is %s; stop service before uninstall", report.Manager.State))
	}
	if report.Daemon.SocketPresent {
		return failReport(report, "uninstall", "refused", false, fmt.Errorf("control socket is still present in %s; stop daemon before uninstall", opts.StateDir))
	}
	if defID, exists, defErr := inspectDefinitionFile(receipt.DefinitionPath); defErr != nil {
		return failReport(report, "uninstall", "refused", false, fmt.Errorf("inspect definition before uninstall: %w", defErr))
	} else if exists && !receiptOwnsDefinition(*receipt, defID.SHA256) {
		return failReport(report, "uninstall", "refused", false, fmt.Errorf("refusing to remove modified definition at %s", receipt.DefinitionPath))
	}

	uninstallingReceipt := *receipt
	uninstallingReceipt.State = "uninstalling"
	if err := writeReceiptAtomic(opts.StateDir, uninstallingReceipt); err != nil {
		return failReport(report, "uninstall", "failed", false, fmt.Errorf("record uninstalling state: %w", err))
	}
	if opts.AfterWritePending != nil {
		if err := opts.AfterWritePending(); err != nil {
			after, _ := observeState(ctx, opts)
			return failReport(after, "uninstall", "failed", false, fmt.Errorf("interrupted after uninstalling receipt: %w", err))
		}
	}
	removed, err := removeDefinitionFile(receipt.DefinitionPath, receipt.DefinitionSHA256, receipt.PendingDefinitionSHA256, receipt.PreviousDefinitionSHA256)
	if err != nil {
		after, _ := observeState(ctx, opts)
		after.DefinitionRemoved = removed
		return failReport(after, "uninstall", "failed", false, err)
	}
	var cmdReceipts []CommandSummary
	executed := false
	if req, ok := reloadStepRequest(*receipt); ok {
		runner := opts.Runner
		if runner == nil {
			runner = serviceexec.ExecRunner{}
		}
		res := runner.Run(ctx, req)
		if res.Started {
			executed = true
		}
		cmdReceipts = append(cmdReceipts, summarizeCommand(req, res))
		if res.Outcome != serviceexec.Success || res.Uncertain {
			after, _ := observeState(ctx, opts)
			after.DefinitionRemoved = removed
			after.CommandsExecuted = executed
			after.Commands = cmdReceipts
			outcome := "failed"
			if res.Started || res.Uncertain {
				outcome = "unconfirmed"
			}
			return failReport(after, "uninstall", outcome, res.Started || res.Uncertain, fmt.Errorf("systemd daemon-reload after definition removal did not succeed cleanly: %s", cmdReceipts[0].Detail))
		}
	}
	if err := removeReceiptInDir(opts.StateDir); err != nil {
		after, _ := observeState(ctx, opts)
		after.DefinitionRemoved = removed
		after.CommandsExecuted = executed
		after.Commands = cmdReceipts
		return failReport(after, "uninstall", "failed", false, fmt.Errorf("remove service receipt: %w", err))
	}
	after, _ := observeState(ctx, opts)
	after.Action = "uninstall"
	after.Outcome = "uninstalled"
	after.DefinitionRemoved = removed
	after.CommandsExecuted = executed
	after.Commands = cmdReceipts
	return finalizeReport(after)
}

func observeState(ctx context.Context, opts Options) (Report, *serviceReceipt) {
	canonicalStateDir := filepath.Clean(opts.StateDir)
	inst := InstallationSummary{
		State:    "not_installed",
		StateDir: canonicalStateDir,
	}
	receipt, hasReceipt, receiptErr := readReceiptReadOnly(canonicalStateDir)
	var currentExecSHA string
	if receiptErr != nil {
		inst.State = "invalid_receipt"
		inst.Detail = clipDetail(receiptErr.Error())
	} else if hasReceipt && receipt != nil {
		inst.Manager = receipt.Manager
		inst.Label = receipt.Label
		inst.DefinitionPath = receipt.DefinitionPath
		inst.ExecutablePath = receipt.ExecutablePath
		inst.SamplePath = receipt.SamplePath
		inst.Interval = receipt.Interval
		if len(receipt.Roots) > 0 {
			inst.Roots = append([]string(nil), receipt.Roots...)
		}
		inst.ConfigSHA256 = receipt.ConfigSHA256
		inst.DefinitionSHA256 = receipt.DefinitionSHA256
		inst.ExecutableSHA256 = receipt.ExecutableSHA256

		defID, defExists, defErr := inspectDefinitionFile(receipt.DefinitionPath)
		if defErr == nil && defExists && receipt.DefinitionSHA256 != "" && defID.SHA256 == receipt.DefinitionSHA256 {
			inst.OnDiskDefinitionMatches = true
		}
		execID, execErr := daemon.InspectExecutable(receipt.ExecutablePath)
		if execErr == nil {
			currentExecSHA = execID.SHA256
			inst.CurrentExecutableSHA256 = execID.SHA256
			if execID.SHA256 == receipt.ExecutableSHA256 && execID.Dev == receipt.ExecutableDev && execID.Ino == receipt.ExecutableIno {
				inst.OnDiskExecutableMatches = true
			}
		}

		switch receipt.State {
		case "installing":
			inst.State = "interrupted_install"
			inst.Detail = "previous installation was interrupted before completion"
		case "uninstalling":
			inst.State = "interrupted_uninstall"
			inst.Detail = "previous uninstallation was interrupted before completion"
		case "installed":
			switch {
			case defErr != nil:
				inst.State = "definition_drifted"
				inst.Detail = clipDetail(defErr.Error())
			case !defExists:
				inst.State = "definition_missing"
				inst.Detail = "installed definition file is missing"
			case !inst.OnDiskDefinitionMatches:
				inst.State = "definition_drifted"
				inst.Detail = "on-disk definition digest differs from installed receipt"
			case errors.Is(execErr, os.ErrNotExist) || errors.Is(execErr, syscall.ENOENT):
				inst.State = "executable_missing"
				inst.Detail = "installed executable is missing"
			case execErr != nil:
				inst.State = "executable_changed"
				inst.Detail = clipDetail(execErr.Error())
			case !inst.OnDiskExecutableMatches:
				inst.State = "executable_changed"
				inst.Detail = "on-disk executable digest or inode changed since install"
			default:
				inst.State = "installed"
			}
		}
	} else if expectedPath, label, mgr, ok := defaultDefinitionTarget(opts); ok {
		if _, exists, defErr := inspectDefinitionFile(expectedPath); defErr != nil || exists {
			inst.State = "foreign_definition"
			inst.Manager = mgr
			inst.Label = label
			inst.DefinitionPath = expectedPath
			inst.Detail = "service definition file exists without ownership receipt"
		}
	}

	var mgrSummary ManagerSummary
	if receipt != nil {
		mgrSummary = inspectManager(ctx, opts.Runner, receipt.Platform, receipt.Label, receipt.Home, receipt.ConfigHome, receipt.UID)
	} else if expectedPath, label, _, ok := defaultDefinitionTarget(opts); ok && expectedPath != "" {
		uid := opts.UID
		if uid == 0 {
			uid = os.Getuid()
		}
		mgrSummary = inspectManager(ctx, opts.Runner, opts.Platform, label, opts.Home, opts.ConfigHome, uid)
	} else {
		mgrSummary = ManagerSummary{State: "not_queried"}
	}

	daemonSummary := inspectDaemonSocket(ctx, canonicalStateDir, mgrSummary, receipt, currentExecSHA)
	healthy, uncertain, overall := classifyOverallState(inst, mgrSummary, daemonSummary)
	return Report{
		Schema:       StatusSchema,
		Version:      0,
		Uncertain:    uncertain,
		Healthy:      healthy,
		OverallState: overall,
		Installation: inst,
		Manager:      mgrSummary,
		Daemon:       daemonSummary,
	}, receipt
}

func inspectDaemonSocket(ctx context.Context, stateDir string, mgr ManagerSummary, receipt *serviceReceipt, currentDiskExecSHA string) DaemonSummary {
	summary := DaemonSummary{State: "socket_absent"}
	present, err := socketExistsReadOnly(stateDir)
	if err != nil {
		summary.State = "unreachable"
		summary.Detail = clipDetail(err.Error())
		return summary
	}
	if !present {
		return summary
	}
	summary.SocketPresent = true
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := daemon.Read(readCtx, stateDir, "status")
	if err != nil {
		summary.State = "unreachable"
		summary.Detail = clipDetail(err.Error())
		return summary
	}
	var st daemon.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		summary.State = "protocol_error"
		summary.Detail = clipDetail(err.Error())
		return summary
	}
	switch st.State {
	case "awaiting_sample", "observing", "sample_unavailable", "journal_unavailable", "journal_capacity_reached":
		summary.State = st.State
	default:
		summary.State = "protocol_error"
		summary.Detail = "unsupported daemon state"
		return summary
	}
	summary.Reachable = true
	if st.Instance != nil {
		summary.InstanceID = st.Instance.InstanceID
		summary.PID = st.Instance.PID
		summary.StartedAt = st.Instance.StartedAt
		summary.ExecutablePath = st.Instance.ExecutablePath
		summary.ExecutableSHA256 = st.Instance.ExecutableSHA256
		summary.ConfigSHA256 = st.Instance.ConfigSHA256
		if mgr.State == "running" && mgr.PID > 0 && st.Instance.PID == mgr.PID {
			summary.PIDMatchesManager = true
		}
		if receipt != nil && receipt.ConfigSHA256 != "" && st.Instance.ConfigSHA256 == receipt.ConfigSHA256 {
			summary.ConfigMatchesReceipt = true
		}
		if receipt != nil && receipt.ExecutableSHA256 != "" && st.Instance.ExecutableSHA256 == receipt.ExecutableSHA256 {
			summary.ExecutableMatchesReceipt = true
		}
		if currentDiskExecSHA != "" && st.Instance.ExecutableSHA256 == currentDiskExecSHA {
			summary.ExecutableMatchesDisk = true
		}
	}
	return summary
}

func classifyOverallState(inst InstallationSummary, mgr ManagerSummary, dm DaemonSummary) (healthy, uncertain bool, overall string) {
	if mgr.State == "unknown" {
		return false, true, "unknown"
	}
	if inst.State == "interrupted_install" || inst.State == "interrupted_uninstall" || inst.State == "invalid_receipt" {
		return false, false, "interrupted"
	}
	if inst.State == "foreign_definition" {
		return false, false, "conflict"
	}
	if dm.SocketPresent && mgr.State != "running" {
		return false, false, "conflict"
	}
	if mgr.State == "running" && dm.Reachable && !dm.PIDMatchesManager {
		return false, false, "conflict"
	}
	if inst.State == "definition_missing" || inst.State == "definition_drifted" || inst.State == "executable_missing" || inst.State == "executable_changed" {
		return false, false, "drifted"
	}
	if mgr.State == "running" && dm.Reachable && (!dm.ConfigMatchesReceipt || !dm.ExecutableMatchesReceipt || !dm.ExecutableMatchesDisk) {
		return false, false, "drifted"
	}
	if inst.State == "not_installed" && (mgr.State == "not_loaded" || mgr.State == "stopped" || mgr.State == "not_queried") && !dm.SocketPresent {
		return false, false, "not_installed"
	}
	if inst.State == "installed" && (mgr.State == "not_loaded" || mgr.State == "stopped") && !dm.SocketPresent {
		return false, false, "installed_stopped"
	}
	if inst.State == "installed" && inst.OnDiskDefinitionMatches && inst.OnDiskExecutableMatches &&
		mgr.State == "running" && mgr.PID > 0 &&
		dm.SocketPresent && dm.Reachable &&
		dm.PIDMatchesManager && dm.ConfigMatchesReceipt && dm.ExecutableMatchesReceipt && dm.ExecutableMatchesDisk {
		if dm.State == "observing" {
			return true, false, "healthy"
		}
		if dm.State == "awaiting_sample" {
			return false, false, "starting"
		}
	}
	return false, false, "degraded"
}

func receiptOwnsDefinition(r serviceReceipt, sha string) bool {
	if sha == "" {
		return false
	}
	return sha == r.DefinitionSHA256 || sha == r.PendingDefinitionSHA256 || sha == r.PreviousDefinitionSHA256
}

func defaultDefinitionTarget(opts Options) (defPath, label, manager string, ok bool) {
	if opts.Vendor == "" || opts.BinaryName == "" || opts.Home == "" {
		return "", "", "", false
	}
	if !validLabelPart(opts.Vendor) || !validLabelPart(opts.BinaryName) {
		return "", "", "", false
	}
	if validatePath("home", opts.Home, maxPathBytes) != nil {
		return "", "", "", false
	}
	label = opts.Vendor + "." + opts.BinaryName + ".daemon"
	switch opts.Platform {
	case PlatformDarwin:
		return filepath.Join(opts.Home, "Library", "LaunchAgents", label+".plist"), label, "launchd", true
	case PlatformLinux:
		cfgHome := opts.ConfigHome
		if cfgHome == "" {
			cfgHome = filepath.Join(opts.Home, ".config")
		}
		if validatePath("config home", cfgHome, maxPathBytes) != nil {
			return "", "", "", false
		}
		return filepath.Join(cfgHome, "systemd", "user", label+".service"), label, "systemd-user", true
	default:
		return "", "", "", false
	}
}

func defaultProcessGoneProbe(pid int) (bool, error) {
	if pid <= 0 {
		return false, fmt.Errorf("invalid pid %d", pid)
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}

func failReport(report Report, action, outcome string, uncertain bool, err error) (Report, error) {
	report.Action = action
	report.Outcome = outcome
	report.Uncertain = uncertain
	report.Healthy = false
	if report.OverallState == "healthy" {
		if uncertain {
			report.OverallState = "unknown"
		} else {
			report.OverallState = "degraded"
		}
	}
	if err != nil {
		report.Error = clipDetail(err.Error())
	}
	finalized, marshalErr := finalizeReport(report)
	if marshalErr != nil {
		return Report{}, errors.Join(err, marshalErr)
	}
	return finalized, err
}

func finalizeReport(report Report) (Report, error) {
	report.Schema = StatusSchema
	report.Version = 0
	raw, err := json.Marshal(report)
	if err != nil {
		return Report{}, err
	}
	if len(raw)+1 > 128<<10 {
		return Report{}, fmt.Errorf("service status report exceeds 128 KiB")
	}
	return report, nil
}
