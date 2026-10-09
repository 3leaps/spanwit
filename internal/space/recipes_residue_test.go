package space

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/catalog"
)

func defaultCacheEntry(id, rel string) catalog.Entry {
	return catalog.Entry{
		ID:           id,
		ClassCeiling: catalog.ClassDiagnosticOnly,
		Locations:    map[string]catalog.Location{"default": {RelHome: rel}},
	}
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGrypeDBResidueRecipe_ListsOnlyResidueNeverLiveDB(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	db := filepath.Join(home, ".cache", "grype", "db")
	live := filepath.Join(db, "6")
	residueA := filepath.Join(db, "grype-db-download111")
	residueB := filepath.Join(db, "grype-db-download222")
	mkdirs(t, live, residueA, residueB)
	if err := os.WriteFile(filepath.Join(live, "vulnerability.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file and a symlink with the residue prefix are never listed.
	if err := os.WriteFile(filepath.Join(db, "grype-db-download-file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(live, filepath.Join(db, "grype-db-download-link")); err != nil {
		t.Fatal(err)
	}

	recipe, ok := grypeDBResidueRecipe(ctx, RecipeOptions{Home: home},
		defaultCacheEntry("development.security.grype", ".cache/grype"))
	if !ok {
		t.Fatal("recipe missing despite residue directories")
	}
	if recipe.State != StateDiagnosticOnly {
		t.Errorf("state = %q, want diagnostic-only", recipe.State)
	}
	if len(recipe.SuggestedCommands) != 2 {
		t.Fatalf("commands = %+v, want one rm per residue directory", recipe.SuggestedCommands)
	}
	want := []string{residueA, residueB}
	for i, cmd := range recipe.SuggestedCommands {
		if cmd.Program != "rm" || len(cmd.Args) != 3 || cmd.Args[0] != "-rf" || cmd.Args[1] != "--" || cmd.Args[2] != want[i] {
			t.Errorf("command %d = %+v, want rm -rf -- %s", i, cmd, want[i])
		}
	}
	for _, cmd := range recipe.SuggestedCommands {
		for _, arg := range cmd.Args {
			if arg == live || strings.HasPrefix(arg, live+string(filepath.Separator)) {
				t.Fatalf("live database targeted: %+v", cmd)
			}
		}
	}
	if len(recipe.Keep) != 1 || recipe.Keep[0] != live {
		t.Errorf("keep = %v, want [%s]", recipe.Keep, live)
	}
}

func TestGrypeDBResidueRecipe_OmittedWithoutResidue(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, filepath.Join(home, ".cache", "grype", "db", "6"))
	if _, ok := grypeDBResidueRecipe(context.Background(), RecipeOptions{Home: home},
		defaultCacheEntry("development.security.grype", ".cache/grype")); ok {
		t.Fatal("recipe must be omitted when only the live database exists")
	}
}

func TestVMDiskTrimRecipe_ProposeOnlyAndKeepsDisk(t *testing.T) {
	home := t.TempDir()
	disks := filepath.Join(home, ".colima", "_lima", "_disks")
	mkdirs(t, filepath.Join(disks, "colima"))
	recipe, ok := vmDiskTrimRecipe(context.Background(), RecipeOptions{Home: home},
		defaultCacheEntry("development.virtualization.colima", ".colima/_lima/_disks"))
	if !ok {
		t.Fatal("recipe missing for present VM disk directory")
	}
	if recipe.State != StateDiagnosticOnly {
		t.Errorf("state = %q, want diagnostic-only", recipe.State)
	}
	last := recipe.SuggestedCommands[len(recipe.SuggestedCommands)-1]
	if last.Program != "colima" || !strings.Contains(last.Display, "fstrim") {
		t.Errorf("last step = %+v, want the in-guest fstrim", last)
	}
	for _, cmd := range recipe.SuggestedCommands {
		if cmd.Program == "rm" || strings.Contains(cmd.Display, "volume prune") {
			t.Fatalf("recipe must not delete disks or prune volumes: %s", cmd.Display)
		}
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, disks) {
				t.Fatalf("recipe must never target the disk path: %+v", cmd)
			}
		}
	}
	if len(recipe.Keep) != 1 || recipe.Keep[0] != disks {
		t.Errorf("keep = %v, want [%s]", recipe.Keep, disks)
	}
}
