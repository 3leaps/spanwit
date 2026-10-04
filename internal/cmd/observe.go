package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/spf13/cobra"

	"github.com/3leaps/spanwit/internal/observe"
)

// newObserveCmd creates the observe+propose command (one-shot cycle; no daemon loop yet).
func newObserveCmd(identity *appidentity.Identity) *cobra.Command {
	var (
		stateDir   string
		format     string
		register   []string
		foreground bool
		volLabel   string
	)

	cmd := &cobra.Command{
		Use:   "observe",
		Short: "Observe registered volumes and emit propose-only capacity guidance",
		Long: `Observe registered volumes by opaque filesystem identity, apply pressure
hysteresis, compute growth/runway only when comparable, and emit dry-run-safe
proposals (recipe ids / notes). Never deletes, elevates, or writes user config.

State is local telemetry under --state-dir. Because observe writes that state,
the global --read-only assertion rejects observe commands. Corrupt or incompatible state fails closed to an
empty incomparable baseline (no stale growth/runway advice).

Subcommands:
  register   Resolve path → opaque volume id and persist registration
  status     Run one sample cycle and print health (alias of once)
  once       One observe cycle (sample → hysteresis → proposals)

Observational only. Privileged OS-scratch inventory is a separate operator
one-shot path, not part of observe. Bounded auto-soft reclaim is out of scope.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObserveOnce(identity, stateDir, format, register, foreground)
		},
	}

	cmd.Flags().StringVar(&stateDir, "state-dir", defaultObserveStateDir(identity), "directory for versioned observe state")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text|json")
	cmd.Flags().StringArrayVar(&register, "register", nil, "path to sample this cycle (also registers volume identity)")
	cmd.Flags().BoolVar(&foreground, "foreground", false, "throttle concurrency for this cycle only (not persisted)")

	reg := &cobra.Command{
		Use:   "register [path]",
		Short: "Register a path's volume identity for observation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("unsupported --format %q (text|json)", format)
			}
			eng := newObserveEngine(stateDir)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			v, err := eng.Register(ctx, args[0], volLabel)
			if err != nil {
				return err
			}
			if format == "json" {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(v)
			}
			_, _ = fmt.Fprintf(os.Stdout, "registered volume_id=%s path=%s\n", v.ID, v.RegisterPath)
			return nil
		},
	}
	reg.Flags().StringVar(&stateDir, "state-dir", defaultObserveStateDir(identity), "directory for versioned observe state")
	reg.Flags().StringVar(&format, "format", "text", "output format: text|json")
	reg.Flags().StringVar(&volLabel, "label", "", "optional label for status display")
	// Leaf must declare capability: resolver uses the executed command, not the parent.
	setCommandCapability(reg, CapabilityMutating)
	cmd.AddCommand(reg)

	cycleFlags := func(c *cobra.Command) {
		c.Flags().StringVar(&stateDir, "state-dir", defaultObserveStateDir(identity), "directory for versioned observe state")
		c.Flags().StringVar(&format, "format", "text", "output format: text|json")
		c.Flags().StringArrayVar(&register, "register", nil, "path to sample this cycle (also registers volume identity)")
		c.Flags().BoolVar(&foreground, "foreground", false, "throttle concurrency for this cycle only (not persisted)")
	}

	once := &cobra.Command{
		Use:   "once",
		Short: "Run one observe cycle (sample → hysteresis → proposals)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObserveOnce(identity, stateDir, format, register, foreground)
		},
	}
	cycleFlags(once)
	setCommandCapability(once, CapabilityMutating)
	cmd.AddCommand(once)

	status := &cobra.Command{
		Use:   "status",
		Short: "Print health by running one sample cycle (alias of once)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObserveOnce(identity, stateDir, format, register, foreground)
		},
	}
	cycleFlags(status)
	setCommandCapability(status, CapabilityMutating)
	cmd.AddCommand(status)

	return cmd
}

func defaultObserveStateDir(identity *appidentity.Identity) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), identity.BinaryName+"-observe")
	}
	return filepath.Join(home, ".local", "state", identity.BinaryName, "observe")
}

func newObserveEngine(stateDir string) *observe.Engine {
	return &observe.Engine{
		Clock:         observe.RealClock{},
		Sampler:       observe.CapacitySampler{},
		Store:         &observe.FileStore{Path: filepath.Join(stateDir, "state.json")},
		Lease:         observe.NewFileCycleLease(stateDir),
		MaxConcurrent: 1,
	}
}

func runObserveOnce(identity *appidentity.Identity, stateDir, format string, register []string, foreground bool) error {
	if format != "text" && format != "json" {
		return fmt.Errorf("unsupported --format %q (text|json)", format)
	}
	eng := newObserveEngine(stateDir)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Default: sample primary write volume (home) if nothing registered and no --register.
	if len(register) == 0 {
		doc, err := eng.Store.Load()
		if err == nil && len(doc.Volumes) == 0 {
			home, _ := os.UserHomeDir()
			if home != "" {
				register = []string{home}
			}
		}
	}

	// Foreground is cycle-scoped (not sticky). Mutation contract mirrors the invocation.
	res, err := eng.ObserveOnce(ctx, observe.ObserveOpts{
		RegisterPaths:    register,
		Foreground:       foreground,
		MutationContract: ResolvedMutationContract(),
	})
	if err != nil {
		if observe.IsCycleBusy(err) {
			return fmt.Errorf("observe cycle unavailable (another process holds the state-dir lease): %w", err)
		}
		return err
	}

	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res.Health)
	}
	_, _ = fmt.Fprint(os.Stdout, observe.HealthText(res.Health))
	if len(res.Notifies) > 0 {
		_, _ = fmt.Fprintf(os.Stderr, "level transitions (notify): %v\n", res.Notifies)
	}
	return nil
}
