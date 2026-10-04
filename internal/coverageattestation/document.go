package coverageattestation

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/3leaps/spanwit/internal/contract"
)

// Document is the canonical coverage-attestation/v0 entry shape.
type Document struct {
	Capabilities  []string   `json:"capabilities"`
	AttestationID string     `json:"attestation_id"`
	Supersedes    string     `json:"supersedes,omitempty"`
	Subject       Subject    `json:"subject"`
	Emitter       Emitter    `json:"emitter"`
	AsOf          string     `json:"as_of"`
	CoverageState string     `json:"coverage_state"`
	Claims        []Claim    `json:"claims"`
	Gaps          []Gap      `json:"gaps,omitempty"`
	Protection    Protection `json:"protection"`
}

type Subject struct {
	ArtifactID string `json:"artifact_id,omitempty"`
	GrainID    string `json:"grain_id,omitempty"`
	SubjectURI string `json:"subject_uri,omitempty"`
}

type Emitter struct {
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	RunID    string `json:"run_id,omitempty"`
	Relation string `json:"relation"`
}

type Claim struct {
	Scope  Scope   `json:"scope"`
	Basis  string  `json:"basis"`
	Method string  `json:"method"`
	Volume *Volume `json:"volume,omitempty"`
	Note   string  `json:"note,omitempty"`
}

type Volume struct {
	Unit     string `json:"unit"`
	Observed int64  `json:"observed"`
	Expected *int64 `json:"expected,omitempty"`
}

type Gap struct {
	Code  string `json:"code"`
	Scope Scope  `json:"scope"`
	Note  string `json:"note,omitempty"`
}

type Scope struct {
	Partition map[string]string `json:"partition,omitempty"`
	Window    *Window           `json:"window,omitempty"`
	Prefix    string            `json:"prefix,omitempty"`
}

type Window struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type Protection struct {
	DefaultAction string `json:"default_action"`
}

// MarshalValidated constructs the complete JSON representation in memory and
// validates it through the canonical manifest-resolved entry schema.
func MarshalValidated(document Document) ([]byte, error) {
	schemaData, err := CanonicalSchema()
	if err != nil {
		return nil, err
	}
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal coverage attestation: %w", err)
	}
	if err := contract.ValidateJSON(schemaData, payload); err != nil {
		return nil, fmt.Errorf("validate coverage attestation: %w", err)
	}
	return append(payload, '\n'), nil
}

// DecodeValidated rejects malformed or non-canonical documents.
func DecodeValidated(payload []byte) (Document, error) {
	schemaData, err := CanonicalSchema()
	if err != nil {
		return Document{}, err
	}
	if err := contract.ValidateJSON(schemaData, payload); err != nil {
		return Document{}, fmt.Errorf("validate coverage attestation: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("decode coverage attestation: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return Document{}, fmt.Errorf("decode coverage attestation: %w", err)
	}
	return document, nil
}
