package space

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/catalog"
)

func darwinCacheEntry(id, rel string) catalog.Entry {
	return catalog.Entry{
		ID:           id,
		Label:        id,
		ClassCeiling: catalog.ClassDiagnosticOnly,
		Locations:    map[string]catalog.Location{"darwin": {RelHome: rel}},
		Source:       catalog.SourceBuiltIn,
	}
}

func TestBrewCleanupRecipe_ArgvAndState(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Caches", "Homebrew")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	recipe, ok := brewCleanupRecipe(ctx, RecipeOptions{Home: home},
		darwinCacheEntry("development.system.homebrew", "Library/Caches/Homebrew"))
	if runtime.GOOS != "darwin" {
		if ok {
			t.Fatal("darwin-location recipe must not build off-platform")
		}
		return
	}
	if !ok {
		t.Fatal("brew-cleanup recipe missing for present cache dir")
	}
	if recipe.State != StateDiagnosticOnly {
		t.Errorf("recipe state = %q, want diagnostic-only", recipe.State)
	}
	// Preview first, then the real command: brew cleanup reaches the Cellar,
	// so the operator must see its list before running it.
	cmds := recipe.SuggestedCommands
	if len(cmds) != 2 ||
		cmds[0].Program != "brew" || strings.Join(cmds[0].Args, " ") != "cleanup --dry-run" ||
		cmds[1].Program != "brew" || strings.Join(cmds[1].Args, " ") != "cleanup" {
		t.Errorf("recipe argv = %+v, want [brew cleanup --dry-run] then [brew cleanup]", cmds)
	}
	if recipe.RebuildExpectation != RebuildMedium {
		t.Errorf("rebuild expectation = %q, want medium", recipe.RebuildExpectation)
	}
	joined := strings.Join(recipe.Rationale, "\n")
	if !strings.Contains(joined, "review") || !strings.Contains(joined, "Medium refill") {
		t.Errorf("rationale must name operator review + refill cost:\n%s", joined)
	}
	if !strings.Contains(joined, "--dry-run") || !strings.Contains(joined, "Cellar") {
		t.Errorf("rationale must name the dry-run preview and the Cellar cost:\n%s", joined)
	}

	// Absent cache dir → no recipe (missing is ordinary).
	empty := RecipeOptions{Home: t.TempDir()}
	if _, ok := brewCleanupRecipe(ctx, empty,
		darwinCacheEntry("development.system.homebrew", "Library/Caches/Homebrew")); ok && runtime.GOOS == "darwin" {
		t.Error("brew-cleanup recipe built for a missing dir")
	}
}

func TestUVCachePruneRecipe_ArgvAndState(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Library", "Caches", "uv"), 0o755); err != nil {
		t.Fatal(err)
	}
	recipe, ok := uvCachePruneRecipe(ctx, RecipeOptions{Home: home},
		darwinCacheEntry("development.python.uv-cache", "Library/Caches/uv"))
	if runtime.GOOS != "darwin" {
		if ok {
			t.Fatal("darwin-location recipe must not build off-platform")
		}
		return
	}
	if !ok {
		t.Fatal("uv-cache-prune recipe missing for present cache dir")
	}
	if recipe.State != StateDiagnosticOnly {
		t.Errorf("recipe state = %q, want diagnostic-only", recipe.State)
	}
	if len(recipe.SuggestedCommands) != 1 ||
		recipe.SuggestedCommands[0].Program != "uv" ||
		len(recipe.SuggestedCommands[0].Args) != 2 ||
		recipe.SuggestedCommands[0].Args[0] != "cache" ||
		recipe.SuggestedCommands[0].Args[1] != "prune" {
		t.Errorf("recipe argv = %+v, want [uv cache prune]", recipe.SuggestedCommands)
	}
	joined := strings.Join(recipe.Rationale, "\n")
	if !strings.Contains(joined, "review") || !strings.Contains(joined, "Medium refill") {
		t.Errorf("rationale must name operator review + refill cost:\n%s", joined)
	}
}

// The built-in catalog links homebrew and uv-cache to their recipes; with
// both cache dirs present under a fake home, both recipes are emitted.
func TestBuildRecipes_BrewAndUVLinks(t *testing.T) {
	ctx := context.Background()
	base, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	needed := 0
	for _, id := range []string{"development.system.homebrew", "development.python.uv-cache"} {
		e, ok := base.EntryByID(id)
		if !ok {
			t.Fatalf("catalog entry %q missing", id)
		}
		rel, ok := e.LocationFor(runtime.GOOS)
		if !ok {
			continue
		}
		needed++
		if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if needed != 2 {
		t.Skip("homebrew/uv locations do not apply on this OS")
	}
	got := BuildRecipes(ctx, RecipeOptions{Catalog: base, Home: home})
	if !hasRecipe(got, catalog.RecipeBrewCleanup) {
		t.Errorf("expected brew-cleanup recipe: %#v", got)
	}
	if !hasRecipe(got, catalog.RecipeUVCachePrune) {
		t.Errorf("expected uv-cache-prune recipe: %#v", got)
	}
}
