package space

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testTriple = "-aarch64-apple-darwin"

// codexPackage builds <home>/.codex/packages/<name>/releases/<versions> with
// current pointing at currentVersion, and returns the releases directory.
func codexPackage(t *testing.T, home, name, currentVersion string, versions ...string) string {
	t.Helper()
	pkg := filepath.Join(home, ".codex", "packages", name)
	releases := filepath.Join(pkg, "releases")
	for _, v := range versions {
		mkdirs(t, filepath.Join(releases, v+testTriple, "bin"))
	}
	if currentVersion != "" {
		if err := os.Symlink(filepath.Join(releases, currentVersion+testTriple), filepath.Join(pkg, "current")); err != nil {
			t.Fatal(err)
		}
	}
	return releases
}

func codexRecipe(t *testing.T, home string) (Recipe, bool) {
	t.Helper()
	return codexReleasesRecipe(context.Background(), RecipeOptions{Home: home},
		defaultCacheEntry("development.agent.codex-packages", ".codex/packages"))
}

func removedPaths(r Recipe) []string {
	var out []string
	for _, c := range r.SuggestedCommands {
		if c.Program == "rm" && len(c.Args) > 0 {
			out = append(out, c.Args[len(c.Args)-1])
		}
	}
	return out
}

func TestCodexReleasesRecipe_KeepsCurrentAndNewest(t *testing.T) {
	home := t.TempDir()
	rel := codexPackage(t, home, "standalone", "0.162.1",
		"0.153.2", "0.160.1", "0.162.0", "0.162.1", "0.9.10")
	r, ok := codexRecipe(t, home)
	if !ok {
		t.Fatal("recipe missing despite superseded releases")
	}
	if r.State != StateDiagnosticOnly {
		t.Fatalf("state = %q, want diagnostic-only", r.State)
	}
	got := removedPaths(r)
	want := []string{
		filepath.Join(rel, "0.153.2"+testTriple),
		filepath.Join(rel, "0.160.1"+testTriple),
		filepath.Join(rel, "0.9.10"+testTriple),
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("removed = %v, want %v (numeric version order, newest 2 kept)", got, want)
	}
	for _, k := range []string{"0.162.1", "0.162.0"} {
		if !slices.Contains(r.Keep, filepath.Join(rel, k+testTriple)) {
			t.Fatalf("keep %v missing %s", r.Keep, k)
		}
	}
	if r.SuggestedCommands[0].Program != "lsof" {
		t.Fatalf("first step must be the open-executable check: %+v", r.SuggestedCommands[0])
	}
}

// current is kept even when it is not among the newest releases (a pinned or
// rolled-back install).
func TestCodexReleasesRecipe_KeepsOlderCurrent(t *testing.T) {
	home := t.TempDir()
	rel := codexPackage(t, home, "standalone", "0.150.0",
		"0.150.0", "0.151.0", "0.160.0", "0.161.0")
	r, ok := codexRecipe(t, home)
	if !ok {
		t.Fatal("recipe missing")
	}
	got := removedPaths(r)
	if slices.Contains(got, filepath.Join(rel, "0.150.0"+testTriple)) {
		t.Fatalf("current must never be suggested for removal: %v", got)
	}
	if !slices.Equal(got, []string{filepath.Join(rel, "0.151.0"+testTriple)}) {
		t.Fatalf("removed = %v, want only 0.151.0", got)
	}
}

// Only plain release-shaped directories are ever named; symlinks, files and
// other names under releases, and anything outside releases, are not.
func TestCodexReleasesRecipe_OnlyReleaseShapedDirectories(t *testing.T) {
	home := t.TempDir()
	rel := codexPackage(t, home, "standalone", "0.162.1", "0.1.0", "0.2.0", "0.162.0", "0.162.1")
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(rel, "0.0.1"+testTriple)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "0.0.2"+testTriple), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mkdirs(t, filepath.Join(rel, "notes"), filepath.Join(rel, "0.0.3"+testTriple+" ; rm -rf ~"))
	mkdirs(t, filepath.Join(home, ".codex", "sessions", "2026"))

	r, ok := codexRecipe(t, home)
	if !ok {
		t.Fatal("recipe missing")
	}
	want := []string{filepath.Join(rel, "0.1.0"+testTriple), filepath.Join(rel, "0.2.0"+testTriple)}
	if got := removedPaths(r); !slices.Equal(got, want) {
		t.Fatalf("removed = %v, want %v", got, want)
	}
	for _, c := range r.SuggestedCommands {
		if strings.Contains(c.Display, "sessions") || strings.Contains(c.Display, elsewhere) {
			t.Fatalf("command reaches outside releases: %s", c.Display)
		}
	}
}

// An unfamiliar layout produces no suggestion: no current link, a current
// that is not a symlink, or a current pointing outside its own releases.
func TestCodexReleasesRecipe_UnfamiliarLayoutIsOmitted(t *testing.T) {
	cases := map[string]func(t *testing.T, home string){
		"no current": func(t *testing.T, home string) {
			codexPackage(t, home, "standalone", "", "0.1.0", "0.2.0", "0.3.0", "0.4.0")
		},
		"current is a directory": func(t *testing.T, home string) {
			codexPackage(t, home, "standalone", "", "0.1.0", "0.2.0", "0.3.0", "0.4.0")
			mkdirs(t, filepath.Join(home, ".codex", "packages", "standalone", "current"))
		},
		"current points elsewhere": func(t *testing.T, home string) {
			codexPackage(t, home, "standalone", "", "0.1.0", "0.2.0", "0.3.0", "0.4.0")
			if err := os.Symlink(t.TempDir(), filepath.Join(home, ".codex", "packages", "standalone", "current")); err != nil {
				t.Fatal(err)
			}
		},
		// Inside releases but not a release-shaped directory: only the
		// current-found check rejects it.
		"current points at a non-release entry in releases": func(t *testing.T, home string) {
			rel := codexPackage(t, home, "standalone", "", "0.1.0", "0.2.0", "0.3.0", "0.4.0")
			mkdirs(t, filepath.Join(rel, "staging"))
			if err := os.Symlink(filepath.Join(rel, "staging"), filepath.Join(home, ".codex", "packages", "standalone", "current")); err != nil {
				t.Fatal(err)
			}
		},
		// A dangling current into releases: the same check rejects it.
		"current dangles into releases": func(t *testing.T, home string) {
			rel := codexPackage(t, home, "standalone", "", "0.1.0", "0.2.0", "0.3.0", "0.4.0")
			if err := os.Symlink(filepath.Join(rel, "9.9.9"+testTriple), filepath.Join(home, ".codex", "packages", "standalone", "current")); err != nil {
				t.Fatal(err)
			}
		},
		// A regular file named current: only the symlink check rejects it.
		"current is a file": func(t *testing.T, home string) {
			rel := codexPackage(t, home, "standalone", "", "0.1.0", "0.2.0", "0.3.0", "0.4.0")
			if err := os.WriteFile(filepath.Join(home, ".codex", "packages", "standalone", "current"),
				[]byte(filepath.Join(rel, "0.4.0"+testTriple)), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"releases is a symlink": func(t *testing.T, home string) {
			real := filepath.Join(t.TempDir(), "releases")
			for _, v := range []string{"0.1.0", "0.2.0", "0.3.0", "0.4.0"} {
				mkdirs(t, filepath.Join(real, v+testTriple))
			}
			pkg := filepath.Join(home, ".codex", "packages", "standalone")
			mkdirs(t, pkg)
			if err := os.Symlink(real, filepath.Join(pkg, "releases")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(pkg, "releases", "0.4.0"+testTriple), filepath.Join(pkg, "current")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, build := range cases {
		home := t.TempDir()
		build(t, home)
		if r, ok := codexRecipe(t, home); ok {
			t.Errorf("%s: unfamiliar layout must produce no recipe: %+v", name, r.SuggestedCommands)
		}
	}
}

func TestCodexReleasesRecipe_OmittedWhenNothingSuperseded(t *testing.T) {
	home := t.TempDir()
	codexPackage(t, home, "standalone", "0.162.1", "0.162.0", "0.162.1")
	if _, ok := codexRecipe(t, home); ok {
		t.Fatal("no recipe when only current and the newest releases exist")
	}
	if _, ok := codexRecipe(t, t.TempDir()); ok {
		t.Fatal("no recipe without a Codex packages directory")
	}
}

// Packages are independent: each keeps its own current and newest releases.
func TestCodexReleasesRecipe_PerPackage(t *testing.T) {
	home := t.TempDir()
	a := codexPackage(t, home, "standalone", "0.3.0", "0.1.0", "0.2.0", "0.3.0")
	b := codexPackage(t, home, "app-server-daemon", "0.3.0", "0.2.0", "0.3.0")
	r, ok := codexRecipe(t, home)
	if !ok {
		t.Fatal("recipe missing")
	}
	if got := removedPaths(r); !slices.Equal(got, []string{filepath.Join(a, "0.1.0"+testTriple)}) {
		t.Fatalf("removed = %v; %s keeps both of its releases", got, b)
	}
}
