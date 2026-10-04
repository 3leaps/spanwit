package main

import (
	"context"
	"fmt"
	"os"

	"github.com/3leaps/spanwit/internal/assets/appidentity"
	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/foundry"
	"github.com/fulmenhq/gofulmen/signals"
	"go.uber.org/zap"

	"github.com/3leaps/spanwit/internal/cmd"
	"github.com/3leaps/spanwit/internal/runtime"
)

// Version information set via ldflags during build
// Example: go build -ldflags="-X main.version=1.0.0 -X main.commit=abc123 -X main.buildDate=2025-11-09"
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	ctx := context.Background()

	// Register embedded app identity fallback (so the built binary works
	// when copied outside the repo).
	if err := appidentityembed.Register(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to register embedded app identity: %v\n", err)
		os.Exit(foundry.ExitConfigInvalid)
	}

	// Load app identity
	identity, err := appidentity.Get(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load app identity: %v\n", err)
		os.Exit(foundry.ExitConfigInvalid)
	}

	// Setup logging
	logger := runtime.SetupLogging(identity)

	// Register shutdown hooks
	signals.OnShutdown(func(ctx context.Context) error {
		logger.Info("shutting down gracefully")
		return nil
	})
	_ = signals.EnableDoubleTap(signals.DoubleTapConfig{}) // Error handling via shutdown hooks

	// Start signal listener in background
	go func() {
		if err := signals.Listen(ctx); err != nil {
			logger.Error("signal listener failed", zap.Error(err))
		}
	}()

	// Set version info for commands to access
	cmd.SetVersionInfo(version, commit, buildDate)

	// Initialize and execute root command
	cmd.Initialize(ctx, identity, logger)
	if err := cmd.Execute(); err != nil {
		logger.Error("command execution failed", zap.Error(err))
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(foundry.ExitFailure)
	}

	// Graceful exit
	os.Exit(foundry.ExitSuccess)
}
