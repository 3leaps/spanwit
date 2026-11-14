//go:build darwin || linux

package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/3leaps/spanwit/internal/daemon"
	"github.com/spf13/cobra"
)

func addProfileActivationCommands(parent *cobra.Command, stateDir *string) {
	var activateName, expectedRev, idempotencyKey string
	activate := &cobra.Command{
		Use:   "activate FILE",
		Short: "Persist and activate a reviewed profile revision while the daemon is stopped",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Geteuid() == 0 {
				return fmt.Errorf("run as an ordinary user")
			}
			pack, err := loadProfilePack(args[0])
			if err != nil {
				return err
			}
			status, err := daemon.ActivateProfile(cmd.Context(), *stateDir, pack, daemon.ActivateProfileOptions{
				Profile:          activateName,
				ExpectedRevision: expectedRev,
				IdempotencyKey:   idempotencyKey,
			})
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
		},
	}
	activate.Flags().StringVar(&activateName, "name", "", "persona name within the pack")
	activate.Flags().StringVar(&expectedRev, "expected-revision", "", "expected active pack revision (sha256:<hex> or 'none')")
	activate.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "optional stable activation identity for retry reconciliation")
	_ = activate.MarkFlagRequired("name")
	setCommandCapability(activate, CapabilityMutating)

	var deactivateExpected, deactivateKey string
	deactivate := &cobra.Command{
		Use:   "deactivate",
		Short: "Revoke the active profile revision while the daemon is stopped (retaining pack bodies)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() == 0 {
				return fmt.Errorf("run as an ordinary user")
			}
			status, err := daemon.DeactivateProfile(cmd.Context(), *stateDir, deactivateExpected, deactivateKey)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
		},
	}
	deactivate.Flags().StringVar(&deactivateExpected, "expected-revision", "", "expected active pack revision (sha256:<hex> or 'none')")
	deactivate.Flags().StringVar(&deactivateKey, "idempotency-key", "", "optional stable revocation identity for retry reconciliation")
	setCommandCapability(deactivate, CapabilityMutating)

	status := &cobra.Command{
		Use:   "status",
		Short: "Inspect the active profile revision and binding",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := daemon.InspectProfileStatus(cmd.Context(), *stateDir)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(st)
		},
	}
	setCommandCapability(status, CapabilityObservational)

	parent.AddCommand(activate, deactivate, status)
}
