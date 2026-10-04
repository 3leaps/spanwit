package runtime

import (
	"fmt"
	"os"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/foundry"
	"github.com/fulmenhq/gofulmen/logging"
)

// SetupLogging creates a CLI logger that writes to stderr.
//
// Per the CLI Output Contract (docs/standards/CLI-Output-Contract.md) and
// adopted stdout purity principles, all logging must go to stderr so that
// stdout remains clean for program output / piping.
func SetupLogging(identity *appidentity.Identity) *logging.Logger {
	// NewCLI in gofulmen is expected to target stderr for CLI use cases.
	// We explicitly document the expectation here for code reviewers and agents.
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(foundry.ExitConfigInvalid)
	}
	return logger
}
