package space

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/3leaps/spanwit/internal/catalog"
)

func hasRecipe(recipes []Recipe, id string) bool {
	for _, r := range recipes {
		if r.ID == id {
			return true
		}
	}
	return false
}

// recipeBuilders must register exactly the catalog's recipe-id vocabulary so
// links stay authoritative and none dangle.
func TestRecipeBuildersMatchRegistry(t *testing.T) {
	reg := catalog.RegisteredRecipeIDs()
	b := recipeBuilders()
	if len(reg) != len(b) {
		t.Fatalf("builder/registry size mismatch: builders=%d registry=%d", len(b), len(reg))
	}
	for id := range reg {
		if _, ok := b[id]; !ok {
			t.Fatalf("registry recipe %q has no builder", id)
		}
	}
	for id := range b {
		if !reg[id] {
			t.Fatalf("builder %q not in RegisteredRecipeIDs", id)
		}
	}
}

// A recipe is emitted only when the resolved catalog entry links it. Dropping
// the link via a full-entry overlay suppresses the recipe even when the cache
// directory still exists.
func TestBuildRecipes_LinkDrivenEmission(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	base, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	e, ok := base.EntryByID("development.go.go-build")
	if !ok {
		t.Fatal("go-build entry missing")
	}
	rel, ok := e.LocationFor(runtime.GOOS)
	if !ok {
		t.Skip("no go-build location for this OS")
	}
	if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatal(err)
	}

	// Built-in catalog links go-build-cache → recipe emitted (dir exists).
	got := BuildRecipes(ctx, RecipeOptions{Catalog: base, Home: home})
	if !hasRecipe(got, catalog.RecipeGoBuildCache) {
		t.Fatalf("expected go-build-cache recipe from built-in links: %#v", got)
	}

	// Overlay keeps the location but drops the recipe link → recipe suppressed.
	overlaid, err := base.WithOverlay([]catalog.Entry{{
		ID:           "development.go.go-build",
		Label:        "Go build cache",
		ClassCeiling: catalog.ClassDiagnosticOnly,
		Locations:    map[string]catalog.Location{"default": {RelHome: rel}},
		// Recipes intentionally omitted (link dropped).
	}})
	if err != nil {
		t.Fatalf("WithOverlay: %v", err)
	}
	after := BuildRecipes(ctx, RecipeOptions{Catalog: overlaid, Home: home})
	if hasRecipe(after, catalog.RecipeGoBuildCache) {
		t.Fatalf("overlay dropped the recipe link but recipe still emitted: %#v", after)
	}
}

// Repointing a recipe-bearing built-in entry suppresses its (tool-global) recipe
// so the reported path and the command's real target can never diverge. The
// rustup builder would otherwise emit from injected toolchain dirs regardless of
// location, so this proves the guard, not just a missing directory.
func TestBuildRecipes_RustupRepointSuppressed(t *testing.T) {
	ctx := context.Background()
	base, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	overlaid, err := base.WithOverlay([]catalog.Entry{{
		ID:           "development.rust.rustup",
		Label:        "Rustup (custom location)",
		ClassCeiling: catalog.ClassDiagnosticOnly,
		Locations:    map[string]catalog.Location{"default": {RelHome: ".rustup-custom"}},
		Recipes:      []string{catalog.RecipeRustupToolchains},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := BuildRecipes(ctx, RecipeOptions{
		Catalog:       overlaid,
		Home:          t.TempDir(),
		IncludeRustup: true,
		ToolchainDirs: []string{"stable-x86_64-apple-darwin"},
	})
	if hasRecipe(got, catalog.RecipeRustupToolchains) {
		t.Fatalf("repointed rustup entry must suppress its recipe: %#v", got)
	}
}

func TestBuildRecipes_GoBuildRepointSuppressed(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	// Cache dir exists at the *repointed* location, so only the guard suppresses it.
	if err := os.MkdirAll(filepath.Join(home, "custom-go-cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	base, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	overlaid, err := base.WithOverlay([]catalog.Entry{{
		ID:           "development.go.go-build",
		Label:        "Go build (custom location)",
		ClassCeiling: catalog.ClassDiagnosticOnly,
		Locations:    map[string]catalog.Location{"default": {RelHome: "custom-go-cache"}},
		Recipes:      []string{catalog.RecipeGoBuildCache},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := BuildRecipes(ctx, RecipeOptions{Catalog: overlaid, Home: home})
	if hasRecipe(got, catalog.RecipeGoBuildCache) {
		t.Fatalf("repointed go-build entry must suppress its recipe: %#v", got)
	}
}

// A same-location relabel is a benign diagnostic overlay: the recipe stays.
func TestBuildRecipes_SameLocationRelabelKept(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	base, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	e, _ := base.EntryByID("development.go.go-build")
	rel, ok := e.LocationFor(runtime.GOOS)
	if !ok {
		t.Skip("no go-build location for this OS")
	}
	if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	overlaid, err := base.WithOverlay([]catalog.Entry{{
		ID:           "development.go.go-build",
		Label:        "Go build (relabeled)",
		ClassCeiling: catalog.ClassDiagnosticOnly,
		Locations:    map[string]catalog.Location{"default": {RelHome: rel}},
		Recipes:      []string{catalog.RecipeGoBuildCache},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := BuildRecipes(ctx, RecipeOptions{Catalog: overlaid, Home: home})
	if !hasRecipe(got, catalog.RecipeGoBuildCache) {
		t.Fatalf("same-location relabel should keep the recipe: %#v", got)
	}
}
