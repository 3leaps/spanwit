package space

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/3leaps/spanwit/internal/catalog"
	"github.com/3leaps/spanwit/internal/config"
)

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

// RecipeOptions controls which guided recipes are evaluated.
type RecipeOptions struct {
	// IncludeRustup enables the rustup toolchain pin recipe.
	IncludeRustup bool
	// Home overrides the user home directory (tests).
	Home string
	// ToolchainDirs injects rustup toolchain directory basenames (tests).
	// When nil, recipes list ~/.rustup/toolchains.
	ToolchainDirs []string
	// Catalog is the resolved domain catalog used to locate cache paths. When
	// nil, the built-in catalog is loaded on demand.
	Catalog *catalog.Catalog
	// Enabled is the authorize-narrow policy. Recipes are only built for enabled
	// domains. A nil Set (zero value) means "no gating" — used by unit tests and
	// callers that gate elsewhere.
	Enabled config.EnabledDomains
}

// recipeBuilder builds a guided recipe for a catalog entry. ok=false when the
// recipe does not apply (e.g. the cache directory is absent on this machine).
type recipeBuilder func(ctx context.Context, opts RecipeOptions, e catalog.Entry) (Recipe, bool)

// recipeBuilders maps registered recipe ids to their builders. Keys must match
// catalog.RegisteredRecipeIDs so that catalog recipe links stay authoritative.
func recipeBuilders() map[string]recipeBuilder {
	return map[string]recipeBuilder{
		catalog.RecipeRustupToolchains: func(ctx context.Context, opts RecipeOptions, _ catalog.Entry) (Recipe, bool) {
			if !opts.IncludeRustup {
				return Recipe{}, false
			}
			return rustupRecipe(ctx, opts)
		},
		catalog.RecipeGoBuildCache:   goBuildCacheRecipe,
		catalog.RecipeCargoCache:     cargoCacheRecipe,
		catalog.RecipeBrewCleanup:    brewCleanupRecipe,
		catalog.RecipeUVCachePrune:   uvCachePruneRecipe,
		catalog.RecipeGrypeDBResidue: grypeDBResidueRecipe,
		catalog.RecipeVMDiskTrim:     vmDiskTrimRecipe,
	}
}

// BuildRecipes returns guided diagnostic recipes driven by catalog recipe links,
// for enabled domains. A recipe is emitted only when the resolved catalog entry
// links it (authoritative linkage), a registered builder produces it, and the
// entry's location is authoritative (see recipeLocationAuthoritative).
//
// Guided recipes carry tool-global argv (e.g. go clean -cache, rustup toolchain
// …) whose real target is the tool's own canonical location. A user overlay that
// repoints a recipe-bearing built-in entry would make the reported path and the
// command's actual target diverge, so its recipe is suppressed (the entry still
// appears as a diagnostic hotspot at the new location). Dropping a recipe link
// suppresses the recipe too. Never promotes diagnostic mass into prunable state.
func BuildRecipes(ctx context.Context, opts RecipeOptions) []Recipe {
	if err := ctx.Err(); err != nil {
		return []Recipe{}
	}
	cat := opts.Catalog
	if cat == nil {
		var err error
		cat, err = catalog.BuiltIn()
		if err != nil {
			return []Recipe{}
		}
	}
	// Built-in catalog for canonical-location comparison (overlay repoint guard).
	builtIn, err := catalog.BuiltIn()
	if err != nil {
		return []Recipe{}
	}
	builders := recipeBuilders()
	out := make([]Recipe, 0)
	seen := map[string]bool{}
	for _, e := range cat.Entries {
		if !recipeDomainAllowed(opts.Enabled, e.Domain) {
			continue
		}
		if !recipeLocationAuthoritative(builtIn, e) {
			continue
		}
		for _, rid := range e.Recipes {
			if seen[rid] {
				continue
			}
			b, ok := builders[rid]
			if !ok {
				// Link to an unregistered recipe. Config load rejects dangling links;
				// skip defensively here.
				continue
			}
			if r, ok := b(ctx, opts, e); ok {
				seen[rid] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// recipeDomainAllowed reports whether a recipe's domain is authorized. A nil
// policy set means no gating context (defaults handled by the caller).
func recipeDomainAllowed(e config.EnabledDomains, domain string) bool {
	if e.Set == nil {
		return true
	}
	return e.Set[domain]
}

// recipeLocationAuthoritative reports whether entry e may carry a guided recipe.
// Built-in entries always may (their location is the canonical tool location). An
// overlay entry may only if it matches the built-in canonical location for this
// id on the current platform — otherwise the tool-global recipe command would not
// match the reported path, so the recipe is suppressed (the entry remains a
// diagnostic hotspot). A new overlay entry with no built-in counterpart carries
// no canonical location to trust, so it may not emit a guided recipe either.
func recipeLocationAuthoritative(builtIn *catalog.Catalog, e catalog.Entry) bool {
	if e.Source == catalog.SourceBuiltIn {
		return true
	}
	if builtIn == nil {
		return false
	}
	canon, ok := builtIn.EntryByID(e.ID)
	if !ok {
		return false
	}
	cl, cok := canon.LocationFor(runtime.GOOS)
	el, eok := e.LocationFor(runtime.GOOS)
	return cok && eok && cl == el
}

// goBuildCacheRecipe suggests clearing the Go build cache. High refill, low
// regret; `go clean -cache` is the canonical safe command (inert suggestion).
func goBuildCacheRecipe(ctx context.Context, opts RecipeOptions, e catalog.Entry) (Recipe, bool) {
	if err := ctx.Err(); err != nil {
		return Recipe{}, false
	}
	path, ok := entryHotspotPath(opts, e)
	if !ok || !dirExists(path) {
		return Recipe{}, false
	}
	r := Recipe{
		ID:                 catalog.RecipeGoBuildCache,
		Title:              "Go build cache",
		State:              StateDiagnosticOnly,
		RebuildExpectation: rebuildOr(e, RebuildHigh),
		Path:               path,
		SuggestedCommands:  []SuggestedCommand{FormatCommand("go", "clean", "-cache")},
		Rationale: []string{
			"Go build cache is diagnostic-only: spanwit never deletes it.",
			"go clean -cache frees the compiled build cache; it refills on the next build.",
			"High refill, low regret — a safe first cache to clear when reclaiming space.",
		},
		CatalogID: e.ID,
	}
	AnnotateRecipeTaxonomy(&r)
	return r, true
}

// cargoCacheRecipe suggests trimming the Cargo registry/git caches via the
// cargo-cache tool. Medium refill (network re-download); inert suggestion.
func cargoCacheRecipe(ctx context.Context, opts RecipeOptions, e catalog.Entry) (Recipe, bool) {
	if err := ctx.Err(); err != nil {
		return Recipe{}, false
	}
	path, ok := entryHotspotPath(opts, e)
	if !ok || !dirExists(path) {
		return Recipe{}, false
	}
	r := Recipe{
		ID:                 catalog.RecipeCargoCache,
		Title:              "Cargo registry/git cache",
		State:              StateDiagnosticOnly,
		RebuildExpectation: rebuildOr(e, RebuildMedium),
		Path:               path,
		SuggestedCommands: []SuggestedCommand{
			FormatCommand("cargo", "cache", "--autoclean"),
		},
		Rationale: []string{
			"Cargo registry index, source cache, and git checkouts are diagnostic-only: spanwit never deletes them.",
			"cargo cache --autoclean (from the cargo-cache crate) trims cached registry sources; install it with cargo install cargo-cache.",
			"Medium refill: removed registry sources re-download from the network on the next build.",
		},
		CatalogID: e.ID,
	}
	AnnotateRecipeTaxonomy(&r)
	return r, true
}

// brewCleanupRecipe suggests Homebrew's own cleanup, previewed first. brew
// cleanup reaches past the download cache: it also removes outdated installed
// keg versions from the Cellar, so the rationale names that cost. The commands
// are print-only and require operator review before running.
func brewCleanupRecipe(ctx context.Context, opts RecipeOptions, e catalog.Entry) (Recipe, bool) {
	if err := ctx.Err(); err != nil {
		return Recipe{}, false
	}
	path, ok := entryHotspotPath(opts, e)
	if !ok || !dirExists(path) {
		return Recipe{}, false
	}
	r := Recipe{
		ID:                 catalog.RecipeBrewCleanup,
		Title:              "Homebrew cache",
		State:              StateDiagnosticOnly,
		RebuildExpectation: rebuildOr(e, RebuildMedium),
		Path:               path,
		SuggestedCommands: []SuggestedCommand{
			FormatCommand("brew", "cleanup", "--dry-run"),
			FormatCommand("brew", "cleanup"),
		},
		Rationale: []string{
			"Homebrew download cache is diagnostic-only: spanwit never deletes it.",
			"Run brew cleanup --dry-run first and review what it lists before running brew cleanup.",
			"brew cleanup also removes outdated installed versions from the Cellar (pinned formulae are kept); an old version you still rely on comes back only by reinstalling it.",
			"Medium refill: pruned downloads re-fetch on the next install or upgrade.",
		},
		CatalogID: e.ID,
	}
	AnnotateRecipeTaxonomy(&r)
	return r, true
}

// uvCachePruneRecipe suggests pruning unreachable uv cache objects via uv
// itself. Medium refill (re-download on next resolve); the command is
// print-only and requires operator review before running.
func uvCachePruneRecipe(ctx context.Context, opts RecipeOptions, e catalog.Entry) (Recipe, bool) {
	if err := ctx.Err(); err != nil {
		return Recipe{}, false
	}
	path, ok := entryHotspotPath(opts, e)
	if !ok || !dirExists(path) {
		return Recipe{}, false
	}
	r := Recipe{
		ID:                 catalog.RecipeUVCachePrune,
		Title:              "uv cache",
		State:              StateDiagnosticOnly,
		RebuildExpectation: rebuildOr(e, RebuildMedium),
		Path:               path,
		SuggestedCommands: []SuggestedCommand{
			FormatCommand("uv", "cache", "prune"),
		},
		Rationale: []string{
			"uv package cache is diagnostic-only: spanwit never deletes it.",
			"uv cache prune removes unreachable objects via uv itself; review the command before running it.",
			"Medium refill: pruned objects re-download on the next resolve.",
		},
		CatalogID: e.ID,
	}
	AnnotateRecipeTaxonomy(&r)
	return r, true
}

// rebuildOr returns the entry's rebuild expectation, or a default when unset.
func rebuildOr(e catalog.Entry, def string) string {
	if e.RebuildExpectation != "" {
		return e.RebuildExpectation
	}
	return def
}

// entryHotspotPath resolves the absolute home-cache path for a catalog entry on
// the current platform, honoring opts.Home. Uses the entry directly so a user
// overlay that repoints the location is respected.
func entryHotspotPath(opts RecipeOptions, e catalog.Entry) (string, bool) {
	rel, ok := e.LocationFor(runtime.GOOS)
	if !ok {
		return "", false
	}
	home, err := resolveHome(opts)
	if err != nil {
		return "", false
	}
	return filepath.Join(home, filepath.FromSlash(rel)), true
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func resolveHome(opts RecipeOptions) (string, error) {
	if opts.Home != "" {
		return opts.Home, nil
	}
	return os.UserHomeDir()
}

// listRustupToolchainDirs returns toolchain directory basenames under
// $HOME/.rustup/toolchains (or opts.ToolchainDirs when injected).
func listRustupToolchainDirs(opts RecipeOptions) (home string, dirs []string, path string, err error) {
	if opts.ToolchainDirs != nil {
		home, err = resolveHome(opts)
		if err != nil {
			return "", nil, "", err
		}
		path = filepath.Join(home, ".rustup", "toolchains")
		return home, append([]string(nil), opts.ToolchainDirs...), path, nil
	}
	home, err = resolveHome(opts)
	if err != nil {
		return "", nil, "", err
	}
	path = filepath.Join(home, ".rustup", "toolchains")
	entries, readErr := os.ReadDir(path)
	if readErr != nil {
		return home, nil, path, readErr
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	return home, dirs, path, nil
}
