package config

import (
	"reflect"
	"testing"
)

func builtIn(t *testing.T, id string) Signature {
	t.Helper()
	sig, ok := BuiltInSignatures().Lookup(id)
	if !ok {
		t.Fatalf("missing built-in %s", id)
	}
	return sig
}

// A user signature that reuses a built-in id keeps the built-in's evidence
// and age floor: it can tighten the rule, never loosen it.
func TestMergeKeepsBuiltInSafetyOnReusedID(t *testing.T) {
	user := SignatureCatalog{
		"development": {
			"security": {"grype-db-download": {
				CandidatePatterns: []string{"**/grype/db/grype-db-download*"},
				AnyChildPaths:     []string{"anything"},
				SafeToPrune:       true,
				// no sibling evidence, no floor
			}},
			"go": {"repo-local-gocache": {
				CandidatePatterns: []string{"**/.gocache"},
				SafeToPrune:       true,
				// no go.mod ancestor, no content marker
			}},
		},
	}
	merged := MergeSignatureCatalogs(BuiltInSignatures(), user)

	grype, _ := merged.Lookup("development.security.grype-db-download")
	want := builtIn(t, "development.security.grype-db-download")
	if !reflect.DeepEqual(grype.SiblingEvidence, want.SiblingEvidence) {
		t.Fatalf("sibling evidence dropped: %v", grype.SiblingEvidence)
	}
	if !reflect.DeepEqual(grype.AnyChildPaths, want.AnyChildPaths) {
		t.Fatalf("child evidence widened: %v", grype.AnyChildPaths)
	}
	if grype.MinAgeFloor != want.MinAgeFloor {
		t.Fatalf("floor dropped: %q", grype.MinAgeFloor)
	}

	gocache, _ := merged.Lookup("development.go.repo-local-gocache")
	if len(gocache.RequiredAncestorFiles) == 0 || gocache.RequiredAncestorFiles[0] != "go.mod" {
		t.Fatalf("go.mod ancestor dropped: %v", gocache.RequiredAncestorFiles)
	}
	if gocache.ContentMarker == nil || gocache.ContentMarker.FirstLine != GoBuildCacheReadmeFirstLine {
		t.Fatalf("content marker dropped: %+v", gocache.ContentMarker)
	}
}

// A reused id may raise the floor, never lower it.
func TestMergeFloorIsAMinimum(t *testing.T) {
	cases := map[string]string{"30m": "1h", "": "1h", "bogus": "1h", "2h": "2h", "1d": "1d"}
	for userFloor, want := range cases {
		user := SignatureCatalog{"development": {"security": {"grype-db-download": {
			CandidatePatterns: []string{"**/grype/db/grype-db-download*"}, MinAgeFloor: userFloor,
		}}}}
		got, _ := MergeSignatureCatalogs(BuiltInSignatures(), user).Lookup("development.security.grype-db-download")
		if got.MinAgeFloor != want {
			t.Errorf("user floor %q: got %q, want %q", userFloor, got.MinAgeFloor, want)
		}
	}
}

// A user may add evidence to a reused id.
func TestMergeAllowsAddingEvidence(t *testing.T) {
	user := SignatureCatalog{"development": {"go": {"repo-local-gocache": {
		CandidatePatterns:     []string{"**/.gocache"},
		RequiredAncestorFiles: []string{"go.sum"},
	}}}}
	got, _ := MergeSignatureCatalogs(BuiltInSignatures(), user).Lookup("development.go.repo-local-gocache")
	if !reflect.DeepEqual(got.RequiredAncestorFiles, []string{"go.mod", "go.sum"}) {
		t.Fatalf("ancestors = %v, want built-in plus added", got.RequiredAncestorFiles)
	}
}

// A new id is the user's own rule: nothing is imposed on it.
func TestMergeNewIDIsUnconstrained(t *testing.T) {
	user := SignatureCatalog{"development": {"go": {"my-cache": {
		CandidatePatterns: []string{"**/.mycache"}, SafeToPrune: true,
	}}}}
	got, ok := MergeSignatureCatalogs(BuiltInSignatures(), user).Lookup("development.go.my-cache")
	if !ok || got.MinAgeFloor != "" || got.ContentMarker != nil || len(got.SiblingEvidence) != 0 {
		t.Fatalf("new id should be exactly the user's: %+v", got)
	}
}
