package cmd

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/foundry"
	"github.com/spf13/cobra"

	"github.com/3leaps/spanwit/internal/config"
)

// scanOptions carries the resolved scan inputs into the testable core.
type scanOptions struct {
	path           string
	minSize        string
	configOverride string
	showUnverified bool
}

// reclaimablePatterns is the by-name heuristic list. It NO LONGER decides what
// scan reports — the context-aware signature engine does that. It survives only
// to power the read-only "unverified" transparency footer: directories that look
// reclaimable by name but carry no signature evidence are summarized, never
// counted, so users can see what was deliberately not verified.
var reclaimablePatterns = []string{
	"target", "node_modules", "dist", "build", ".next", ".turbo", ".cache", "out", "tmp", "temp",
}

type ScanResult struct {
	Path string
	Size int64
}

func newScanCmd(identity *appidentity.Identity) *cobra.Command {
	var (
		minSizeStr     string
		configOverride string
		showUnverified bool
	)

	cmd := &cobra.Command{
		Use:   "scan [path]",
		Short: "Scan for reclaimable disk space (read-only, context-aware)",
		Long: `Scan a directory for reclaimable build artifacts using the same
context-aware signature engine as prune: matches are verified by context (for
example, a Rust target/ next to a Cargo.toml), not by directory name alone.

Read-only — scan never deletes; use prune to reclaim.

Directories that merely look reclaimable by name but carry no signature evidence
are not counted. They are summarized in a footer; pass --show-unverified to list
them.

By default no size or age filter is applied. Pass --min-size to set a floor.
Pass --config to add your own signatures/targets on top of the built-ins.

Exit codes (see docs/standards/Exit-Codes.md):
  0  Success
  1  Partial success (some paths could not be read)
  2  Error
  3  Usage error

See docs/standards/CLI-Output-Contract.md for stdout/stderr rules.
`,
		Args: cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			opts := scanOptions{
				path:           ".",
				minSize:        minSizeStr,
				configOverride: configOverride,
				showUnverified: showUnverified,
			}
			if len(args) > 0 {
				opts.path = args[0]
			}
			os.Exit(runScan(identity, os.Stdout, os.Stderr, opts))
		},
	}

	cmd.Flags().StringVarP(&minSizeStr, "min-size", "m", "", "minimum size to report (default: no filter)")
	cmd.Flags().StringVarP(&configOverride, "config", "c", "", "add signatures/targets from this config on top of the built-ins")
	cmd.Flags().BoolVar(&showUnverified, "show-unverified", false, "list the name-shaped dirs that were not context-verified")

	return cmd
}

// runScan is the testable scan core. It writes human output to stdout/stderr and
// returns the process exit code (0 success, 1 partial, 2 error, 3 usage). It is
// read-only and never deletes.
func runScan(identity *appidentity.Identity, stdout, stderr io.Writer, opts scanOptions) int {
	targetPath := opts.path
	if targetPath == "" {
		targetPath = "."
	}

	root, err := cleanConfiguredPath(targetPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 2
	}
	if _, err := os.Stat(root); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: cannot access path %q: %v\n", root, err)
		return 2
	}

	// No silent size/age gate: filtering is opt-in via --min-size only.
	var minSize int64
	if opts.minSize != "" {
		minSize, err = parseSize(opts.minSize)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: invalid --min-size %q\n", opts.minSize)
			return 3
		}
	}

	// Ephemeral profile: the given path + all built-in signatures, whole tree, no
	// gate. buildPrunePlan merges the built-in signature catalog itself, so
	// built-in signature ids resolve without restating them.
	cfg := &config.Config{
		Version: 1,
		Paths: []config.PathProfile{{
			Path:     root,
			MaxDepth: -1,
			Targets:  builtInSignatureTargets(),
		}},
	}
	if opts.minSize != "" {
		cfg.Defaults.MinSize = opts.minSize
	}
	// --config unions the user's signatures/targets on top of the built-ins (scan
	// optimizes for honest coverage; unlike prune, config is additive here, not
	// authoritative).
	if opts.configOverride != "" {
		userCfg, _, err := config.LoadConfig(context.Background(), identity, loggerInstance, opts.configOverride)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
			return 2
		}
		cfg.Signatures = userCfg.Signatures
		for _, p := range userCfg.Paths {
			cfg.Paths[0].Targets = append(cfg.Paths[0].Targets, p.Targets...)
		}
	}

	_, _ = fmt.Fprintf(stderr, "Scanning (context-aware) in: %s\n", root)

	plan, err := buildPrunePlan(context.Background(), cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 2
	}

	writePlanText(stdout, plan)

	// Transparency footer: name-shaped directories the engine did not
	// context-verify. Summarized (and optionally listed), never counted.
	verified := make(map[string]bool, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		verified[candidate.Path] = true
	}
	unverified, unverifiedBytes := nameShapedUnverified(root, verified, minSize)
	if len(unverified) > 0 {
		_, _ = fmt.Fprintln(stdout)
		_, _ = fmt.Fprintf(stdout, "+ %d name-shaped dir(s) (~%s) not context-verified — not counted.\n", len(unverified), humanSize(unverifiedBytes))
		if opts.showUnverified {
			for _, u := range unverified {
				_, _ = fmt.Fprintf(stdout, "  %-12s  %s\n", humanSize(u.Size), u.Path)
			}
		} else {
			_, _ = fmt.Fprintln(stdout, "  Pass --show-unverified to list them.")
		}
	}

	if len(plan.Warnings) > 0 {
		for _, warning := range plan.Warnings {
			_, _ = fmt.Fprintf(stderr, "Warning: %v\n", warning)
		}
		_, _ = fmt.Fprintf(stderr, "\n%d path(s) had errors.\n", len(plan.Warnings))
		return 1 // partial success
	}

	return foundry.ExitSuccess
}

// builtInSignatureTargets returns one target per built-in signature, sorted by id
// for deterministic evaluation order.
func builtInSignatureTargets() []config.Target {
	var ids []string
	for domain, ecosystems := range config.BuiltInSignatures() {
		for ecosystem, signatures := range ecosystems {
			for name := range signatures {
				ids = append(ids, domain+"."+ecosystem+"."+name)
			}
		}
	}
	sort.Strings(ids)
	targets := make([]config.Target, 0, len(ids))
	for _, id := range ids {
		targets = append(targets, config.Target{Signature: id})
	}
	return targets
}

// nameShapedUnverified walks root for directories whose name looks reclaimable
// but which the engine did not context-verify (their paths are absent from
// verified). Sizes are approximate: like the engine, it stops descending at the
// first name match. Results are sorted largest-first.
func nameShapedUnverified(root string, verified map[string]bool, minSize int64) ([]ScanResult, int64) {
	var results []ScanResult
	var total int64

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() || path == root {
			return nil
		}
		for _, pattern := range reclaimablePatterns {
			if d.Name() == pattern {
				if !verified[path] {
					if size, _ := dirSize(path); size >= minSize {
						results = append(results, ScanResult{Path: path, Size: size})
						total += size
					}
				}
				return filepath.SkipDir
			}
		}
		return nil
	})

	sort.Slice(results, func(i, j int) bool { return results[i].Size > results[j].Size })
	return results, total
}
