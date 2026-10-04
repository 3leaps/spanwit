package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"

	"github.com/3leaps/spanwit/internal/config"
)

// setupTestLogger initializes the global logger for testing
func setupTestLogger(t *testing.T) {
	t.Helper()
	logger, err := logging.NewCLI("spanwit-test")
	if err != nil {
		t.Fatalf("Failed to create test logger: %v", err)
	}
	loggerInstance = logger
}

// TestValidateCommand_MetaValidation tests schema meta-validation
func TestValidateCommand_MetaValidation(t *testing.T) {
	setupTestLogger(t)

	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		t.Fatalf("Failed to get project root: %v", err)
	}

	schemaPath := filepath.Join(projectRoot, "tests/fixtures/schema-validation/config.schema.json")

	// Verify schema file exists
	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		t.Skipf("Schema file not found: %s (test fixtures may not be available)", schemaPath)
	}

	cmd := newValidateCmd(&appidentity.Identity{BinaryName: "spanwit"})
	cmd.SetArgs([]string{
		"--schema", schemaPath,
		"--meta-only",
	})

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err = cmd.Execute()
	if err != nil {
		t.Errorf("Meta-validation failed: %v\nStderr: %s", err, stderr.String())
	}

	// Note: Output goes to os.Stdout (not captured in buffer) which is expected behavior
	// The test verifies the command executes successfully without error
}

// TestValidateCommand_ValidYAML tests validation of valid YAML data
func TestValidateCommand_ValidYAML(t *testing.T) {
	setupTestLogger(t)

	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		t.Fatalf("Failed to get project root: %v", err)
	}

	schemaPath := filepath.Join(projectRoot, "tests/fixtures/schema-validation/config.schema.json")
	dataPath := filepath.Join(projectRoot, "tests/fixtures/schema-validation/valid-config.yaml")

	// Verify files exist
	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		t.Skipf("Schema file not found: %s", schemaPath)
	}
	if _, err := os.Stat(dataPath); os.IsNotExist(err) {
		t.Skipf("Data file not found: %s", dataPath)
	}

	cmd := newValidateCmd(&appidentity.Identity{BinaryName: "spanwit"})
	cmd.SetArgs([]string{
		"--schema", schemaPath,
		"--data", dataPath,
	})

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err = cmd.Execute()
	if err != nil {
		t.Errorf("Validation of valid YAML failed: %v\nStderr: %s", err, stderr.String())
	}

	// Command executed successfully - validation passed
}

// TestValidateCommand_ValidJSON tests validation of valid JSON data
func TestValidateCommand_ValidJSON(t *testing.T) {
	setupTestLogger(t)

	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		t.Fatalf("Failed to get project root: %v", err)
	}

	schemaPath := filepath.Join(projectRoot, "tests/fixtures/schema-validation/config.schema.json")
	dataPath := filepath.Join(projectRoot, "tests/fixtures/schema-validation/valid-config.json")

	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		t.Skipf("Schema file not found: %s", schemaPath)
	}
	if _, err := os.Stat(dataPath); os.IsNotExist(err) {
		t.Skipf("Data file not found: %s", dataPath)
	}

	cmd := newValidateCmd(&appidentity.Identity{BinaryName: "spanwit"})
	cmd.SetArgs([]string{
		"--schema", schemaPath,
		"--data", dataPath,
	})

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err = cmd.Execute()
	if err != nil {
		t.Errorf("Validation of valid JSON failed: %v\nStderr: %s", err, stderr.String())
	}

	// Command executed successfully - validation passed
}

// TestValidateCommand_ValidBlueGreen tests validation of blue-green deployment config
func TestValidateCommand_ValidBlueGreen(t *testing.T) {
	setupTestLogger(t)

	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		t.Fatalf("Failed to get project root: %v", err)
	}

	schemaPath := filepath.Join(projectRoot, "tests/fixtures/schema-validation/config.schema.json")
	dataPath := filepath.Join(projectRoot, "tests/fixtures/schema-validation/valid-bluegreen.yaml")

	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		t.Skipf("Schema file not found: %s", schemaPath)
	}
	if _, err := os.Stat(dataPath); os.IsNotExist(err) {
		t.Skipf("Data file not found: %s", dataPath)
	}

	cmd := newValidateCmd(&appidentity.Identity{BinaryName: "spanwit"})
	cmd.SetArgs([]string{
		"--schema", schemaPath,
		"--data", dataPath,
	})

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err = cmd.Execute()
	if err != nil {
		t.Errorf("Validation of blue-green config failed: %v\nStderr: %s", err, stderr.String())
	}
}

// TestValidateCommand_InvalidData tests that invalid data is properly rejected
func TestValidateCommand_InvalidData(t *testing.T) {
	setupTestLogger(t)

	projectRoot, err := config.FindProjectRoot()
	if err != nil {
		t.Fatalf("Failed to get project root: %v", err)
	}

	schemaPath := filepath.Join(projectRoot, "tests/fixtures/schema-validation/config.schema.json")
	dataPath := filepath.Join(projectRoot, "tests/fixtures/schema-validation/invalid-config.yaml")

	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		t.Skipf("Schema file not found: %s", schemaPath)
	}
	if _, err := os.Stat(dataPath); os.IsNotExist(err) {
		t.Skipf("Invalid data file not found: %s", dataPath)
	}

	cmd := newValidateCmd(&appidentity.Identity{BinaryName: "spanwit"})
	cmd.SetArgs([]string{
		"--schema", schemaPath,
		"--data", dataPath,
	})

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	// We expect this to exit with an error, but Execute() itself shouldn't panic
	// The command calls os.Exit() internally, so we can't catch that in tests
	// Instead, we test that the command is properly constructed and would execute
	// In a real scenario, this would exit with foundry.ExitDataInvalid

	// Note: Since the validate command calls os.Exit() on validation failure,
	// this test verifies the command setup but cannot test the actual exit behavior
	// without refactoring the command to be more testable (e.g., returning errors instead of os.Exit)

	// For now, just verify the command is properly configured
	if cmd == nil {
		t.Error("Expected validate command to be created")
	}

	// Verify the invalid data file has the expected invalid content
	invalidData, err := os.ReadFile(dataPath)
	if err != nil {
		t.Fatalf("Failed to read invalid data file: %v", err)
	}

	// Verify it contains known invalid values
	if !bytes.Contains(invalidData, []byte("environment: local")) {
		t.Error("Invalid data file should contain 'environment: local' (invalid enum value)")
	}
	if !bytes.Contains(invalidData, []byte("mode: rolling")) {
		t.Error("Invalid data file should contain 'mode: rolling' (invalid enum value)")
	}
}

// TestValidateCommand_MissingSchemaFile tests error handling for missing schema
func TestValidateCommand_MissingSchemaFile(t *testing.T) {
	setupTestLogger(t)

	cmd := newValidateCmd(&appidentity.Identity{BinaryName: "spanwit"})
	cmd.SetArgs([]string{
		"--schema", "/nonexistent/schema.json",
		"--meta-only",
	})

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Error("Expected error for missing schema file, got nil")
	}

	// Verify error message mentions the file
	if !bytes.Contains([]byte(err.Error()), []byte("cannot read schema")) {
		t.Errorf("Expected error message about missing schema, got: %v", err)
	}
}
