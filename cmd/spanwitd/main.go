//go:build darwin || linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	appidentityembed "github.com/3leaps/spanwit/internal/assets/appidentity"
	"github.com/3leaps/spanwit/internal/daemon"
	"github.com/fulmenhq/gofulmen/appidentity"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("run as an ordinary user")
	}
	if err := appidentityembed.Register(); err != nil {
		return err
	}
	identity, err := appidentity.Get(context.Background())
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := flag.String("state-dir", filepath.Join(home, ".local", "state", identity.BinaryName, "daemon"), "private coordinator state directory")
	path := flag.String("path", home, "filesystem to sample")
	interval := flag.Duration("interval", 5*time.Minute, "sample interval (minimum 60s)")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional argument")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.Run(ctx, *dir, *path, *interval)
}
