package space

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/contract"
)

// cargoFixture writes a minimal context-verified cargo target and returns root.
func cargoFixture(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "x.o"), 8192)
	return tmp
}

func analyzeWithDomains(t *testing.T, root string, cfg *config.Config, filters ...string) Report {
	t.Helper()
	opts := Options{
		Path:              root,
		MinSize:           "1K",
		Top:               10,
		IncludeHomeCaches: false,
		Config:            cfg,
		Now:               time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	}
	if len(filters) > 0 {
		opts.DomainFilters = filters
	}
	report, err := Analyze(context.Background(), opts)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	raw, _ := json.Marshal(report)
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
		t.Fatalf("schema: %v\n%s", err, raw)
	}
	return report
}

func TestDomainEnable_OmittedKeepsCargoPrunable(t *testing.T) {
	report := analyzeWithDomains(t, cargoFixture(t), nil)
	if report.Verified.PrunableCandidates < 1 {
		t.Fatalf("default enabled set must keep cargo-target prunable: %#v", report.Verified)
	}
	if report.PruneHandoff == nil || !report.PruneHandoff.Present {
		t.Fatal("default: prune handoff should be present")
	}
	if report.EnabledDomains != nil {
		t.Fatalf("omitted domains block must not echo enabled_domains: %#v", *report.EnabledDomains)
	}
}

func TestDomainEnable_ExplicitEmptyWithholdsAndDropsHandoff(t *testing.T) {
	empty := []string{}
	cfg := &config.Config{Version: 1, Domains: &config.DomainsConfig{Enabled: &empty}}
	report := analyzeWithDomains(t, cargoFixture(t), cfg)

	if report.Verified.PrunableCandidates != 0 {
		t.Fatalf("explicit empty must leave zero prunable: %#v", report.Verified)
	}
	if report.Verified.WithheldCandidates < 1 {
		t.Fatalf("cargo-target should be withheld: %#v", report.Verified)
	}
	foundDomainDisabled := false
	for _, c := range report.Verified.Candidates {
		if c.Signature == "development.rust.cargo-target" {
			if c.State != StateWithheld || c.WithheldReason != WithheldReasonDomainDisabled {
				t.Fatalf("cargo-target should be withheld/domain_disabled: %#v", c)
			}
			foundDomainDisabled = true
		}
	}
	if !foundDomainDisabled {
		t.Fatal("expected cargo-target withheld with domain_disabled")
	}
	if report.PruneHandoff != nil && report.PruneHandoff.Present {
		t.Fatalf("domain-disabled candidate must be absent from handoff: %#v", report.PruneHandoff)
	}
	if report.EnabledDomains == nil || len(*report.EnabledDomains) != 0 {
		t.Fatalf("explicit empty must echo an empty enabled_domains: %#v", report.EnabledDomains)
	}
}

func TestDomainEnable_OtherDomainDisablesDevelopment(t *testing.T) {
	media := []string{"media"}
	cfg := &config.Config{Version: 1, Domains: &config.DomainsConfig{Enabled: &media}}
	report := analyzeWithDomains(t, cargoFixture(t), cfg)
	if report.Verified.PrunableCandidates != 0 {
		t.Fatalf("development disabled: expected zero prunable: %#v", report.Verified)
	}
	if report.EnabledDomains == nil || len(*report.EnabledDomains) != 1 || (*report.EnabledDomains)[0] != "media" {
		t.Fatalf("enabled_domains echo: %#v", report.EnabledDomains)
	}
}

func TestDomainEnable_ExplicitDevelopmentKeepsPrunable(t *testing.T) {
	dev := []string{"development"}
	cfg := &config.Config{Version: 1, Domains: &config.DomainsConfig{Enabled: &dev}}
	report := analyzeWithDomains(t, cargoFixture(t), cfg)
	if report.Verified.PrunableCandidates < 1 {
		t.Fatalf("development enabled must keep prunable: %#v", report.Verified)
	}
	if report.PruneHandoff == nil || !report.PruneHandoff.Present {
		t.Fatal("development enabled: handoff should be present")
	}
}

// A --domain view filter must never authorize a disabled domain: with
// development disabled, projecting the view to development still yields no
// prunable/handoff — the filter cannot enable policy.
func TestDomainEnable_ViewFilterCannotAuthorize(t *testing.T) {
	empty := []string{}
	cfg := &config.Config{Version: 1, Domains: &config.DomainsConfig{Enabled: &empty}}
	report := analyzeWithDomains(t, cargoFixture(t), cfg, "development")
	if report.Verified.PrunableCandidates != 0 {
		t.Fatalf("view filter must not authorize a disabled domain: %#v", report.Verified)
	}
	if report.PruneHandoff != nil && report.PruneHandoff.Present {
		t.Fatal("view filter must not populate handoff for a disabled domain")
	}
}

func TestBuildRecipes_GatedByEnabledDomain(t *testing.T) {
	ctx := context.Background()
	toolchains := []string{"stable-x86_64-apple-darwin"}

	// development disabled → no development recipes even with a toolchain present.
	off := BuildRecipes(ctx, RecipeOptions{
		IncludeRustup: true,
		Home:          t.TempDir(),
		ToolchainDirs: toolchains,
		Enabled:       config.EnabledDomains{Set: map[string]bool{"media": true}},
	})
	if len(off) != 0 {
		t.Fatalf("development recipes must be gated off: %#v", off)
	}

	// development enabled → rustup recipe present.
	on := BuildRecipes(ctx, RecipeOptions{
		IncludeRustup: true,
		Home:          t.TempDir(),
		ToolchainDirs: toolchains,
		Enabled:       config.EnabledDomains{Set: map[string]bool{"development": true}},
	})
	if len(on) < 1 {
		t.Fatalf("development enabled should build the rustup recipe: %#v", on)
	}
}
