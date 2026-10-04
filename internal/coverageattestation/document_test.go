package coverageattestation

import (
	"strings"
	"testing"
)

func TestMarshalValidatedCanonicalControls(t *testing.T) {
	complete := testDocument()
	payload, err := MarshalValidated(complete)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeValidated(payload); err != nil {
		t.Fatal(err)
	}

	t.Run("gap code required", func(t *testing.T) {
		document := testDocument()
		document.CoverageState = "partial"
		document.Gaps = []Gap{{
			Scope: Scope{Partition: map[string]string{"root_id": "root-1"}},
		}}
		if _, err := MarshalValidated(document); err == nil ||
			!strings.Contains(err.Error(), "schema validation") {
			t.Fatalf("expected canonical gap-code rejection, got %v", err)
		}
	})

	t.Run("negative volume rejected", func(t *testing.T) {
		document := testDocument()
		document.Claims[0].Volume.Observed = -1
		if _, err := MarshalValidated(document); err == nil ||
			!strings.Contains(err.Error(), "schema validation") {
			t.Fatalf("expected canonical volume rejection, got %v", err)
		}
	})

	t.Run("negative expected volume rejected", func(t *testing.T) {
		document := testDocument()
		negative := int64(-1)
		document.Claims[0].Volume.Expected = &negative
		if _, err := MarshalValidated(document); err == nil ||
			!strings.Contains(err.Error(), "schema validation") {
			t.Fatalf("expected canonical expected-volume rejection, got %v", err)
		}
	})

	t.Run("exact capability required", func(t *testing.T) {
		document := testDocument()
		document.Capabilities = []string{"contract:coverage-attestation/v0"}
		if _, err := MarshalValidated(document); err == nil {
			t.Fatal("expected exact capability rejection")
		}
	})
}

func testDocument() Document {
	return Document{
		Capabilities:  []string{Capability},
		AttestationID: "urn:uuid:2f6d1c9a-8f4e-4b0a-9c3d-5e7a1b2c4d6e",
		Subject: Subject{
			SubjectURI: "urn:spanwit:filesystem-inventory:run-1",
		},
		Emitter: Emitter{
			Name: "spanwit", Version: "test", RunID: "run-1", Relation: "producer",
		},
		AsOf:          "2026-08-02T20:00:00Z",
		CoverageState: "complete",
		Claims: []Claim{{
			Scope: Scope{Partition: map[string]string{"root_id": "root-1"}},
			Basis: "confirmed", Method: "enumerated",
			Volume: &Volume{Unit: "entries_visited", Observed: 2},
		}},
		Protection: Protection{DefaultAction: "block_export"},
	}
}
