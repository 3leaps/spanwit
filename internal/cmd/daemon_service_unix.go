//go:build darwin || linux

package cmd

import (
	"encoding/json"
	"os"
	"runtime"
	"time"

	"github.com/3leaps/spanwit/internal/serviceexec"
	"github.com/3leaps/spanwit/internal/userservice"
	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/spf13/cobra"
)

var serviceCommandRunner serviceexec.Runner

func newDaemonServiceCmd(identity *appidentity.Identity, stateDir *string) *cobra.Command {
	parent := &cobra.Command{Use: "service", Short: "Prepare and manage an explicit per-user observation service"}
	setCommandCapability(parent, CapabilityObservational)
	parent.AddCommand(
		newDaemonServicePreviewCmd(identity, stateDir),
		newDaemonServiceInstallCmd(identity, stateDir),
		newDaemonServiceStartCmd(identity, stateDir),
		newDaemonServiceStatusCmd(identity, stateDir),
		newDaemonServiceStopCmd(identity, stateDir),
		newDaemonServiceUninstallCmd(identity, stateDir),
	)
	return parent
}

func newDaemonServicePreviewCmd(identity *appidentity.Identity, stateDir *string) *cobra.Command {
	var executable, platform, path string
	var roots []string
	var interval time.Duration
	preview := &cobra.Command{
		Use: "preview", Args: cobra.NoArgs,
		Short: "Render a service definition without installing or starting it",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := buildServiceConfig(identity, *stateDir, platform, executable, path, roots, interval)
			if err != nil {
				return err
			}
			plan, err := userservice.Render(cfg)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
		},
	}
	bindServiceConfigFlags(preview, &executable, &platform, &path, &roots, &interval, "absolute path of the intended installed executable (not verified by preview)")
	setCommandCapability(preview, CapabilityObservational)
	return preview
}

func newDaemonServiceInstallCmd(identity *appidentity.Identity, stateDir *string) *cobra.Command {
	var executable, path string
	var roots []string
	var interval time.Duration
	install := &cobra.Command{
		Use: "install", Args: cobra.NoArgs,
		Short: "Validate the executable and write an owned per-user service definition without starting it",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := buildServiceConfig(identity, *stateDir, runtime.GOOS, executable, path, roots, interval)
			if err != nil {
				return err
			}
			opts := buildServiceOptions(identity, *stateDir, runtime.GOOS)
			report, runErr := userservice.Install(cmd.Context(), cfg, opts)
			return writeServiceReport(cmd, report, runErr)
		},
	}
	install.Flags().StringVar(&executable, "executable", "", "absolute path of the verified installed executable")
	_ = install.MarkFlagRequired("executable")
	install.Flags().StringVar(&path, "path", "", "filesystem to sample (default: home directory)")
	install.Flags().StringArrayVar(&roots, "root", nil, "explicit attribution root (repeatable, maximum 16)")
	install.Flags().DurationVar(&interval, "interval", 5*time.Minute, "capacity sample interval (minimum 60s)")
	setCommandCapability(install, CapabilityMutating)
	return install
}

func newDaemonServiceStartCmd(identity *appidentity.Identity, stateDir *string) *cobra.Command {
	start := &cobra.Command{
		Use: "start", Args: cobra.NoArgs,
		Short: "Start the installed user service and verify bound daemon instance health",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := buildServiceOptions(identity, *stateDir, runtime.GOOS)
			report, runErr := userservice.Start(cmd.Context(), opts)
			return writeServiceReport(cmd, report, runErr)
		},
	}
	setCommandCapability(start, CapabilityMutating)
	return start
}

func newDaemonServiceStatusCmd(identity *appidentity.Identity, stateDir *string) *cobra.Command {
	status := &cobra.Command{
		Use: "status", Args: cobra.NoArgs,
		Short: "Observe service definition integrity, manager state, and daemon health",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := buildServiceOptions(identity, *stateDir, runtime.GOOS)
			report, runErr := userservice.Status(cmd.Context(), opts)
			return writeServiceReport(cmd, report, runErr)
		},
	}
	setCommandCapability(status, CapabilityObservational)
	return status
}

func newDaemonServiceStopCmd(identity *appidentity.Identity, stateDir *string) *cobra.Command {
	stop := &cobra.Command{
		Use: "stop", Args: cobra.NoArgs,
		Short: "Stop the owned user service and confirm manager and socket termination",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := buildServiceOptions(identity, *stateDir, runtime.GOOS)
			report, runErr := userservice.Stop(cmd.Context(), opts)
			return writeServiceReport(cmd, report, runErr)
		},
	}
	setCommandCapability(stop, CapabilityMutating)
	return stop
}

func newDaemonServiceUninstallCmd(identity *appidentity.Identity, stateDir *string) *cobra.Command {
	uninstall := &cobra.Command{
		Use: "uninstall", Args: cobra.NoArgs,
		Short: "Remove the owned service definition and receipt while preserving journal state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := buildServiceOptions(identity, *stateDir, runtime.GOOS)
			report, runErr := userservice.Uninstall(cmd.Context(), opts)
			return writeServiceReport(cmd, report, runErr)
		},
	}
	setCommandCapability(uninstall, CapabilityMutating)
	return uninstall
}

func bindServiceConfigFlags(cmd *cobra.Command, executable, platform, path *string, roots *[]string, interval *time.Duration, execHelp string) {
	cmd.Flags().StringVar(executable, "executable", "", execHelp)
	_ = cmd.MarkFlagRequired("executable")
	cmd.Flags().StringVar(platform, "platform", runtime.GOOS, "service definition platform: darwin or linux")
	cmd.Flags().StringVar(path, "path", "", "filesystem to sample (default: home directory)")
	cmd.Flags().StringArrayVar(roots, "root", nil, "explicit attribution root (repeatable, maximum 16)")
	cmd.Flags().DurationVar(interval, "interval", 5*time.Minute, "capacity sample interval (minimum 60s)")
}

func buildServiceConfig(identity *appidentity.Identity, stateDir, platform, executable, path string, roots []string, interval time.Duration) (userservice.Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return userservice.Config{}, err
	}
	if path == "" {
		path = home
	}
	configHome := ""
	if platform == "linux" {
		configHome = os.Getenv("XDG_CONFIG_HOME")
	}
	return userservice.Config{
		Platform:   platform,
		Executable: executable,
		Home:       home,
		ConfigHome: configHome,
		StateDir:   stateDir,
		Path:       path,
		Roots:      roots,
		Interval:   interval,
		BinaryName: identity.BinaryName,
		Vendor:     identity.Vendor,
		UID:        os.Getuid(),
	}, nil
}

func buildServiceOptions(identity *appidentity.Identity, stateDir, platform string) userservice.Options {
	home, _ := os.UserHomeDir()
	configHome := ""
	if platform == "linux" {
		configHome = os.Getenv("XDG_CONFIG_HOME")
	}
	return userservice.Options{
		StateDir:   stateDir,
		Platform:   platform,
		Home:       home,
		ConfigHome: configHome,
		BinaryName: identity.BinaryName,
		Vendor:     identity.Vendor,
		UID:        os.Getuid(),
		Runner:     serviceCommandRunner,
	}
}

func writeServiceReport(cmd *cobra.Command, report userservice.Report, runErr error) error {
	if report.Schema != "" {
		if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(report); encodeErr != nil && runErr == nil {
			return encodeErr
		}
	}
	return runErr
}
