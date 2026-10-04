package engine

import (
	"context"
	"path/filepath"
	"testing"
)

// Config-discovery candidates must carry a non-inferred authorization boundary
// (the enabled profile root) and provenance so execute can guard them.
func TestBuildPrunePlan_CarriesConfigProfileProvenance(t *testing.T) {
	tmp := newCargoFixture(t)
	plan, err := BuildPrunePlan(context.Background(), cargoConfig(tmp, nil))
	if err != nil {
		t.Fatal(err)
	}
	c := cargoTargetCandidate(t, plan)
	if c.ProvenanceKind != ProvenanceConfigProfile {
		t.Fatalf("provenance=%q want %q", c.ProvenanceKind, ProvenanceConfigProfile)
	}
	if c.ContainmentRelation != ContainmentDescendant {
		t.Fatalf("relation=%q want %q", c.ContainmentRelation, ContainmentDescendant)
	}
	if c.AuthorizationBoundary != filepath.Clean(tmp) {
		t.Fatalf("boundary=%q want %q", c.AuthorizationBoundary, filepath.Clean(tmp))
	}
	// The candidate must be a strict descendant that the guard authorizes.
	if _, err := AuthorizeDeletion(DeletionRequest{
		Target:     c.Path,
		Boundary:   c.AuthorizationBoundary,
		Relation:   c.ContainmentRelation,
		Provenance: c.ProvenanceKind,
	}); err != nil {
		t.Fatalf("guard should authorize a config-discovery candidate: %v", err)
	}
}
