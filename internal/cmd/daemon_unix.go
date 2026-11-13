//go:build darwin || linux

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/3leaps/spanwit/internal/daemon"
	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/spf13/cobra"
)

func newDaemonCmd(identity *appidentity.Identity) *cobra.Command {
	var dir, path string
	home, _ := os.UserHomeDir()
	dir = filepath.Join(home, ".local", "state", identity.BinaryName, "daemon")
	path = home
	parent := &cobra.Command{Use: "daemon", Short: "Experimental observation-only coordinator (foreground)"}
	parent.PersistentFlags().StringVar(&dir, "state-dir", dir, "private coordinator state directory")
	run := &cobra.Command{Use: "run", Args: cobra.NoArgs, Short: "Sample capacity and serve read-only local status"}
	var interval time.Duration
	var roots []string
	run.Flags().StringArrayVar(&roots, "root", nil, "explicit attribution root (repeatable, maximum 16; opt-in deep walks)")
	run.Flags().DurationVar(&interval, "interval", 5*time.Minute, "capacity sample interval (minimum 60s)")
	run.Flags().StringVar(&path, "path", path, "filesystem to sample without walking")
	run.RunE = func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() == 0 {
			return fmt.Errorf("run as an ordinary user")
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return daemon.RunWithRoots(ctx, dir, path, interval, roots)
	}
	setCommandCapability(run, CapabilityMutating)
	once := &cobra.Command{Use: "once", Args: cobra.NoArgs, Short: "Append one offline sample under exclusive writer ownership", RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() == 0 {
			return fmt.Errorf("run as an ordinary user")
		}
		c, err := daemon.Open(dir, nil)
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		status, err := c.Sample(cmd.Context(), path)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}}
	once.Flags().StringVar(&path, "path", path, "filesystem to sample without walking")
	setCommandCapability(once, CapabilityMutating)
	parent.AddCommand(run, once, newDaemonServiceCmd(identity, &dir))
	for _, op := range []string{"status", "capabilities", "proposals"} {
		command := &cobra.Command{Use: op, Args: cobra.NoArgs, Short: "Read cached daemon " + op, RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := daemon.Read(cmd.Context(), dir, cmd.Name())
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(raw)
			return err
		}}
		setCommandCapability(command, CapabilityObservational)
		parent.AddCommand(command)
	}
	return parent
}
