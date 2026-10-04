package cmd

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/crucible"

	"github.com/3leaps/spanwit/internal/config"
)

// newEnvinfoCmd creates the envinfo command
func newEnvinfoCmd(identity *appidentity.Identity) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "envinfo",
		Short: "Display environment information",
		Long:  `Display comprehensive environment, configuration, and version information.`,
		Run: func(cmd *cobra.Command, args []string) {
			version := crucible.GetVersion()

			loggerInstance.Info(fmt.Sprintf("=== %s Environment Information ===", identity.BinaryName))
			loggerInstance.Info("")

			// Application Info
			loggerInstance.Info("Application:")
			loggerInstance.Info("  Name:        "+identity.BinaryName, zap.String("binary_name", identity.BinaryName))
			loggerInstance.Info("  Vendor:      "+identity.Vendor, zap.String("vendor", identity.Vendor))
			loggerInstance.Info("  Description: "+identity.Description, zap.String("description", identity.Description))
			loggerInstance.Info("  Version:     "+getVersionString(), zap.String("version", getVersionString()))
			loggerInstance.Info("  Commit:      "+commit, zap.String("commit", commit))
			loggerInstance.Info("  Built:       "+buildDate, zap.String("build_date", buildDate))
			loggerInstance.Info("")

			// SSOT Info
			loggerInstance.Info("SSOT Dependencies:")
			loggerInstance.Info("  Gofulmen:    v"+version.Gofulmen, zap.String("gofulmen_version", version.Gofulmen))
			loggerInstance.Info("  Crucible:    v"+version.Crucible, zap.String("crucible_version", version.Crucible))
			loggerInstance.Info("")

			// Runtime Info
			loggerInstance.Info("Runtime:")
			loggerInstance.Info("  Go Version:  "+runtime.Version(), zap.String("go_version", runtime.Version()))
			loggerInstance.Info("  GOOS:        "+runtime.GOOS, zap.String("goos", runtime.GOOS))
			loggerInstance.Info("  GOARCH:      "+runtime.GOARCH, zap.String("goarch", runtime.GOARCH))
			loggerInstance.Info(fmt.Sprintf("  NumCPU:      %d", runtime.NumCPU()), zap.Int("num_cpu", runtime.NumCPU()))
			loggerInstance.Info("")

			// Configuration
			loggerInstance.Info("Configuration:")
			loggerInstance.Info("  Env Prefix:  "+identity.EnvPrefix, zap.String("env_prefix", identity.EnvPrefix))
			loggerInstance.Info("  Config Name: "+identity.ConfigName, zap.String("config_name", identity.ConfigName))

			// Config path
			homeDir, err := os.UserHomeDir()
			if err == nil {
				_ = homeDir
				configPath := config.DefaultPath(identity)
				if _, err := os.Stat(configPath); err == nil {
					loggerInstance.Info("  Config File: "+configPath+" (exists)", zap.String("config_file", configPath))
				} else {
					loggerInstance.Info("  Config File: "+configPath+" (not found, using defaults)", zap.String("config_file", configPath))
				}
			}

			// Environment variables (show if set)
			loggerInstance.Info("")
			loggerInstance.Info("Environment Variables:")
			envVars := []string{
				identity.EnvPrefix + "LOG_LEVEL",
				identity.EnvPrefix + "CONFIG_PATH",
			}
			foundAny := false
			for _, envVar := range envVars {
				if val := os.Getenv(envVar); val != "" {
					loggerInstance.Info("  "+envVar+"="+val, zap.String("env_var", envVar), zap.String("value", val))
					foundAny = true
				}
			}
			if !foundAny {
				loggerInstance.Info("  (no environment variables set with prefix " + identity.EnvPrefix + ")")
			}

			loggerInstance.Info("")
			loggerInstance.Info(fmt.Sprintf("=== End %s Environment Information ===", identity.BinaryName))
		},
	}

	return cmd
}

func getVersionString() string {
	if version != "" && version != "dev" {
		return version
	}
	return "dev"
}
