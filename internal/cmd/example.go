package cmd

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/pathfinder"
)

// newExampleCmd creates the example command
// This demonstrates the REQUIRED pathfinder module for safe filesystem traversal
func newExampleCmd(identity *appidentity.Identity) *cobra.Command {
	var (
		root     string
		include  []string
		exclude  []string
		maxDepth int
	)

	binaryName := "tool"
	if identity != nil && identity.BinaryName != "" {
		binaryName = identity.BinaryName
	}

	cmd := &cobra.Command{
		Use:   "example [path]",
		Short: "Example command demonstrating pathfinder usage",
		Long: fmt.Sprintf(`This is a sample command that demonstrates the REQUIRED pathfinder module.
It safely traverses directories and finds files matching patterns, showing
how microtools should handle filesystem operations.

Examples:
  %s example .                          # Find all files in current directory
  %s example . --include "*.go"         # Find all Go files
  %s example . --exclude "vendor/"      # Exclude vendor directory
  %s example /tmp --max-depth 2         # Limit traversal depth`, binaryName, binaryName, binaryName, binaryName),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Use provided path or default to current directory
			if len(args) > 0 {
				root = args[0]
			}
			if root == "" {
				root = "."
			}

			// Convert to absolute path for clarity
			absRoot, err := filepath.Abs(root)
			if err != nil {
				loggerInstance.Error("failed to resolve path", zap.Error(err))
				return fmt.Errorf("invalid path: %w", err)
			}

			loggerInstance.Info("starting file discovery",
				zap.String("root", absRoot),
				zap.Strings("include", include),
				zap.Strings("exclude", exclude),
				zap.Int("max_depth", maxDepth))

			// Build pathfinder query
			query := pathfinder.FindQuery{
				Root:     absRoot,
				Include:  include,
				Exclude:  exclude,
				MaxDepth: maxDepth,
			}

			// Create finder and execute safe filesystem traversal
			// This uses gofulmen's pathfinder (which internally uses doublestar for patterns)
			finder := pathfinder.NewFinder()
			results, err := finder.FindFiles(context.Background(), query)
			if err != nil {
				loggerInstance.Error("pathfinder traversal failed", zap.Error(err))
				return fmt.Errorf("failed to traverse filesystem: %w", err)
			}

			// Output results to stdout (data) while logs go to stderr
			fmt.Printf("Found %d file(s):\n", len(results))
			for _, result := range results {
				// PathResult has multiple path representations:
				// - RelativePath: relative to root
				// - SourcePath: absolute path
				// - LogicalPath: logical mapping path
				fmt.Printf("  %s\n", result.RelativePath)
			}

			loggerInstance.Info("file discovery complete",
				zap.Int("total_files", len(results)))

			return nil
		},
	}

	// Pathfinder-aligned flags
	cmd.Flags().StringVarP(&root, "root", "r", ".", "root directory to search")
	cmd.Flags().StringSliceVarP(&include, "include", "i", []string{}, "patterns to include (e.g., *.go,*.md)")
	cmd.Flags().StringSliceVarP(&exclude, "exclude", "e", []string{".git/", "vendor/", "node_modules/"}, "patterns to exclude")
	cmd.Flags().IntVarP(&maxDepth, "max-depth", "d", 10, "maximum traversal depth (0 = unlimited)")

	return cmd
}
