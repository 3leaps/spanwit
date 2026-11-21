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
//
// The configuration is gofulmen's CLI logger (logging.NewCLI) without stack
// traces: an error log is a message for the person running the command, and a
// stack trace of the logging call says nothing about the error.
func SetupLogging(identity *appidentity.Identity) *logging.Logger {
	logger, err := logging.New(&logging.LoggerConfig{
		DefaultLevel: "INFO",
		Service:      identity.BinaryName,
		Environment:  "cli",
		Sinks: []logging.SinkConfig{{
			Type:    "console",
			Format:  "console",
			Console: &logging.ConsoleSinkConfig{Stream: "stderr", Colorize: true},
		}},
		EnableCaller:     false,
		EnableStacktrace: false,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(foundry.ExitConfigInvalid)
	}
	return logger
}
