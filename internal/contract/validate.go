// Package contract is a thin adapter over gofulmen/schema: it compiles an
// embedded JSON Schema and validates JSON or YAML payloads, collapsing
// gofulmen diagnostics into a single error.
package contract

import (
	"fmt"
	"strings"

	gofulmenschema "github.com/fulmenhq/gofulmen/schema"
	"gopkg.in/yaml.v3"
)

// ValidateJSON validates JSON bytes against a JSON Schema document.
func ValidateJSON(schemaData []byte, jsonData []byte) error {
	validator, err := gofulmenschema.NewValidator(schemaData)
	if err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}
	diags, err := validator.ValidateJSON(jsonData)
	if err != nil {
		return err
	}
	return diagnosticsError(diags)
}

// ValidateYAML validates YAML bytes against a JSON Schema document. The YAML is
// decoded to a Go value and validated directly — gofulmen/schema validates
// decoded data, so no JSON round-trip is needed.
func ValidateYAML(schemaData []byte, yamlData []byte) error {
	var payload any
	if err := yaml.Unmarshal(yamlData, &payload); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	validator, err := gofulmenschema.NewValidator(schemaData)
	if err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}
	diags, err := validator.ValidateData(payload)
	if err != nil {
		return err
	}
	return diagnosticsError(diags)
}

func diagnosticsError(diags []gofulmenschema.Diagnostic) error {
	if len(diags) == 0 {
		return nil
	}
	return fmt.Errorf("schema validation failed: %s", strings.Join(gofulmenschema.DiagnosticsToStringSlice(diags), "; "))
}
