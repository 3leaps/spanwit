package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
	"github.com/spf13/cobra"
)

var (
	// Global flags
	logLevel   string
	configPath string
)

// rootCmd represents the base command
var rootCmd *cobra.Command

// loggerInstance holds the logger (used by subcommands)
var loggerInstance *logging.Logger

// Execute runs the root command
func Execute() error {
	return rootCmd.Execute()
}

// Initialize sets up the root command with app identity, logger, and config
func Initialize(ctx context.Context, identity *appidentity.Identity, logger *logging.Logger) {
	loggerInstance = logger
	mutationEnvPrefix = identity.EnvPrefix
	if mutationEnvPrefix != "" && !strings.HasSuffix(mutationEnvPrefix, "_") {
		mutationEnvPrefix += "_"
	}

	rootCmd = &cobra.Command{
		Use:   identity.BinaryName,
		Short: identity.Description,
		Long:  identity.Description,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Set log level from flag if provided
			if logLevel != "" {
				// Parse level: trace, debug, info, warn, error
				switch strings.ToUpper(logLevel) {
				case "TRACE":
					logger.SetLevel(logging.TRACE)
				case "DEBUG":
					logger.SetLevel(logging.DEBUG)
				case "INFO":
					logger.SetLevel(logging.INFO)
				case "WARN", "WARNING":
					logger.SetLevel(logging.WARN)
				case "ERROR":
					logger.SetLevel(logging.ERROR)
				default:
					return fmt.Errorf("invalid log level: %s (use trace|debug|info|warn|error)", logLevel)
				}
			}
			// Fail-closed no-mutation assertion before discovery/work.
			return enforceMutationPolicy(cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	// Global flags
	rootCmd.PersistentFlags().StringVarP(&logLevel, "log-level", "l", "info", "logging level (trace|debug|info|warn|error)")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "config file path override")
	rootCmd.PersistentFlags().BoolVar(&assertReadOnlyFlag, readOnlyFlagName, false,
		"assert the resolved invocation has no mutating capability (also via "+mutationEnvPrefix+envReadOnlySuffix+
			"; fails closed on conflict, prune --execute/-e, or inventory --coverage-attest)")

	// Add subcommands (each declares a mutation capability annotation).
	addClassified := func(c *cobra.Command, capability string) {
		setCommandCapability(c, capability)
		rootCmd.AddCommand(c)
	}
	addClassified(newVersionCmd(identity), CapabilityObservational)
	addClassified(newEnvinfoCmd(identity), CapabilityObservational)
	addClassified(newDoctorCmd(identity), CapabilityObservational)
	addClassified(newExampleCmd(identity), CapabilityObservational)
	addClassified(newValidateCmd(identity), CapabilityObservational)
	addClassified(newScanCmd(identity), CapabilityObservational)
	addClassified(newSpaceCmd(identity), CapabilityObservational)
	addClassified(newInventoryCmd(identity), CapabilityObservational)
	addClassified(newDiagnoseCmd(identity), CapabilityObservational)
	addClassified(newObserveCmd(identity), CapabilityMutating)
	// prune is planning by default; --execute elevates to mutating at enforce time.
	addClassified(newPruneCmd(identity), CapabilityPlanning)
}
