//go:build !(darwin || linux)

package cmd

import (
	"fmt"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/spf13/cobra"
)

func newApprovalCmd(_ *appidentity.Identity) *cobra.Command {
	return &cobra.Command{Use: "approval", Short: "One-shot plan approvals (macOS/Linux only)", RunE: func(*cobra.Command, []string) error {
		return fmt.Errorf("approved execution is unavailable on this platform")
	}}
}
