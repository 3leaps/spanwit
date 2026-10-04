package coverageattestation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/inventory"
)

func TestBuildCompleteAndPartialAttestations(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		evidence := completeEvidence()
		document, err := Build(evidence, deterministicBuildOptions())
		if err != nil {
			t.Fatal(err)
		}
		if document.CoverageState != "complete" || len(document.Claims) != 4 ||
			len(document.Gaps) != 0 {
			t.Fatalf("document=%+v", document)
		}
		payload, err := MarshalValidated(document)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), "/patient") ||
			strings.Contains(string(payload), "secret/project.bin") {
			t.Fatalf("attestation leaked protected path:\n%s", payload)
		}
	})

	t.Run("partial with opaque prefix", func(t *testing.T) {
		evidence := completeEvidence()
		evidence.Header.Roots[0].Path = "/patient"
		evidence.Summary.Lifecycle = inventory.LifecyclePartial
		evidence.Summary.GapCount = 1
		evidence.Summary.Roots[0].Lifecycle = inventory.LifecyclePartial
		evidence.Summary.Roots[0].GapCount = 1
		evidence.Gaps = []inventory.Gap{{
			RootID: "root-1", RelativePath: "secret/project.bin",
			LocalAbsolutePath: "/patient/secret/project.bin",
			Kind:              "permission", Detail: "permission denied",
			AffectsCompleteness: true,
		}}
		document, err := Build(evidence, deterministicBuildOptions())
		if err != nil {
			t.Fatal(err)
		}
		if document.CoverageState != "partial" || len(document.Gaps) != 1 {
			t.Fatalf("document=%+v", document)
		}
		if !strings.HasPrefix(document.Gaps[0].Scope.Prefix, "opaque:hmac-sha256:") {
			t.Fatalf("prefix=%q", document.Gaps[0].Scope.Prefix)
		}
	})
}

func TestBuildGapTokensArePerDocumentOpaqueAndStableWithinDocument(t *testing.T) {
	evidence := completeEvidence()
	evidence.Header.Roots[0].Path = "/patient/private"
	evidence.Summary.Lifecycle = inventory.LifecyclePartial
	evidence.Summary.GapCount = 3
	evidence.Summary.Roots[0].Lifecycle = inventory.LifecyclePartial
	evidence.Summary.Roots[0].GapCount = 3
	evidence.Gaps = []inventory.Gap{
		{RootID: "root-1", RelativePath: "secret/report.csv", LocalAbsolutePath: "/patient/private/secret/report.csv", Kind: "permission", AffectsCompleteness: true},
		{RootID: "root-1", RelativePath: "secret/report.csv", LocalAbsolutePath: "/patient/private/secret/report.csv", Kind: "permission", AffectsCompleteness: true},
		{RootID: "root-1", RelativePath: "secret/other.csv", LocalAbsolutePath: "/patient/private/secret/other.csv", Kind: "vanished", AffectsCompleteness: true},
	}

	first, err := Build(evidence, deterministicBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(evidence, deterministicBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	firstTokens := []string{first.Gaps[0].Scope.Prefix, first.Gaps[1].Scope.Prefix, first.Gaps[2].Scope.Prefix}
	if firstTokens[0] != firstTokens[1] {
		t.Fatalf("same protected scope received unstable tokens: %v", firstTokens)
	}
	if firstTokens[0] == firstTokens[2] {
		t.Fatalf("distinct protected scopes received the same token: %v", firstTokens)
	}
	if firstTokens[0] == second.Gaps[0].Scope.Prefix {
		t.Fatalf("token was reproducible across documents: first=%q second=%q", firstTokens[0], second.Gaps[0].Scope.Prefix)
	}
	payload, err := MarshalValidated(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, protected := range []string{"/patient/private", "secret/report.csv", "secret/other.csv"} {
		if strings.Contains(string(payload), protected) {
			t.Fatalf("attestation leaked protected value %q:\n%s", protected, payload)
		}
	}
}

func TestBuildTrustworthyFailedInventoryProjectsPartialAttestation(t *testing.T) {
	evidence := completeEvidence()
	evidence.Summary.Lifecycle = inventory.LifecycleFailed
	evidence.Summary.Roots[0].Lifecycle = inventory.LifecycleFailed
	evidence.Summary.Roots[0].Failed = true
	document, err := Build(evidence, deterministicBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	if document.CoverageState != coveragePartial {
		t.Fatalf("coverage_state=%q want partial", document.CoverageState)
	}
}

func TestBuildRejectsSemanticMismatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Evidence)
	}{
		{
			name: "global root mismatch",
			mutate: func(e *Evidence) {
				e.Summary.VisitedEntries++
			},
		},
		{
			name: "gap count mismatch",
			mutate: func(e *Evidence) {
				e.Summary.Lifecycle = inventory.LifecyclePartial
				e.Summary.GapCount = 1
				e.Summary.Roots[0].Lifecycle = inventory.LifecyclePartial
				e.Summary.Roots[0].GapCount = 1
			},
		},
		{
			name: "complete root with failure",
			mutate: func(e *Evidence) {
				e.Summary.Roots[0].Failed = true
			},
		},
		{
			name: "summary-only emitted",
			mutate: func(e *Evidence) {
				e.Summary.EmissionMode = inventory.EmissionSummaryOnly
				e.Summary.EntriesSuppressed = true
			},
		},
		{
			name: "per-root gap mismatch",
			mutate: func(e *Evidence) {
				e.Header.Roots = append(e.Header.Roots,
					inventory.Root{ID: "root-2", Path: "/patient-2"})
				e.Summary.Roots = append(e.Summary.Roots,
					inventory.RootSummary{
						RootID: "root-2", Lifecycle: inventory.LifecyclePartial,
						GapCount: 1,
					})
				e.Summary.Lifecycle = inventory.LifecyclePartial
				e.Summary.GapCount = 1
				e.Gaps = []inventory.Gap{{
					RootID: "root-1", Kind: "permission",
					AffectsCompleteness: true,
				}}
			},
		},
		{
			name: "header summary emission mismatch",
			mutate: func(e *Evidence) {
				e.Header.EmissionMode = inventory.EmissionSummaryOnly
			},
		},
		{
			name: "no trustworthy observation",
			mutate: func(e *Evidence) {
				e.Summary.VisitedEntries = 0
				e.Summary.VisitedDirectories = 0
				e.Summary.MatchedCount = 0
				e.Summary.EmittedCount = 0
				e.Summary.MatchedApparentBytes = 0
				e.Summary.MatchedAllocatedBytes = 0
				e.Summary.Roots[0] = inventory.RootSummary{
					RootID: "root-1", Lifecycle: inventory.LifecycleFailed, Failed: true,
				}
				e.Summary.Lifecycle = inventory.LifecycleFailed
			},
		},
		{
			name: "one root fails before trustworthy observation",
			mutate: func(e *Evidence) {
				e.Header.Roots = append(e.Header.Roots,
					inventory.Root{ID: "root-2", Path: "/patient-2"})
				e.Summary.Roots = append(e.Summary.Roots,
					inventory.RootSummary{
						RootID: "root-2", Lifecycle: inventory.LifecycleFailed,
						Failed: true,
					})
				e.Summary.Lifecycle = inventory.LifecycleFailed
			},
		},
		{
			name: "zero terminal observation timestamp",
			mutate: func(e *Evidence) {
				e.Summary.TerminalObservedAt = time.Time{}
			},
		},
		{
			name: "partial root without partial evidence",
			mutate: func(e *Evidence) {
				e.Summary.Lifecycle = inventory.LifecyclePartial
				e.Summary.Roots[0].Lifecycle = inventory.LifecyclePartial
			},
		},
		{
			name: "failed root without failed flag",
			mutate: func(e *Evidence) {
				e.Summary.Lifecycle = inventory.LifecycleFailed
				e.Summary.Roots[0].Lifecycle = inventory.LifecycleFailed
			},
		},
		{
			name: "canceled root without canceled gap",
			mutate: func(e *Evidence) {
				makePartialEvidence(e, "permission")
				e.Summary.Canceled = true
				e.Summary.Roots[0].Canceled = true
			},
		},
		{
			name: "partial global lifecycle with failed root",
			mutate: func(e *Evidence) {
				e.Summary.Lifecycle = inventory.LifecyclePartial
				e.Summary.Roots[0].Lifecycle = inventory.LifecycleFailed
				e.Summary.Roots[0].Failed = true
			},
		},
		{
			name: "cancellation does not reconcile to roots",
			mutate: func(e *Evidence) {
				makePartialEvidence(e, "canceled")
				e.Summary.Canceled = false
				e.Summary.Roots[0].Canceled = true
			},
		},
		{
			name: "complete global lifecycle with partial root",
			mutate: func(e *Evidence) {
				makePartialEvidence(e, "permission")
				e.Summary.Lifecycle = inventory.LifecycleComplete
			},
		},
		{
			name: "selection truncation mismatch",
			mutate: func(e *Evidence) {
				e.Header.Top = 1
				e.Summary.Top = 1
				e.Summary.SelectionTruncated = true
			},
		},
		{
			name: "entries emission count does not reconcile to top",
			mutate: func(e *Evidence) {
				e.Header.Top = 1
				e.Summary.Top = 1
				e.Summary.EmittedCount = 0
				e.Summary.Roots[0].EmittedCount = 0
			},
		},
		{
			name: "entries mode suppresses entries",
			mutate: func(e *Evidence) {
				e.Summary.EntriesSuppressed = true
			},
		},
		{
			name: "summary only retains emitted bytes",
			mutate: func(e *Evidence) {
				e.Header.EmissionMode = inventory.EmissionSummaryOnly
				e.Summary.EmissionMode = inventory.EmissionSummaryOnly
				e.Summary.EntriesSuppressed = true
				e.Summary.EmittedCount = 0
				e.Summary.EmittedApparentBytes = 1
				e.Summary.Roots[0].EmittedCount = 0
				e.Summary.Roots[0].EmittedApparentBytes = 1
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence := completeEvidence()
			tt.mutate(&evidence)
			if _, err := Build(evidence, deterministicBuildOptions()); err == nil {
				t.Fatal("expected semantic rejection")
			}
		})
	}
}

func makePartialEvidence(e *Evidence, kind string) {
	e.Summary.Lifecycle = inventory.LifecyclePartial
	e.Summary.GapCount = 1
	e.Summary.Roots[0].Lifecycle = inventory.LifecyclePartial
	e.Summary.Roots[0].GapCount = 1
	e.Gaps = []inventory.Gap{{
		RootID: "root-1", RelativePath: "protected", Kind: kind,
		AffectsCompleteness: true,
	}}
}

func TestCollectorIsBoundedAndForwards(t *testing.T) {
	sink := &recordingSink{}
	collector, err := NewCollector(sink, 1)
	if err != nil {
		t.Fatal(err)
	}
	evidence := completeEvidence()
	evidence.Header.Roots[0].Path = t.TempDir()
	if err := collector.Header(evidence.Header); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := collector.Gap(inventory.Gap{
			RootID: "root-1", Kind: "permission", Detail: "permission denied",
			AffectsCompleteness: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := collector.Summary(evidence.Summary); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Evidence(); err == nil ||
		!strings.Contains(err.Error(), "gap limit exceeded") {
		t.Fatalf("expected bounded overflow, got %v", err)
	}
	if sink.gaps != 2 {
		t.Fatalf("downstream gaps=%d want 2", sink.gaps)
	}
}

func completeEvidence() Evidence {
	root := inventory.RootSummary{
		RootID: "root-1", Lifecycle: inventory.LifecycleComplete,
		VisitedEntries: 2, VisitedDirectories: 1,
		MatchedCount: 1, EmittedCount: 1,
		MatchedApparentBytes: 10, MatchedAllocatedBytes: 8,
	}
	return Evidence{
		Header: inventory.Header{
			RunID: "run-1", Profile: inventory.ProfileV0,
			Roots:          []inventory.Root{{ID: "root-1", Path: "/patient"}},
			EmissionMode:   inventory.EmissionEntries,
			PathProtection: inventory.PathProtectionSourceStructure,
		},
		Summary: inventory.Summary{
			Lifecycle:      inventory.LifecycleComplete,
			VisitedEntries: 2, VisitedDirectories: 1,
			MatchedCount: 1, EmittedCount: 1,
			MatchedApparentBytes: 10, MatchedAllocatedBytes: 8,
			TerminalObservedAt: time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC),
			EmissionMode:       inventory.EmissionEntries,
			Roots:              []inventory.RootSummary{root},
		},
	}
}

func deterministicBuildOptions() BuildOptions {
	return BuildOptions{
		EmitterName: "spanwit", EmitterVersion: "test",
		AttestationID: "urn:uuid:2f6d1c9a-8f4e-4b0a-9c3d-5e7a1b2c4d6e",
	}
}

type recordingSink struct {
	gaps int
}

func (*recordingSink) Header(inventory.Header) error { return nil }
func (*recordingSink) Entry(inventory.Entry) error   { return nil }
func (s *recordingSink) Gap(inventory.Gap) error {
	s.gaps++
	return nil
}
func (*recordingSink) Summary(inventory.Summary) error { return nil }

func TestAttestationDocumentHasNoUnknownFields(t *testing.T) {
	document, err := Build(completeEvidence(), deterministicBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := MarshalValidated(document)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["coverage_state"] != "complete" {
		t.Fatalf("decoded=%v", decoded)
	}
}
