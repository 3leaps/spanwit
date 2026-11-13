//go:build !darwin && !linux

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func addProfileActivationCommands(parent *cobra.Command, _ *string) {
	for _, use := range []string{"activate FILE", "deactivate", "status"} {
		cmd := &cobra.Command{
			Use:   use,
			Short: "Profile store commands (macOS/Linux only)",
			RunE: func(*cobra.Command, []string) error {
				return fmt.Errorf("profile activation store unsupported on this platform")
			},
		}
		if use == "status" {
			setCommandCapability(cmd, CapabilityObservational)
		} else {
			setCommandCapability(cmd, CapabilityMutating)
		}
		parent.AddCommand(cmd)
	}
}
