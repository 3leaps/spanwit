package space

import (
	"testing"
)

func TestTaxonomyMatches_SegmentBoundaries(t *testing.T) {
	cases := []struct {
		id, filter string
		want       bool
	}{
		{"development.rust.cargo-target", "development", true},
		{"development.rust.cargo-target", "development.rust", true},
		{"development.rust.cargo-target", "development.rust.cargo-target", true},
		{"development.rust.cargo-target", "develop", false},
		{"development.rust.cargo-target", "development.ru", false},
		{"development.rust.cargo-target", "development.rustup", false},
		{"development.rustup.toolchains", "development.rust", false},
		{"development.go.go-build", "development.go", true},
		{"", "development", false},
		{"development", "", false},
	}
	for _, tc := range cases {
		got := TaxonomyMatches(tc.id, tc.filter)
		if got != tc.want {
			t.Errorf("TaxonomyMatches(%q, %q)=%v want %v", tc.id, tc.filter, got, tc.want)
		}
	}
}

func TestNormalizeInventoryFilters(t *testing.T) {
	f, err := NormalizeInventoryFilters(
		[]string{"prunable", "diagnostic-only", "prunable"},
		[]string{"development.rust", "development"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Classes) != 2 || f.Classes[0] != "diagnostic-only" || f.Classes[1] != "prunable" {
		t.Fatalf("classes canonical: %#v", f.Classes)
	}
	if len(f.Domains) != 2 || f.Domains[0] != "development" || f.Domains[1] != "development.rust" {
		t.Fatalf("domains canonical: %#v", f.Domains)
	}
	af := f.ToApplied()
	if af == nil || af.Classes == nil || af.Domains == nil {
		t.Fatal("ToApplied must set both arrays")
	}

	// Alias "diagnostic" rejected.
	if _, err := NormalizeInventoryFilters([]string{"diagnostic"}, nil); err == nil {
		t.Fatal("expected reject alias diagnostic")
	}
	// Empty class.
	if _, err := NormalizeInventoryFilters([]string{""}, nil); err == nil {
		t.Fatal("expected reject empty class")
	}
	// Malformed domain (syntax + schema segment grammar parity).
	for _, bad := range []string{
		".dev", "dev.", "dev..rust", "Development", "dev/rust", "dev*",
		"development.-rust", "development.rust-", "development._rust", "development.rust_",
		" development.rust", "development.rust ", "development rust",
		"-rust", "rust-", "_x", "x_",
	} {
		if _, err := NormalizeInventoryFilters(nil, []string{bad}); err == nil {
			t.Fatalf("expected reject domain %q", bad)
		}
	}
	// Schema-legal interiors still ok.
	for _, good := range []string{"development", "development.rust", "development.rust.cargo-target", "a", "go-build"} {
		if _, err := NormalizeInventoryFilters(nil, []string{good}); err != nil {
			t.Fatalf("expected accept domain %q: %v", good, err)
		}
	}
}

func TestEntryMatchesFilters_ORAndAND(t *testing.T) {
	prunableRust := Entry{
		State:     StatePrunable,
		Signature: "development.rust.cargo-target",
		Domain:    "development",
		Ecosystem: "rust",
	}
	hotspotGo := Entry{
		State:     StateDiagnosticOnly,
		CatalogID: "development.go.go-build",
		Domain:    "development",
		Ecosystem: "go",
	}
	unknown := Entry{State: StateUnknown, Path: "/tmp/x"}

	// Class OR
	f, _ := NormalizeInventoryFilters([]string{"prunable", "withheld"}, nil)
	if !EntryMatchesFilters(prunableRust, f) {
		t.Fatal("prunable should match class OR")
	}
	if EntryMatchesFilters(hotspotGo, f) {
		t.Fatal("diagnostic-only should not match prunable|withheld")
	}

	// Domain OR
	f, _ = NormalizeInventoryFilters(nil, []string{"development.rust", "development.go"})
	if !EntryMatchesFilters(prunableRust, f) || !EntryMatchesFilters(hotspotGo, f) {
		t.Fatal("domain OR should match both spines")
	}
	if EntryMatchesFilters(unknown, f) {
		t.Fatal("unknown without spine must not match domain filter")
	}

	// Class AND domain
	f, _ = NormalizeInventoryFilters([]string{"prunable"}, []string{"development.go"})
	if EntryMatchesFilters(prunableRust, f) {
		t.Fatal("rust prunable must not match go domain")
	}
	if EntryMatchesFilters(hotspotGo, f) {
		t.Fatal("go hotspot is diagnostic-only, not prunable")
	}
	f, _ = NormalizeInventoryFilters([]string{"diagnostic-only"}, []string{"development.go"})
	if !EntryMatchesFilters(hotspotGo, f) {
		t.Fatal("want diagnostic-only AND development.go match")
	}
}

func TestDomainEcosystemFromSpine(t *testing.T) {
	d, e := DomainEcosystemFromSpine("development.rust.cargo-target")
	if d != "development" || e != "rust" {
		t.Fatalf("got %q %q", d, e)
	}
	d, e = DomainEcosystemFromSpine("solo")
	if d != "solo" || e != "" {
		t.Fatalf("solo: %q %q", d, e)
	}
}
