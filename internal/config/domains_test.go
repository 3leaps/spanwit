package config

import (
	"testing"

	"github.com/3leaps/spanwit/internal/catalog"
)

func TestResolveEnabledDomains_DefaultWhenOmitted(t *testing.T) {
	for _, cfg := range []*Config{nil, {Version: 1}, {Version: 1, Domains: &DomainsConfig{}}} {
		got := ResolveEnabledDomains(cfg)
		if got.Configured {
			t.Fatalf("omitted enable set must not be Configured: %#v", got)
		}
		if !got.Enabled("development") || len(got.List) != 1 {
			t.Fatalf("default enabled set should be [development]: %#v", got)
		}
	}
}

func TestResolveEnabledDomains_ExplicitEmpty(t *testing.T) {
	empty := []string{}
	got := ResolveEnabledDomains(&Config{Version: 1, Domains: &DomainsConfig{Enabled: &empty}})
	if !got.Configured {
		t.Fatal("explicit empty must be Configured")
	}
	if got.Enabled("development") {
		t.Fatal("explicit empty must not enable development")
	}
	if len(got.List) != 0 {
		t.Fatalf("explicit empty list must be empty: %#v", got.List)
	}
}

func TestResolveEnabledDomains_ExplicitList(t *testing.T) {
	list := []string{"media", "development"}
	got := ResolveEnabledDomains(&Config{Version: 1, Domains: &DomainsConfig{Enabled: &list}})
	if !got.Enabled("media") || !got.Enabled("development") {
		t.Fatalf("both domains should be enabled: %#v", got)
	}
	if len(got.List) != 2 || got.List[0] != "development" || got.List[1] != "media" {
		t.Fatalf("list should be sorted: %#v", got.List)
	}
}

func TestSignatureCatalogIDs(t *testing.T) {
	ids := BuiltInSignatures().IDs()
	if !ids["development.rust.cargo-target"] {
		t.Fatalf("expected cargo-target in signature ids: %#v", ids)
	}
}

func TestValidateDomains_RejectsBadEnabledName(t *testing.T) {
	bad := []string{"Development"}
	err := validateDomains(&Config{Domains: &DomainsConfig{Enabled: &bad}})
	if err == nil {
		t.Fatal("uppercase domain name should be rejected")
	}
}

func TestValidateDomains_RejectsDuplicateOverlayID(t *testing.T) {
	err := validateDomains(&Config{Domains: &DomainsConfig{Catalog: []catalog.Entry{
		{ID: "development.node.a", Label: "a", ClassCeiling: catalog.ClassDiagnosticOnly},
		{ID: "development.node.a", Label: "b", ClassCeiling: catalog.ClassDiagnosticOnly},
	}}})
	if err == nil {
		t.Fatal("duplicate overlay id should be rejected")
	}
}

func TestValidateDomains_RejectsMalformedOverlayID(t *testing.T) {
	err := validateDomains(&Config{Domains: &DomainsConfig{Catalog: []catalog.Entry{
		{ID: "twoseg.only", Label: "a", ClassCeiling: catalog.ClassDiagnosticOnly},
	}}})
	if err == nil {
		t.Fatal("malformed overlay id should be rejected")
	}
}

func TestResolveCatalog_BuiltInOK(t *testing.T) {
	if _, err := ResolveCatalog(nil); err != nil {
		t.Fatalf("built-in catalog should resolve: %v", err)
	}
}

func TestResolveCatalog_RejectsUnknownEnabledDomain(t *testing.T) {
	bad := []string{"nope"}
	if _, err := ResolveCatalog(&Config{Version: 1, Domains: &DomainsConfig{Enabled: &bad}}); err == nil {
		t.Fatal("unknown enabled domain should be rejected")
	}
}

func TestResolveCatalog_RejectsDanglingRecipeLink(t *testing.T) {
	_, err := ResolveCatalog(&Config{Version: 1, Domains: &DomainsConfig{Catalog: []catalog.Entry{
		{ID: "development.node.thing", Label: "x", ClassCeiling: catalog.ClassDiagnosticOnly, Recipes: []string{"no-such-recipe"}},
	}}})
	if err == nil {
		t.Fatal("dangling recipe link should be rejected")
	}
}

func TestResolveCatalog_RejectsOverlayEscalation(t *testing.T) {
	_, err := ResolveCatalog(&Config{Version: 1, Domains: &DomainsConfig{Catalog: []catalog.Entry{
		{ID: "development.node.thing", Label: "x", ClassCeiling: catalog.ClassPrunable},
	}}})
	if err == nil {
		t.Fatal("overlay prunable ceiling should be rejected")
	}
}
