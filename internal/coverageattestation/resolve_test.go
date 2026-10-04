package coverageattestation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
)

func TestCanonicalSchemaResolvesThroughManifest(t *testing.T) {
	schema, err := CanonicalSchema()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schema), `"minimum": 0`) ||
		!strings.Contains(string(schema), `"required": ["code", "scope"]`) {
		t.Fatal("vendored schema lacks canonical gap-code or non-negative-volume controls")
	}
}

func TestVendoredContractMatchesRecordedProvenance(t *testing.T) {
	var source struct {
		SourceCommit string            `json:"source_commit"`
		Files        map[string]string `json:"files"`
	}
	sourceData, err := embeddedContract.ReadFile("contract/SOURCE.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(sourceData, &source); err != nil {
		t.Fatal(err)
	}
	if source.SourceCommit != "14db609d7117a2910a2d0e5e3a868d24f27f490d" {
		t.Fatalf("unexpected canonical source commit %q", source.SourceCommit)
	}
	for name, expected := range source.Files {
		data, err := embeddedContract.ReadFile("contract/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		actual := "sha256:" + hex.EncodeToString(sum[:])
		if actual != expected {
			t.Fatalf("%s provenance mismatch: got %s want %s", name, actual, expected)
		}
	}
}

func TestResolveSchemaFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		schema   string
	}{
		{
			name:     "capability mismatch",
			manifest: `{"capability":"contract: local/v0","entry_schema":"entry.json"}`,
			schema:   `{}`,
		},
		{
			name:     "schema traversal",
			manifest: `{"capability":"contract: coverage-attestation/v0","entry_schema":"../entry.json"}`,
			schema:   `{}`,
		},
		{
			name:     "schema backslash",
			manifest: `{"capability":"contract: coverage-attestation/v0","entry_schema":"..\\entry.json"}`,
			schema:   `{}`,
		},
		{
			name:     "schema subdirectory",
			manifest: `{"capability":"contract: coverage-attestation/v0","entry_schema":"sub/entry.json"}`,
			schema:   `{}`,
		},
		{
			name:     "invalid schema json",
			manifest: `{"capability":"contract: coverage-attestation/v0","entry_schema":"entry.json"}`,
			schema:   `{`,
		},
		{
			name:     "trailing manifest json",
			manifest: `{"capability":"contract: coverage-attestation/v0","entry_schema":"entry.json"} {}`,
			schema:   `{}`,
		},
		{
			name:     "schema capability mismatch",
			manifest: `{"capability":"contract: coverage-attestation/v0","entry_schema":"entry.json"}`,
			schema:   `{"properties":{"capabilities":{"contains":{"const":"contract: local/v0"}}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"contract/contract.json": &fstest.MapFile{Data: []byte(tt.manifest)},
				"contract/entry.json":    &fstest.MapFile{Data: []byte(tt.schema)},
			}
			if _, err := ResolveSchema(fsys, "contract/contract.json"); err == nil {
				t.Fatal("expected fail-closed resolution error")
			}
		})
	}
}

func TestResolveSchemaRejectsAmbiguousManifestPath(t *testing.T) {
	fsys := fstest.MapFS{
		"contract/contract.json": &fstest.MapFile{Data: []byte(
			`{"capability":"contract: coverage-attestation/v0","entry_schema":"entry.json"}`)},
		"contract/entry.json": &fstest.MapFile{Data: []byte(
			`{"properties":{"capabilities":{"contains":{"const":"contract: coverage-attestation/v0"}}}}`)},
	}
	for _, manifestPath := range []string{
		"contract/../contract/contract.json",
		`contract\contract.json`,
		"contract//contract.json",
	} {
		if _, err := ResolveSchema(fsys, manifestPath); err == nil {
			t.Fatalf("expected rejection for %q", manifestPath)
		}
	}
}
