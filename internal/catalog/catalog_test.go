package catalog

import "testing"

func TestBuiltInLoadsAndIndexes(t *testing.T) {
	cat, err := BuiltIn()
	if err != nil {
		t.Fatalf("BuiltIn: %v", err)
	}
	if cat.Version != 1 {
		t.Fatalf("version: %d", cat.Version)
	}
	dev, ok := cat.DomainByName("development")
	if !ok || dev.Status != StatusCurated {
		t.Fatalf("development domain: %#v ok=%v", dev, ok)
	}
	// media carries curated app-cache entries; ml remains a stub.
	media, ok := cat.DomainByName("media")
	if !ok || media.Status != StatusCurated {
		t.Fatalf("media should be a curated domain: %#v ok=%v", media, ok)
	}
	ml, ok := cat.DomainByName("ml")
	if !ok || ml.Status != StatusStub {
		t.Fatalf("ml should be a stub domain: %#v ok=%v", ml, ok)
	}
}

func TestCargoTargetEntryIsPrunableWithSignature(t *testing.T) {
	cat, _ := BuiltIn()
	e, ok := cat.EntryByID("development.rust.cargo-target")
	if !ok {
		t.Fatal("cargo-target entry missing")
	}
	if e.ClassCeiling != ClassPrunable {
		t.Fatalf("cargo-target ceiling: %q", e.ClassCeiling)
	}
	if e.Signature != "development.rust.cargo-target" {
		t.Fatalf("cargo-target signature link: %q", e.Signature)
	}
	if e.Domain != "development" || e.Ecosystem != "rust" || e.Name != "cargo-target" {
		t.Fatalf("derived segments: %#v", e)
	}
	// Signature-discovered; not a home-cache hotspot.
	if _, ok := e.LocationFor("darwin"); ok {
		t.Fatal("cargo-target must not carry a home-cache location")
	}
}

func TestHotspotsForOSPlatformScoping(t *testing.T) {
	cat, _ := BuiltIn()

	hasID := func(entries []Entry, id string) bool {
		for _, e := range entries {
			if e.ID == id {
				return true
			}
		}
		return false
	}

	darwin := cat.HotspotsForOS("darwin")
	if !hasID(darwin, "development.system.homebrew") || !hasID(darwin, "development.browser.playwright") {
		t.Fatal("darwin hotspots should include homebrew + playwright")
	}
	if !hasID(darwin, "development.go.go-build") {
		t.Fatal("darwin hotspots should include go-build")
	}

	linux := cat.HotspotsForOS("linux")
	if hasID(linux, "development.system.homebrew") || hasID(linux, "development.browser.playwright") {
		t.Fatal("linux hotspots must not include macOS-only homebrew/playwright")
	}
	if !hasID(linux, "development.go.go-build") {
		t.Fatal("linux hotspots should include go-build")
	}

	// go-build resolves a platform-specific location.
	e, _ := cat.EntryByID("development.go.go-build")
	if loc, _ := e.LocationFor("darwin"); loc != "Library/Caches/go-build" {
		t.Fatalf("darwin go-build location: %q", loc)
	}
	if loc, _ := e.LocationFor("linux"); loc != ".cache/go-build" {
		t.Fatalf("linux go-build location: %q", loc)
	}
}

// Darwin capacity entries are darwin-gated, diagnostic-only, and resolve
// home locations on macOS; none may leak into portable defaults.
func TestDarwinCapacityEntriesPlatformGating(t *testing.T) {
	cat, err := BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	darwinIDs := []string{
		"development.browser.vivaldi",
		"development.browser.chrome",
		"development.browser.firefox",
		"development.iot.esphome",
		"development.virtualization.lima",
		"media.audio.spotify",
		"development.node.yarn-cache",
		"development.python.pip-cache",
		"development.python.poetry-cache",
		"development.python.virtualenv-seeds",
		"development.system.trash",
		"development.system.downloads",
		"development.system.software-update",
	}
	inHotspots := map[string]bool{}
	for _, e := range cat.HotspotsForOS("darwin") {
		inHotspots[e.ID] = true
	}
	for _, id := range darwinIDs {
		e, ok := cat.EntryByID(id)
		if !ok {
			t.Errorf("catalog entry %q missing", id)
			continue
		}
		if e.ClassCeiling != ClassDiagnosticOnly {
			t.Errorf("entry %q ceiling = %q, want diagnostic-only", id, e.ClassCeiling)
		}
		if !e.AppliesTo("darwin") || e.AppliesTo("linux") {
			t.Errorf("entry %q must be darwin-only", id)
		}
		if !inHotspots[id] {
			t.Errorf("entry %q missing from darwin hotspots", id)
		}
	}
	linuxHotspots := map[string]bool{}
	for _, e := range cat.HotspotsForOS("linux") {
		linuxHotspots[e.ID] = true
	}
	for _, id := range darwinIDs {
		if linuxHotspots[id] {
			t.Errorf("darwin-only entry %q leaks into linux hotspots", id)
		}
	}
	// uv resolves its corrected per-platform locations.
	uv, ok := cat.EntryByID("development.python.uv-cache")
	if !ok {
		t.Fatal("uv-cache entry missing")
	}
	if loc, _ := uv.LocationFor("darwin"); loc != "Library/Caches/uv" {
		t.Errorf("darwin uv location = %q, want Library/Caches/uv", loc)
	}
	if loc, _ := uv.LocationFor("linux"); loc != ".cache/uv" {
		t.Errorf("linux uv location = %q, want .cache/uv", loc)
	}
}

func TestBuiltInLinksResolve(t *testing.T) {
	cat, _ := BuiltIn()
	sigIDs := map[string]bool{"development.rust.cargo-target": true}
	recipeIDs := map[string]bool{"rustup-toolchains": true, "go-build-cache": true, "cargo-cache": true, "brew-cleanup": true, "uv-cache-prune": true, "grype-db-residue": true, "vm-disk-trim": true}
	if err := cat.ValidateLinks(sigIDs, recipeIDs); err != nil {
		t.Fatalf("built-in links should resolve: %v", err)
	}
	// Empty (non-nil) recipe set → dangling recipe link rejected.
	if err := cat.ValidateLinks(sigIDs, map[string]bool{}); err == nil {
		t.Fatal("expected dangling recipe link error")
	}
	// Empty (non-nil) signature set → dangling signature link rejected.
	if err := cat.ValidateLinks(map[string]bool{}, recipeIDs); err == nil {
		t.Fatal("expected dangling signature link error")
	}
	// nil sets skip that link type.
	if err := cat.ValidateLinks(nil, nil); err != nil {
		t.Fatalf("nil link sets should skip: %v", err)
	}
}

func TestOverlayAddAndReplaceByID(t *testing.T) {
	cat, _ := BuiltIn()
	merged, err := cat.WithOverlay([]Entry{
		{ID: "development.node.pnpm-store", Label: "pnpm store", ClassCeiling: ClassDiagnosticOnly},
		{ID: "development.go.go-pkg", Label: "Overridden go pkg", ClassCeiling: ClassDiagnosticOnly},
	})
	if err != nil {
		t.Fatalf("WithOverlay: %v", err)
	}
	added, ok := merged.EntryByID("development.node.pnpm-store")
	if !ok || added.Source != SourceOverlay {
		t.Fatalf("added overlay entry: %#v ok=%v", added, ok)
	}
	replaced, _ := merged.EntryByID("development.go.go-pkg")
	if replaced.Label != "Overridden go pkg" || replaced.Source != SourceOverlay {
		t.Fatalf("replace-by-id failed: %#v", replaced)
	}
	// Base catalog unchanged.
	base, _ := cat.EntryByID("development.go.go-pkg")
	if base.Label == "Overridden go pkg" {
		t.Fatal("overlay mutated base catalog")
	}
}

func TestOverlayRejectsTrustEscalation(t *testing.T) {
	cat, _ := BuiltIn()
	// New prunable entry → rejected.
	if _, err := cat.WithOverlay([]Entry{
		{ID: "development.custom.mine", Label: "x", ClassCeiling: ClassPrunable},
	}); err == nil {
		t.Fatal("overlay may not create a prunable entry")
	}
	// Replacing the built-in prunable cargo-target with a *prunable* overlay is
	// still rejected (cannot re-assert prunable authority via overlay).
	if _, err := cat.WithOverlay([]Entry{
		{ID: "development.rust.cargo-target", Label: "x", ClassCeiling: ClassPrunable},
	}); err == nil {
		t.Fatal("overlay may not replace a built-in entry with a prunable overlay")
	}
}

// A fail-closed downgrade/relabel of the built-in prunable cargo-target entry is
// allowed (full-entry replacement); it lowers classification only and never
// disables the underlying signature capability.
func TestOverlayAllowsFailClosedDowngrade(t *testing.T) {
	cat, _ := BuiltIn()
	merged, err := cat.WithOverlay([]Entry{
		{ID: "development.rust.cargo-target", Label: "relabeled", ClassCeiling: ClassDiagnosticOnly},
	})
	if err != nil {
		t.Fatalf("fail-closed downgrade should be allowed: %v", err)
	}
	e, _ := merged.EntryByID("development.rust.cargo-target")
	if e.ClassCeiling != ClassDiagnosticOnly || e.Label != "relabeled" || e.Source != SourceOverlay {
		t.Fatalf("downgrade not applied: %#v", e)
	}
}

func TestRegisteredRecipeIDsCoverBuiltInLinks(t *testing.T) {
	cat, _ := BuiltIn()
	reg := RegisteredRecipeIDs()
	for _, e := range cat.Entries {
		for _, rid := range e.Recipes {
			if !reg[rid] {
				t.Fatalf("entry %q links recipe %q missing from RegisteredRecipeIDs", e.ID, rid)
			}
		}
	}
}

func TestOverlayRejectsDupAndUndeclaredDomain(t *testing.T) {
	cat, _ := BuiltIn()
	if _, err := cat.WithOverlay([]Entry{
		{ID: "development.node.a", Label: "a", ClassCeiling: ClassDiagnosticOnly},
		{ID: "development.node.a", Label: "a2", ClassCeiling: ClassDiagnosticOnly},
	}); err == nil {
		t.Fatal("expected duplicate overlay id error")
	}
	if _, err := cat.WithOverlay([]Entry{
		{ID: "nope.node.a", Label: "a", ClassCeiling: ClassDiagnosticOnly},
	}); err == nil {
		t.Fatal("expected undeclared-domain error")
	}
}

// User data and OS-owned locations are explained, never remediated: linking a
// recipe to any of them would turn location into implied deletion guidance.
func TestBuiltIn_UserDataAndSystemEntriesCarryNoRecipes(t *testing.T) {
	cat, err := BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"development.system.trash",
		"development.system.downloads",
		"development.system.software-update",
	} {
		e, ok := cat.EntryByID(id)
		if !ok {
			t.Fatalf("catalog entry %s missing", id)
		}
		if len(e.Recipes) != 0 {
			t.Errorf("%s carries recipes %v; user-data/system entries must have none", id, e.Recipes)
		}
		if e.ClassCeiling != "diagnostic-only" {
			t.Errorf("%s ceiling %q, want diagnostic-only", id, e.ClassCeiling)
		}
	}
}
