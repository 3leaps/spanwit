package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/foundry"
	"github.com/fulmenhq/gofulmen/schema"
)

// newValidateCmd creates the validate command
// This demonstrates OPTIONAL schema validation for microtools that process structured data
func newValidateCmd(identity *appidentity.Identity) *cobra.Command {
	var (
		schemaPath string
		dataPath   string
		metaOnly   bool
	)

	binaryName := "tool"
	if identity != nil && identity.BinaryName != "" {
		binaryName = identity.BinaryName
	}

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate schema and data (demonstrates schema validation pattern)",
		Long: fmt.Sprintf(`Demonstrates schema validation for microtools that process structured data.

This command shows two types of validation:
1. Meta-validation: Verify a schema is valid against JSON Schema Draft-07/2020-12
2. Data validation: Verify data conforms to a schema

Examples:
  # Validate that a schema file is valid JSON Schema
  %s validate --schema config.schema.json --meta-only

  # Validate data against a schema
  %s validate --schema config.schema.json --data config.yaml

  # Use the example fixtures
  %s validate \
    --schema tests/fixtures/schema-validation/config.schema.json \
    --data tests/fixtures/schema-validation/valid-config.yaml`, binaryName, binaryName, binaryName),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve absolute paths
			absSchemaPath, err := filepath.Abs(schemaPath)
			if err != nil {
				loggerInstance.Error("failed to resolve schema path", zap.Error(err))
				return fmt.Errorf("invalid schema path: %w", err)
			}

			loggerInstance.Info("schema validation starting",
				zap.String("schema", absSchemaPath),
				zap.Bool("meta_only", metaOnly))

			// Step 1: Meta-validation - validate the schema itself
			loggerInstance.Info("Step 1: Meta-validating schema against JSON Schema specification")

			schemaData, err := os.ReadFile(absSchemaPath)
			if err != nil {
				loggerInstance.Error("failed to read schema file", zap.Error(err))
				return fmt.Errorf("cannot read schema: %w", err)
			}

			// Validate schema is valid JSON Schema (Draft-07 or 2020-12)
			// ValidateSchemaBytes is a package-level function
			diags, err := schema.ValidateSchemaBytes(schemaData)
			if err != nil {
				loggerInstance.Error("schema meta-validation failed", zap.Error(err))
				_, _ = fmt.Fprintf(os.Stderr, "\n❌ Schema validation error:\n%v\n\n", err)
				os.Exit(foundry.ExitDataInvalid)
			}

			if len(diags) > 0 {
				loggerInstance.Error("schema meta-validation failed with diagnostics",
					zap.Int("diagnostic_count", len(diags)))
				_, _ = fmt.Fprintf(os.Stderr, "\n❌ Schema is not valid JSON Schema:\n")
				for _, diag := range diags {
					_, _ = fmt.Fprintf(os.Stderr, "  - %s: %s\n", diag.Pointer, diag.Message)
				}
				_, _ = fmt.Fprintln(os.Stderr)
				os.Exit(foundry.ExitDataInvalid)
			}

			loggerInstance.Info("✓ Schema meta-validation passed - schema is valid JSON Schema")
			fmt.Println("✓ Schema meta-validation passed")

			// If meta-only, we're done
			if metaOnly {
				loggerInstance.Info("meta-validation complete (--meta-only flag set)")
				return nil
			}

			// Step 2: Data validation - validate data against the schema
			if dataPath == "" {
				loggerInstance.Warn("no data file specified, skipping data validation")
				fmt.Println("\n⚠️  No data file specified. Use --data to validate data against schema.")
				return nil
			}

			absDataPath, err := filepath.Abs(dataPath)
			if err != nil {
				loggerInstance.Error("failed to resolve data path", zap.Error(err))
				return fmt.Errorf("invalid data path: %w", err)
			}

			loggerInstance.Info("Step 2: Validating data against schema",
				zap.String("data", absDataPath))

			// Load data file (handles both YAML and JSON)
			var dataBytes []byte
			if filepath.Ext(absDataPath) == ".yaml" || filepath.Ext(absDataPath) == ".yml" {
				// Use LoadYAMLFile which converts YAML to JSON
				dataBytes, err = schema.LoadYAMLFile(absDataPath)
				if err != nil {
					loggerInstance.Error("failed to load YAML data file", zap.Error(err))
					return fmt.Errorf("cannot load YAML file: %w", err)
				}
			} else {
				// Load JSON file directly
				dataBytes, err = schema.LoadJSONFile(absDataPath)
				if err != nil {
					loggerInstance.Error("failed to load JSON data file", zap.Error(err))
					return fmt.Errorf("cannot load JSON file: %w", err)
				}
			}

			// Create validator from schema
			validator, err := schema.NewValidator(schemaData)
			if err != nil {
				loggerInstance.Error("failed to create validator from schema", zap.Error(err))
				return fmt.Errorf("cannot create validator: %w", err)
			}

			// Validate data against schema
			// Note: dataBytes is now JSON (converted from YAML if needed)
			diags, err = validator.ValidateJSON(dataBytes)
			if err != nil {
				loggerInstance.Error("data validation failed", zap.Error(err))
				_, _ = fmt.Fprintf(os.Stderr, "\n❌ Data validation error:\n%v\n\n", err)
				os.Exit(foundry.ExitDataInvalid)
			}

			if len(diags) > 0 {
				loggerInstance.Error("data validation failed with diagnostics",
					zap.Int("diagnostic_count", len(diags)))
				_, _ = fmt.Fprintf(os.Stderr, "\n❌ Data validation failed:\n")
				for _, diag := range diags {
					_, _ = fmt.Fprintf(os.Stderr, "  - %s: %s\n", diag.Pointer, diag.Message)
				}
				_, _ = fmt.Fprintln(os.Stderr)

				// Show helpful context
				_, _ = fmt.Fprintf(os.Stderr, "Schema: %s\n", absSchemaPath)
				_, _ = fmt.Fprintf(os.Stderr, "Data:   %s\n\n", absDataPath)

				os.Exit(foundry.ExitDataInvalid)
			}

			loggerInstance.Info("✓ Data validation passed - data conforms to schema")
			fmt.Println("✓ Data validation passed")

			// Success summary
			fmt.Printf("\n✅ Validation complete\n")
			fmt.Printf("   Schema: %s\n", filepath.Base(absSchemaPath))
			fmt.Printf("   Data:   %s\n", filepath.Base(absDataPath))

			return nil
		},
	}

	// Flags
	cmd.Flags().StringVarP(&schemaPath, "schema", "s", "", "path to JSON Schema file (required)")
	cmd.Flags().StringVarP(&dataPath, "data", "d", "", "path to data file (YAML or JSON) to validate")
	cmd.Flags().BoolVarP(&metaOnly, "meta-only", "m", false, "only validate schema itself (meta-validation)")

	_ = cmd.MarkFlagRequired("schema") // Error only occurs if flag doesn't exist

	return cmd
}
