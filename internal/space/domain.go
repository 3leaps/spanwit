package space

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Valid inventory class values (ADR-0004 state vocabulary).
var validClassValues = map[string]bool{
	StatePrunable:       true,
	StateWithheld:       true,
	StateDiagnosticOnly: true,
	StateUnverified:     true,
	StateUnknown:        true,
}

// AppliedFilters records opt-in class/domain projection on a SpaceReport.
// Present only when at least one filter was supplied. Both arrays are always
// non-nil when the object is present (empty when that flag type was unused).
type AppliedFilters struct {
	Classes []string `json:"classes"`
	Domains []string `json:"domains"`
}

// InventoryFilters is the validated, canonical projection request.
type InventoryFilters struct {
	Classes []string // empty = no class projection
	Domains []string // empty = no domain projection
}

// Active reports whether any inventory filter was requested.
func (f InventoryFilters) Active() bool {
	return len(f.Classes) > 0 || len(f.Domains) > 0
}

// ToApplied returns the JSON carrier shape (both arrays non-nil, never null).
func (f InventoryFilters) ToApplied() *AppliedFilters {
	if !f.Active() {
		return nil
	}
	// Always allocate empty slices so json.Marshal emits [] not null.
	classes := make([]string, len(f.Classes))
	copy(classes, f.Classes)
	domains := make([]string, len(f.Domains))
	copy(domains, f.Domains)
	return &AppliedFilters{
		Classes: classes,
		Domains: domains,
	}
}

// NormalizeInventoryFilters validates and canonicalizes class/domain selectors.
// Unknown class values, empty values, and malformed domains are errors.
// Duplicates are removed; order is sorted for deterministic applied_filters.
func NormalizeInventoryFilters(classes, domains []string) (InventoryFilters, error) {
	out := InventoryFilters{
		Classes: make([]string, 0, len(classes)),
		Domains: make([]string, 0, len(domains)),
	}
	seenClass := map[string]bool{}
	for _, c := range classes {
		c = strings.TrimSpace(c)
		if c == "" {
			return InventoryFilters{}, fmt.Errorf("empty --class value")
		}
		if !validClassValues[c] {
			return InventoryFilters{}, fmt.Errorf("unsupported --class %q (use prunable|withheld|diagnostic-only|unverified|unknown)", c)
		}
		if !seenClass[c] {
			seenClass[c] = true
			out.Classes = append(out.Classes, c)
		}
	}
	sort.Strings(out.Classes)

	seenDomain := map[string]bool{}
	for _, d := range domains {
		// Do not trim domains: surrounding whitespace is a usage error (schema parity).
		if d == "" {
			return InventoryFilters{}, fmt.Errorf("empty --domain value")
		}
		if err := ValidateDomainSyntax(d); err != nil {
			return InventoryFilters{}, fmt.Errorf("invalid --domain %q: %w", d, err)
		}
		if !seenDomain[d] {
			seenDomain[d] = true
			out.Domains = append(out.Domains, d)
		}
	}
	sort.Strings(out.Domains)
	return out, nil
}

// ValidateDomainSyntax enforces the same segment grammar as
// spanwit-space-report applied_filters.domains:
//
//	^[a-z0-9]([a-z0-9_-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9_-]*[a-z0-9])?)*$
//
// Each segment starts and ends alphanumeric; interior may use - or _.
// Rejects whitespace (no silent trim), leading/trailing/consecutive dots,
// uppercase, path separators, and glob metacharacters.
func ValidateDomainSyntax(d string) error {
	if d == "" {
		return fmt.Errorf("empty domain")
	}
	if strings.ContainsAny(d, " \t\n\r") {
		return fmt.Errorf("domain must not contain whitespace")
	}
	if strings.ContainsAny(d, "/\\*?[]{}()!^$+|") {
		return fmt.Errorf("domain must be lowercase dot-delimited segments (no path or glob characters)")
	}
	if strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") || strings.Contains(d, "..") {
		return fmt.Errorf("domain must not have leading, trailing, or consecutive dots")
	}
	for _, seg := range strings.Split(d, ".") {
		if err := validateDomainSegment(seg); err != nil {
			return err
		}
	}
	return nil
}

func validateDomainSegment(seg string) error {
	if seg == "" {
		return fmt.Errorf("empty domain segment")
	}
	// Single-char: must be [a-z0-9]. Multi-char: start/end alnum; middle [a-z0-9_-].
	runes := []rune(seg)
	if !isLowerAlnum(runes[0]) || !isLowerAlnum(runes[len(runes)-1]) {
		return fmt.Errorf("each domain segment must start and end with a lowercase letter or digit")
	}
	for _, r := range runes {
		if isLowerAlnum(r) || r == '-' || r == '_' {
			continue
		}
		if unicode.IsUpper(r) {
			return fmt.Errorf("domain must be lowercase")
		}
		return fmt.Errorf("invalid character %q in domain segment", r)
	}
	return nil
}

func isLowerAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
}

// TaxonomyMatches reports segment-aware prefix match of id against filter.
// filter "development.rust" matches "development.rust" and
// "development.rust.cargo-target", but not "development.rustup" or
// "development.ru".
func TaxonomyMatches(id, filter string) bool {
	if id == "" || filter == "" {
		return false
	}
	if id == filter {
		return true
	}
	return strings.HasPrefix(id, filter+".")
}

// DomainEcosystemFromSpine splits a full-spine id (domain.ecosystem.name…) into
// domain and ecosystem labels. Returns empty strings when fewer than two segments.
func DomainEcosystemFromSpine(spine string) (domain, ecosystem string) {
	parts := strings.Split(spine, ".")
	if len(parts) == 0 || parts[0] == "" {
		return "", ""
	}
	domain = parts[0]
	if len(parts) >= 2 {
		ecosystem = parts[1]
	}
	return domain, ecosystem
}

// EntryTaxonomySpine prefers signature (verified), then catalog_id (catalog rows).
func EntryTaxonomySpine(e Entry) string {
	if e.Signature != "" {
		return e.Signature
	}
	return e.CatalogID
}

// AnnotateEntryTaxonomy derives domain/ecosystem from signature or catalog_id.
// Does not invent independent strings; empty when no spine is present.
func AnnotateEntryTaxonomy(e *Entry) {
	if e == nil {
		return
	}
	spine := EntryTaxonomySpine(*e)
	if spine == "" {
		return
	}
	d, eco := DomainEcosystemFromSpine(spine)
	e.Domain = d
	e.Ecosystem = eco
}

// AnnotateRecipeTaxonomy derives domain/ecosystem from catalog_id when set.
func AnnotateRecipeTaxonomy(r *Recipe) {
	if r == nil || r.CatalogID == "" {
		return
	}
	d, eco := DomainEcosystemFromSpine(r.CatalogID)
	r.Domain = d
	r.Ecosystem = eco
}

// EntryMatchesFilters applies class OR and domain OR, then AND across types.
// Unfiltered (empty) dimension always matches. Domain filters require a spine
// (signature or catalog_id); bare domain/ecosystem fields are not matched alone.
func EntryMatchesFilters(e Entry, f InventoryFilters) bool {
	if len(f.Classes) > 0 {
		ok := false
		for _, c := range f.Classes {
			if e.State == c {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.Domains) > 0 {
		spine := EntryTaxonomySpine(e)
		if spine == "" {
			return false
		}
		ok := false
		for _, d := range f.Domains {
			if TaxonomyMatches(spine, d) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// RecipeMatchesFilters projects recipes by state (class) and catalog taxonomy.
func RecipeMatchesFilters(r Recipe, f InventoryFilters) bool {
	if len(f.Classes) > 0 {
		ok := false
		for _, c := range f.Classes {
			if r.State == c {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.Domains) > 0 {
		spine := r.CatalogID
		if spine == "" {
			// Fall back to domain.ecosystem composition when catalog_id absent.
			if r.Domain == "" {
				return false
			}
			spine = r.Domain
			if r.Ecosystem != "" {
				spine = r.Domain + "." + r.Ecosystem
			}
		}
		ok := false
		for _, d := range f.Domains {
			if TaxonomyMatches(spine, d) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// ProjectEntries returns entries matching f (preserves order).
func ProjectEntries(entries []Entry, f InventoryFilters) []Entry {
	if !f.Active() {
		return entries
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if EntryMatchesFilters(e, f) {
			out = append(out, e)
		}
	}
	return out
}

// ProjectRecipes returns recipes matching f (preserves order).
func ProjectRecipes(recipes []Recipe, f InventoryFilters) []Recipe {
	if !f.Active() {
		return recipes
	}
	out := make([]Recipe, 0, len(recipes))
	for _, r := range recipes {
		if RecipeMatchesFilters(r, f) {
			out = append(out, r)
		}
	}
	return out
}

// ProjectVerified filters verified candidates and recomputes section totals
// from the full filtered set (before --top truncation).
func ProjectVerified(v VerifiedSection, f InventoryFilters) VerifiedSection {
	if !f.Active() {
		annotateVerifiedBoundState(&v)
		return v
	}
	out := VerifiedSection{
		Candidates: make([]Entry, 0, len(v.Candidates)),
	}
	for _, e := range v.Candidates {
		if !EntryMatchesFilters(e, f) {
			continue
		}
		out.Candidates = append(out.Candidates, e)
		switch e.State {
		case StateWithheld:
			out.WithheldCandidates++
			out.WithheldBytes += e.SizeBytes
		case StatePrunable:
			out.PrunableCandidates++
			out.PrunableBytes += e.SizeBytes
		}
	}
	annotateVerifiedBoundState(&out)
	// Human strings filled by caller with engine.HumanSize to avoid import cycle
	// if needed — keep as bytes here; space.go sets human after project.
	return out
}

// annotateVerifiedBoundState sets section size_incomplete and per-state bound
// helpers from the full candidate set (call before --top truncation).
func annotateVerifiedBoundState(v *VerifiedSection) {
	v.SizeIncomplete = false
	v.PrunableSizeIncomplete = false
	v.WithheldSizeIncomplete = false
	for _, e := range v.Candidates {
		if !e.SizeIncomplete {
			continue
		}
		v.SizeIncomplete = true
		switch e.State {
		case StatePrunable:
			v.PrunableSizeIncomplete = true
		case StateWithheld:
			v.WithheldSizeIncomplete = true
		}
	}
}

// ClassesIncludePrunable reports whether class filters allow prunable rows.
// No class filter means broad (includes prunable).
func ClassesIncludePrunable(f InventoryFilters) bool {
	if len(f.Classes) == 0 {
		return true
	}
	for _, c := range f.Classes {
		if c == StatePrunable {
			return true
		}
	}
	return false
}
