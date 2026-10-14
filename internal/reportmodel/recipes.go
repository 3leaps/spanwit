package reportmodel

// Recipe is a guided, non-executable cleanup suggestion for diagnostic-only
// capacity (toolchains, caches) or a multi-step structural journey (016A).
// Recipes never mark paths as prunable and never invoke delete; they print
// suggested external commands and copy-paste steps for human review.
type Recipe struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"` // always diagnostic-only
	// Kind is "argv" (default when empty) or "structural" for multi-step journeys.
	Kind               string             `json:"kind,omitempty"`
	RebuildExpectation string             `json:"rebuild_expectation,omitempty"`
	Path               string             `json:"path,omitempty"`
	Keep               []string           `json:"keep,omitempty"`
	SuggestedCommands  []SuggestedCommand `json:"suggested_commands"`
	// Steps are ordered structural journey steps (print-only). Required when Kind=structural.
	// For structural recipes, steps are authoritative; suggested_commands is an ordered
	// projection of non-nil step.Command values (same argv sequence).
	Steps []RecipeStep `json:"steps,omitempty"`
	// ExactPlan is an optional closed-set revalidation plan scoped to this recipe
	// (e.g. Cargo-only targets). Revalidate with: spanwit prune --from-exact-plan <file>
	// after writing this object to disk. Never broader than the recipe's purge step paths.
	ExactPlan *ExactPlan `json:"exact_plan,omitempty"`
	Rationale []string   `json:"rationale,omitempty"`
	// Domain / Ecosystem / CatalogID are optional taxonomy tags for inventory filters.
	Domain    string `json:"domain,omitempty"`
	Ecosystem string `json:"ecosystem,omitempty"`
	CatalogID string `json:"catalog_id,omitempty"`
}
