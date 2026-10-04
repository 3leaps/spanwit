package config

import (
	"fmt"
	"sort"

	"github.com/3leaps/spanwit/internal/catalog"
)

// DefaultEnabledDomain is the built-in enabled use domain when domains.enabled
// is omitted. Keeping development the default preserves current behavior for
// configs written before the domain-catalog feature.
const DefaultEnabledDomain = "development"

// EnabledDomains is the resolved use-domain policy for a config.
type EnabledDomains struct {
	// Set is the membership set used for authorize-narrow gating.
	Set map[string]bool
	// List is the sorted resolved enabled domains (for reporting).
	List []string
	// Configured is true when the config supplied a domains.enabled key
	// (including an explicit empty list). False means built-in default.
	Configured bool
}

// Enabled reports whether a domain is authorized.
func (e EnabledDomains) Enabled(domain string) bool {
	return e.Set[domain]
}

// ResolveEnabledDomains derives the authorized domain set from a config.
//
//   - nil config, no domains block, or omitted enabled → default {development}
//   - explicit empty list → empty set (broad inventory, no domain recipes/reclaim)
//   - explicit list → exactly those domains
func ResolveEnabledDomains(cfg *Config) EnabledDomains {
	if cfg == nil || cfg.Domains == nil || cfg.Domains.Enabled == nil {
		return EnabledDomains{
			Set:        map[string]bool{DefaultEnabledDomain: true},
			List:       []string{DefaultEnabledDomain},
			Configured: false,
		}
	}
	set := map[string]bool{}
	for _, d := range *cfg.Domains.Enabled {
		set[d] = true
	}
	list := make([]string, 0, len(set))
	for d := range set {
		list = append(list, d)
	}
	sort.Strings(list)
	return EnabledDomains{Set: set, List: list, Configured: true}
}

// ResolveCatalog builds the effective domain catalog for a config: the built-in
// catalog with any user overlay applied (deterministic full-entry replacement by
// id), then validates that signature/recipe links resolve and that explicitly
// enabled domain names exist. Every config-consuming command calls this (via
// validateConfig at load), so scan, prune, and space reject the same invalid
// catalog. Overlay escalation is rejected inside catalog.WithOverlay (fail-closed).
func ResolveCatalog(cfg *Config) (*catalog.Catalog, error) {
	base, err := catalog.BuiltIn()
	if err != nil {
		return nil, err
	}
	cat := base
	if cfg != nil && cfg.Domains != nil && len(cfg.Domains.Catalog) > 0 {
		cat, err = base.WithOverlay(cfg.Domains.Catalog)
		if err != nil {
			return nil, err
		}
	}
	var userSigs SignatureCatalog
	if cfg != nil {
		userSigs = cfg.Signatures
	}
	sigIDs := MergeSignatureCatalogs(BuiltInSignatures(), userSigs).IDs()
	if err := cat.ValidateLinks(sigIDs, catalog.RegisteredRecipeIDs()); err != nil {
		return nil, err
	}
	if cfg != nil && cfg.Domains != nil && cfg.Domains.Enabled != nil {
		for _, d := range *cfg.Domains.Enabled {
			if _, ok := cat.DomainByName(d); !ok {
				return nil, fmt.Errorf("domains.enabled: unknown use domain %q (not declared in the catalog)", d)
			}
		}
	}
	return cat, nil
}

// IDs flattens a signature catalog into the set of full-spine signature ids.
func (c SignatureCatalog) IDs() map[string]bool {
	out := map[string]bool{}
	for domain, ecosystems := range c {
		for ecosystem, signatures := range ecosystems {
			for name := range signatures {
				out[domain+"."+ecosystem+"."+name] = true
			}
		}
	}
	return out
}
