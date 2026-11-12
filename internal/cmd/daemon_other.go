//go:build !darwin && !linux

package cmd

import (
	"fmt"
	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/spf13/cobra"
)

func newDaemonCmd(_ *appidentity.Identity) *cobra.Command {
	return &cobra.Command{Use: "daemon", Short: "Observation coordinator (macOS/Linux only)", RunE: func(*cobra.Command, []string) error { return fmt.Errorf("daemon platform unsupported") }}
}
