package cmd

import (
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/crucible"
	"github.com/fulmenhq/gofulmen/foundry"

	"github.com/3leaps/spanwit/internal/config"
)

// newDoctorCmd creates the doctor command
func newDoctorCmd(identity *appidentity.Identity) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run diagnostic checks",
		Long:  `Run diagnostic checks on the system and suggest fixes for common issues.`,
		Run: func(cmd *cobra.Command, args []string) {
			loggerInstance.Info(fmt.Sprintf("=== %s Doctor ===", identity.BinaryName))
			loggerInstance.Info("")
			loggerInstance.Info("Running diagnostic checks...")
			loggerInstance.Info("")

			allChecks := true
			exitCode := foundry.ExitSuccess

			// Check 1: Go version
			goVersion := runtime.Version()
			if goVersion >= "go1.21" {
				loggerInstance.Info("[1/5] Go version... ✅ "+goVersion, zap.String("go_version", goVersion))
			} else {
				loggerInstance.Warn("[1/5] Go version... ⚠️  "+goVersion+" (recommended: go1.21+)", zap.String("go_version", goVersion))
				allChecks = false
			}

			// Check 2: Gofulmen/Crucible access
			version := crucible.GetVersion()
			if version.Gofulmen != "" {
				loggerInstance.Info("[2/5] Gofulmen access... ✅ v"+version.Gofulmen, zap.String("gofulmen_version", version.Gofulmen))
			} else {
				loggerInstance.Error("[2/5] Gofulmen access... ❌ Cannot access Gofulmen")
				allChecks = false
				exitCode = foundry.ExitExternalServiceUnavailable
			}

			if version.Crucible != "" {
				loggerInstance.Info("[3/5] Crucible access... ✅ v"+version.Crucible, zap.String("crucible_version", version.Crucible))
			} else {
				loggerInstance.Warn("[3/5] Crucible access... ⚠️  Cannot access Crucible (embedded version used)")
			}

			// Check 4: App Identity
			if identity.BinaryName != "" && identity.Vendor != "" {
				loggerInstance.Info("[4/5] App identity... ✅ Loaded successfully",
					zap.String("binary_name", identity.BinaryName),
					zap.String("vendor", identity.Vendor))
			} else {
				loggerInstance.Error("[4/5] App identity... ❌ Invalid app identity")
				allChecks = false
				exitCode = foundry.ExitConfigInvalid
			}

			// Check 5: Config directory
			homeDir, err := os.UserHomeDir()
			if err != nil {
				loggerInstance.Error("[5/5] Config directory... ❌ Cannot find home directory", zap.Error(err))
				allChecks = false
				if exitCode == foundry.ExitSuccess {
					exitCode = foundry.ExitFileNotFound
				}
			} else {
				configDir := fmt.Sprintf("%s/.config/%s", homeDir, identity.ConfigName)
				// Check if config directory exists or can be created
				if stat, err := os.Stat(configDir); err == nil && stat.IsDir() {
					loggerInstance.Info("[5/5] Config directory... ✅ "+configDir, zap.String("config_dir", configDir))
				} else {
					loggerInstance.Info("[5/5] Config directory... ⚠️  "+configDir+" (will be created on first config save)", zap.String("config_dir", configDir))
				}
				loggerInstance.Info("      Config file... "+config.DefaultPath(identity), zap.String("config_file", config.DefaultPath(identity)))
			}

			loggerInstance.Info("")
			if allChecks {
				loggerInstance.Info(fmt.Sprintf("✅ All checks passed! Your %s installation is healthy.", identity.BinaryName))
			} else {
				loggerInstance.Warn("⚠️  Some checks failed. Review the output above for details.")
			}
			loggerInstance.Info("")
			loggerInstance.Info("=== End Diagnostics ===")

			// Exit with appropriate code
			if exitCode != foundry.ExitSuccess {
				os.Exit(exitCode)
			}
		},
	}

	return cmd
}
