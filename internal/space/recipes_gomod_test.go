package space

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/catalog"
	"github.com/3leaps/spanwit/internal/engine"
)

func goModCacheFromHome(t *testing.T, home string) (Recipe, bool) {
	t.Helper()
	base, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range BuildRecipes(context.Background(), RecipeOptions{Catalog: base, Home: home}) {
		if r.ID == catalog.RecipeGoModCache {
			return r, true
		}
	}
	return Recipe{}, false
}

func TestGoModCacheRecipe_EmittedWhenGoPkgPresent(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "go", "pkg", "mod", "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, ok := goModCacheFromHome(t, home)
	if !ok {
		t.Fatal("expected go-mod-cache recipe when ~/go/pkg exists")
	}
	if r.State != StateDiagnosticOnly {
		t.Fatalf("state = %q, want diagnostic-only", r.State)
	}
	if r.CatalogID != "development.go.go-pkg" {
		t.Fatalf("catalog id = %q", r.CatalogID)
	}
	if r.RebuildExpectation != RebuildHigh {
		t.Fatalf("rebuild expectation = %q, want high", r.RebuildExpectation)
	}
	if r.Path != filepath.Join(home, "go", "pkg") {
		t.Fatalf("path = %q", r.Path)
	}
	if len(r.SuggestedCommands) != 1 {
		t.Fatalf("want exactly one command, got %#v", r.SuggestedCommands)
	}
	c := r.SuggestedCommands[0]
	if c.Program != "go" || !slices.Equal(c.Args, []string{"clean", "-modcache"}) {
		t.Fatalf("argv = %q %q, want go clean -modcache", c.Program, c.Args)
	}
	if strings.Contains(c.Display, "-cache ") || strings.HasSuffix(c.Display, " -cache") {
		t.Fatalf("module recipe must not combine the build cache: %q", c.Display)
	}
	rationale := strings.Join(r.Rationale, "\n")
	for _, want := range []string{"never deletes", "downloads it again", "Offline builds", "GOMODCACHE"} {
		if !strings.Contains(rationale, want) {
			t.Errorf("rationale missing %q:\n%s", want, rationale)
		}
	}
}

func TestGoModCacheRecipe_AbsentDirNoRecipe(t *testing.T) {
	home := t.TempDir()
	// The build cache's neighbour Caches/go-mod (TASK-016) is a different
	// directory and must not trigger this recipe.
	if err := os.MkdirAll(filepath.Join(home, "Library", "Caches", "go-mod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r, ok := goModCacheFromHome(t, home); ok {
		t.Fatalf("no ~/go/pkg: recipe must be absent, got %#v", r)
	}
}

func TestGoModCacheRecipe_LinksGoPkgHotspot(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "go", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, ok := goModCacheFromHome(t, home)
	if !ok {
		t.Fatal("expected recipe")
	}
	hotspots := []Entry{
		{Path: filepath.Join(home, "go", "pkg"), CatalogID: "development.go.go-pkg"},
		{Path: filepath.Join(home, "Library", "Caches", "go-mod"), CatalogID: "development.go.go-mod-download"},
	}
	linkHotspotRecipes(hotspots, []Recipe{r})
	if got := hotspots[0].RecipeIDs; !slices.Equal(got, []string{catalog.RecipeGoModCache}) {
		t.Fatalf("go-pkg hotspot recipes = %v", got)
	}
	if hotspots[1].RecipeIDs != nil {
		t.Fatalf("a different catalog id must not inherit go-mod-cache: %v", hotspots[1].RecipeIDs)
	}
}

// Recipes are print-only. The handoff is built only from verified signature
// candidates (HandoffInput), and --execute consumes only the exact plan
// (ExactPlan -> engine.ExactPlanInput). None of those types can carry a recipe
// or its argv, so a recipe command cannot reach the execute path.
func TestRecipesNotReachableFromHandoff(t *testing.T) {
	recipeT := reflect.TypeOf(Recipe{})
	cmdT := reflect.TypeOf(SuggestedCommand{})
	var walk func(t2 reflect.Type, path string, seen map[reflect.Type]bool)
	walk = func(t2 reflect.Type, path string, seen map[reflect.Type]bool) {
		for t2.Kind() == reflect.Pointer || t2.Kind() == reflect.Slice || t2.Kind() == reflect.Array || t2.Kind() == reflect.Map {
			t2 = t2.Elem()
		}
		if t2 == recipeT || t2 == cmdT {
			t.Fatalf("%s carries %s; recipe argv must stay out of the execute path", path, t2)
		}
		if t2.Kind() != reflect.Struct || seen[t2] {
			return
		}
		seen[t2] = true
		for i := 0; i < t2.NumField(); i++ {
			f := t2.Field(i)
			walk(f.Type, path+"."+f.Name, seen)
		}
	}
	walk(reflect.TypeOf(HandoffInput{}), "HandoffInput", map[reflect.Type]bool{})
	walk(reflect.TypeOf(ExactPlan{}), "ExactPlan", map[reflect.Type]bool{})
	walk(reflect.TypeOf(engine.ExactPlanInput{}), "engine.ExactPlanInput", map[reflect.Type]bool{})
}
