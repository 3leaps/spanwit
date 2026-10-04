package cmd

import (
	"fmt"
	"runtime"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/crucible"
	"github.com/spf13/cobra"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// SetVersionInfo sets the version information (called from main)
func SetVersionInfo(v, c, b string) {
	version = v
	commit = c
	buildDate = b
}

// newVersionCmd creates the version command
func newVersionCmd(identity *appidentity.Identity) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long:  `Print the version information for the tool, including commit and build date.`,
		Run: func(cmd *cobra.Command, args []string) {
			extended, _ := cmd.Flags().GetBool("extended")
			if extended {
				helperVersion := crucible.GetVersion()
				fmt.Printf("Version:         %s\n", version)
				fmt.Printf("Commit:          %s\n", commit)
				fmt.Printf("Build Date:      %s\n", buildDate)
				fmt.Printf("Go Version:      %s\n", runtime.Version())
				fmt.Printf("Gofulmen:        v%s\n", helperVersion.Gofulmen)
				fmt.Printf("Crucible:        v%s\n", helperVersion.Crucible)
			} else {
				fmt.Printf("%s %s\n", identity.BinaryName, version)
			}
		},
	}

	cmd.Flags().BoolP("extended", "e", false, "show extended version information")
	return cmd
}
