//go:build darwin || linux

package cmd

import (
	"encoding/json"
	"os"
	"runtime"
	"time"

	"github.com/3leaps/spanwit/internal/userservice"
	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/spf13/cobra"
)

func newDaemonServiceCmd(identity *appidentity.Identity, stateDir *string) *cobra.Command {
	parent := &cobra.Command{Use: "service", Short: "Prepare an explicit per-user service definition"}
	setCommandCapability(parent, CapabilityObservational)
	var executable, platform, path string
	var roots []string
	var interval time.Duration
	preview := &cobra.Command{
		Use: "preview", Args: cobra.NoArgs,
		Short: "Render a service definition without installing or starting it",
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			if path == "" {
				path = home
			}
			configHome := ""
			if platform == "linux" {
				configHome = os.Getenv("XDG_CONFIG_HOME")
			}
			plan, err := userservice.Render(userservice.Config{
				Platform: platform, Executable: executable, Home: home, ConfigHome: configHome,
				StateDir: *stateDir, Path: path, Roots: roots, Interval: interval,
				BinaryName: identity.BinaryName, Vendor: identity.Vendor, UID: os.Getuid(),
			})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
		},
	}
	preview.Flags().StringVar(&executable, "executable", "", "absolute path of the intended installed executable (not verified by preview)")
	_ = preview.MarkFlagRequired("executable")
	preview.Flags().StringVar(&platform, "platform", runtime.GOOS, "service definition platform: darwin or linux")
	preview.Flags().StringVar(&path, "path", "", "filesystem to sample (default: home directory)")
	preview.Flags().StringArrayVar(&roots, "root", nil, "explicit attribution root (repeatable, maximum 16)")
	preview.Flags().DurationVar(&interval, "interval", 5*time.Minute, "capacity sample interval (minimum 60s)")
	setCommandCapability(preview, CapabilityObservational)
	parent.AddCommand(preview)
	return parent
}
