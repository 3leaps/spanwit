// Package coverageattestation implements Spanwit's producer projection onto
// Crucible's canonical coverage-attestation/v0 contract.
package coverageattestation

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

const (
	// Capability is the exact opaque token required by the canonical manifest.
	Capability = "contract: coverage-attestation/v0"

	embeddedManifestPath = "contract/contract.json"
)

//go:embed contract/*.json
var embeddedContract embed.FS

type manifest struct {
	Capability  string `json:"capability"`
	EntrySchema string `json:"entry_schema"`
}

// CanonicalSchema resolves the vendored contract through its manifest.
func CanonicalSchema() ([]byte, error) {
	return ResolveSchema(embeddedContract, embeddedManifestPath)
}

// ResolveSchema applies Crucible's L2 contract-entry rules: consumers resolve
// contract.json, require the exact capability, and then load its relative
// entry_schema. Direct schema $id lookup is deliberately insufficient.
func ResolveSchema(contractFS fs.FS, manifestPath string) ([]byte, error) {
	if contractFS == nil {
		return nil, errors.New("coverage-attestation contract filesystem is required")
	}
	rawManifestPath := strings.TrimSpace(manifestPath)
	if rawManifestPath == "" || path.IsAbs(rawManifestPath) ||
		strings.Contains(rawManifestPath, `\`) {
		return nil, errors.New("coverage-attestation manifest path must be relative")
	}
	for _, segment := range strings.Split(rawManifestPath, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return nil, errors.New(
				"coverage-attestation manifest path contains an unsafe segment")
		}
	}
	manifestPath = path.Clean(rawManifestPath)
	manifestData, err := fs.ReadFile(contractFS, manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read coverage-attestation manifest: %w", err)
	}

	var entry manifest
	decoder := json.NewDecoder(bytes.NewReader(manifestData))
	if err := decoder.Decode(&entry); err != nil {
		return nil, fmt.Errorf("decode coverage-attestation manifest: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return nil, fmt.Errorf("decode coverage-attestation manifest: %w", err)
	}
	if entry.Capability != Capability {
		return nil, fmt.Errorf(
			"coverage-attestation capability mismatch: got %q, require %q",
			entry.Capability, Capability)
	}

	schemaName := strings.TrimSpace(entry.EntrySchema)
	if schemaName == "" || path.IsAbs(schemaName) ||
		strings.Contains(schemaName, "/") || strings.Contains(schemaName, `\`) ||
		schemaName == "." || schemaName == ".." {
		return nil, errors.New(
			"coverage-attestation entry_schema must be a file relative to the manifest directory")
	}
	schemaPath := path.Join(path.Dir(manifestPath), schemaName)
	schemaData, err := fs.ReadFile(contractFS, schemaPath)
	if err != nil {
		return nil, fmt.Errorf("read coverage-attestation entry schema: %w", err)
	}
	if !json.Valid(schemaData) {
		return nil, errors.New("coverage-attestation entry schema is not valid JSON")
	}
	var advertised struct {
		Properties struct {
			Capabilities struct {
				Contains struct {
					Const string `json:"const"`
				} `json:"contains"`
			} `json:"capabilities"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaData, &advertised); err != nil {
		return nil, fmt.Errorf("decode coverage-attestation entry schema: %w", err)
	}
	if advertised.Properties.Capabilities.Contains.Const != entry.Capability {
		return nil, fmt.Errorf(
			"coverage-attestation schema capability mismatch: manifest has %q, schema advertises %q",
			entry.Capability, advertised.Properties.Capabilities.Contains.Const)
	}
	return schemaData, nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return err
}
