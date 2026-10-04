// Package catalog loads and validates the spanwit domain catalog: the curated,
// user-extensible list of use domains and their disk-concern entries.
//
// Trust posture (fail-closed): an entry's ClassCeiling is a trust ceiling, not
// a delete authority. Only a code-registered capability (a signature with
// evidence and guards, resolved elsewhere) can ever produce a deletion path.
// User overlays may add diagnostic inventory but can never promote a path to
// prunable — that is rejected here as a trust-ceiling escalation.
//
// This package is deliberately low-level: it does not import internal/config or
// internal/space. Link resolution (signatures, recipes) is validated against
// id sets passed in by the caller so there is no import cycle.
package catalog

import (
	"fmt"
	"sort"
	"strings"

	catalogdata "github.com/3leaps/spanwit/config/catalog"
	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
	"gopkg.in/yaml.v3"
)

// Class ceilings (maximum trust an entry may carry).
const (
	ClassPrunable       = "prunable"
	ClassDiagnosticOnly = "diagnostic-only"
	ClassRecipe         = "recipe"
)

// Domain status values.
const (
	StatusCurated = "curated"
	StatusStub    = "stub"
)

// Entry provenance.
const (
	SourceBuiltIn = "builtin"
	SourceOverlay = "overlay"
)

// Registered guided-recipe ids. This is the canonical vocabulary the product
// can build; catalog recipe links must resolve to one of these. The space
// package registers a builder for each. Kept here (not in space) so config can
// validate links without importing space.
const (
	RecipeRustupToolchains = "rustup-toolchains"
	RecipeGoBuildCache     = "go-build-cache"
	RecipeCargoCache       = "cargo-cache"
	RecipeBrewCleanup      = "brew-cleanup"
	RecipeUVCachePrune     = "uv-cache-prune"
	// RecipeCargoTargetPlacement is a structural journey id (016A). It is
	// evidence-driven from space verified rows, not a home-cache argv recipe, so
	// it is intentionally absent from RegisteredRecipeIDs (catalog links stay
	// argv-only).
	RecipeCargoTargetPlacement = "cargo-target-placement"
)

// RegisteredRecipeIDs returns the set of buildable home-cache guided-recipe ids
// (argv builders in the space package). Catalog recipe links must resolve here.
// Structural journey ids (e.g. cargo-target-placement) are not catalog-linked.
func RegisteredRecipeIDs() map[string]bool {
	return map[string]bool{
		RecipeRustupToolchains: true,
		RecipeGoBuildCache:     true,
		RecipeCargoCache:       true,
		RecipeBrewCleanup:      true,
		RecipeUVCachePrune:     true,
	}
}

// Location is a per-platform home-relative discovery hint.
type Location struct {
	RelHome string `yaml:"rel_home"`
}

// Domain is a curated top-level product category of disk concerns.
type Domain struct {
	Name        string `yaml:"name"`
	Label       string `yaml:"label"`
	Status      string `yaml:"status"`
	Description string `yaml:"description,omitempty"`
}

// Entry is one catalog row: classification metadata for a disk-concern
// location or signature-backed target. Domain/Ecosystem/Name are derived from
// ID and never independently authored.
type Entry struct {
	ID                 string              `yaml:"id"`
	Label              string              `yaml:"label"`
	ClassCeiling       string              `yaml:"class_ceiling"`
	RebuildExpectation string              `yaml:"rebuild_expectation,omitempty"`
	Platforms          []string            `yaml:"platforms,omitempty"`
	Locations          map[string]Location `yaml:"locations,omitempty"`
	Signature          string              `yaml:"signature,omitempty"`
	Recipes            []string            `yaml:"recipes,omitempty"`
	Description        string              `yaml:"description,omitempty"`

	// Derived, not decoded from YAML.
	Domain    string `yaml:"-"`
	Ecosystem string `yaml:"-"`
	Name      string `yaml:"-"`
	Source    string `yaml:"-"`
}

// Catalog is a validated, indexed domain catalog.
type Catalog struct {
	Version  int      `yaml:"version"`
	Domains  []Domain `yaml:"domains"`
	Entries  []Entry  `yaml:"entries"`
	byID     map[string]int
	byDomain map[string]int
}

// BuiltIn loads and validates the embedded built-in catalog (SSOT).
func BuiltIn() (*Catalog, error) {
	return Load(spanwitschema.SpanwitDomainCatalogV1, catalogdata.BuiltInDomainCatalogV1)
}

// Load validates yamlData against schemaData, decodes it, and structurally
// validates + indexes the result. Link resolution is not checked here.
func Load(schemaData, yamlData []byte) (*Catalog, error) {
	if err := contract.ValidateYAML(schemaData, yamlData); err != nil {
		return nil, fmt.Errorf("domain catalog does not match schema: %w", err)
	}
	var cat Catalog
	if err := yaml.Unmarshal(yamlData, &cat); err != nil {
		return nil, fmt.Errorf("failed to parse domain catalog: %w", err)
	}
	if err := cat.finalize(SourceBuiltIn); err != nil {
		return nil, err
	}
	return &cat, nil
}

// finalize derives id segments, sets provenance on entries lacking one, and
// builds indexes with structural validation (dup ids, declared domains).
func (c *Catalog) finalize(defaultSource string) error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported domain catalog version %d", c.Version)
	}
	c.byDomain = make(map[string]int, len(c.Domains))
	for i, d := range c.Domains {
		if _, dup := c.byDomain[d.Name]; dup {
			return fmt.Errorf("duplicate domain %q in catalog", d.Name)
		}
		c.byDomain[d.Name] = i
	}
	c.byID = make(map[string]int, len(c.Entries))
	for i := range c.Entries {
		e := &c.Entries[i]
		dom, eco, name, err := splitEntryID(e.ID)
		if err != nil {
			return err
		}
		e.Domain, e.Ecosystem, e.Name = dom, eco, name
		if e.Source == "" {
			e.Source = defaultSource
		}
		if _, dup := c.byID[e.ID]; dup {
			return fmt.Errorf("duplicate catalog entry id %q", e.ID)
		}
		if _, ok := c.byDomain[e.Domain]; !ok {
			return fmt.Errorf("catalog entry %q references undeclared domain %q", e.ID, e.Domain)
		}
		c.byID[e.ID] = i
	}
	return nil
}

// EntryByID returns the catalog entry for id.
func (c *Catalog) EntryByID(id string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	if i, ok := c.byID[id]; ok {
		return c.Entries[i], true
	}
	return Entry{}, false
}

// DomainByName returns the declared domain.
func (c *Catalog) DomainByName(name string) (Domain, bool) {
	if c == nil {
		return Domain{}, false
	}
	if i, ok := c.byDomain[name]; ok {
		return c.Domains[i], true
	}
	return Domain{}, false
}

// DomainNames returns declared domain names in catalog order.
func (c *Catalog) DomainNames() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.Domains))
	for _, d := range c.Domains {
		out = append(out, d.Name)
	}
	return out
}

// AppliesTo reports whether an entry applies on the given GOOS.
// An empty Platforms list means all platforms.
func (e Entry) AppliesTo(goos string) bool {
	if len(e.Platforms) == 0 {
		return true
	}
	for _, p := range e.Platforms {
		if p == goos {
			return true
		}
	}
	return false
}

// LocationFor returns the home-relative discovery path for goos, preferring an
// OS-specific location then the default. Returns false when no location applies.
func (e Entry) LocationFor(goos string) (string, bool) {
	if e.Locations == nil {
		return "", false
	}
	if loc, ok := e.Locations[goos]; ok && loc.RelHome != "" {
		return loc.RelHome, true
	}
	if loc, ok := e.Locations["default"]; ok && loc.RelHome != "" {
		return loc.RelHome, true
	}
	return "", false
}

// HotspotsForOS returns entries that are home-cache hotspots on goos: they
// apply to the platform and resolve a discovery location. Order follows the
// catalog for determinism.
func (c *Catalog) HotspotsForOS(goos string) []Entry {
	if c == nil {
		return nil
	}
	out := make([]Entry, 0, len(c.Entries))
	for _, e := range c.Entries {
		if !e.AppliesTo(goos) {
			continue
		}
		if _, ok := e.LocationFor(goos); !ok {
			continue
		}
		out = append(out, e)
	}
	return out
}

// WithOverlay returns a new catalog with overlay entries applied by
// deterministic full-entry replacement by id. Overlays may add new entries or
// replace built-in ones, but may never carry a prunable ceiling: promoting a
// path to prunable is a trust-ceiling escalation and is rejected (fail-closed).
// The base catalog is not mutated.
func (c *Catalog) WithOverlay(overlay []Entry) (*Catalog, error) {
	merged := &Catalog{
		Version: c.Version,
		Domains: append([]Domain(nil), c.Domains...),
		Entries: append([]Entry(nil), c.Entries...),
	}
	if err := merged.finalize(SourceBuiltIn); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, oe := range overlay {
		dom, _, _, err := splitEntryID(oe.ID)
		if err != nil {
			return nil, fmt.Errorf("overlay entry: %w", err)
		}
		if seen[oe.ID] {
			return nil, fmt.Errorf("duplicate overlay entry id %q", oe.ID)
		}
		seen[oe.ID] = true
		if oe.ClassCeiling == ClassPrunable {
			return nil, fmt.Errorf("overlay entry %q may not set class_ceiling=prunable: user overlays cannot promote a path to prunable (trust-ceiling escalation)", oe.ID)
		}
		if oe.ClassCeiling == "" {
			oe.ClassCeiling = ClassDiagnosticOnly
		}
		if _, ok := merged.byDomain[dom]; !ok {
			return nil, fmt.Errorf("overlay entry %q references undeclared domain %q", oe.ID, dom)
		}
		oe.Source = SourceOverlay
		if idx, ok := merged.byID[oe.ID]; ok {
			// Full-entry replacement by id. Replacing a built-in prunable entry is
			// a fail-closed downgrade/relabel: the overlay itself cannot be prunable
			// (blocked above), so this only lowers or annotates classification. It
			// never disables the underlying code capability (the signature still
			// executes) — catalog class is metadata, not delete authority.
			merged.Entries[idx] = oe
		} else {
			merged.Entries = append(merged.Entries, oe)
		}
	}
	// Re-derive segments/provenance/indexes for the merged set.
	if err := merged.reindex(); err != nil {
		return nil, err
	}
	return merged, nil
}

// reindex rebuilds derived fields and indexes without overwriting provenance.
func (c *Catalog) reindex() error {
	c.byDomain = make(map[string]int, len(c.Domains))
	for i, d := range c.Domains {
		c.byDomain[d.Name] = i
	}
	c.byID = make(map[string]int, len(c.Entries))
	for i := range c.Entries {
		e := &c.Entries[i]
		dom, eco, name, err := splitEntryID(e.ID)
		if err != nil {
			return err
		}
		e.Domain, e.Ecosystem, e.Name = dom, eco, name
		if e.Source == "" {
			e.Source = SourceBuiltIn
		}
		c.byID[e.ID] = i
	}
	return nil
}

// ValidateLinks rejects dangling signature and recipe links. sigIDs and
// recipeIDs are the known resolvable ids (built-in + user). A nil set means
// "unknown — skip that link type" only when explicitly passed nil; pass an
// empty non-nil map to require all links resolve to nothing (reject any).
func (c *Catalog) ValidateLinks(sigIDs, recipeIDs map[string]bool) error {
	for _, e := range c.Entries {
		if e.Signature != "" && sigIDs != nil {
			if !sigIDs[e.Signature] {
				return fmt.Errorf("catalog entry %q links dangling signature %q", e.ID, e.Signature)
			}
		}
		if recipeIDs != nil {
			for _, r := range e.Recipes {
				if !recipeIDs[r] {
					return fmt.Errorf("catalog entry %q links dangling recipe %q", e.ID, r)
				}
			}
		}
	}
	return nil
}

// splitEntryID splits a domain.ecosystem.name id and validates its shape.
func splitEntryID(id string) (domain, ecosystem, name string, err error) {
	parts := strings.Split(id, ".")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("catalog entry id %q must use domain.ecosystem.name form", id)
	}
	for _, p := range parts {
		if p == "" {
			return "", "", "", fmt.Errorf("catalog entry id %q contains an empty segment", id)
		}
	}
	return parts[0], parts[1], parts[2], nil
}

// SortedEntryIDs returns entry ids sorted (test/determinism helper).
func (c *Catalog) SortedEntryIDs() []string {
	out := make([]string, 0, len(c.Entries))
	for _, e := range c.Entries {
		out = append(out, e.ID)
	}
	sort.Strings(out)
	return out
}
