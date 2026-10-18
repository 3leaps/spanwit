package space

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/3leaps/spanwit/internal/catalog"
)

// codexReleaseName is the exact shape of a Codex installed release directory:
// a dotted numeric version and a target triple. A name of any other shape is
// never emitted as a command argument.
var codexReleaseName = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)-[a-z0-9_]+(-[a-z0-9_]+)+$`)

// codexKeepNewest is how many of the newest releases per package stay out of
// the suggestions besides current. A long-running session keeps executing the
// release it started from, so the most recent superseded releases are often
// still in use.
const codexKeepNewest = 2

// codexReleasesRecipe lists superseded Codex release directories under
// <home>/.codex/packages/<package>/releases. It is propose-only: it never marks
// a path prunable, never names current, its link target or the newest
// releases, never names anything outside a releases directory, and is omitted
// when nothing is superseded. Each suggested command removes one exact release.
func codexReleasesRecipe(ctx context.Context, opts RecipeOptions, e catalog.Entry) (Recipe, bool) {
	if err := ctx.Err(); err != nil {
		return Recipe{}, false
	}
	path, ok := entryHotspotPath(opts, e)
	if !ok || !filepath.IsAbs(path) || !plainDir(path) {
		return Recipe{}, false
	}
	packages, err := os.ReadDir(path)
	if err != nil {
		return Recipe{}, false
	}
	var keep, superseded []string
	for _, pkg := range packages {
		if pkg.Type()&os.ModeSymlink != 0 || !pkg.IsDir() {
			continue
		}
		k, s := codexReleaseLayout(filepath.Join(path, pkg.Name()))
		keep = append(keep, k...)
		superseded = append(superseded, s...)
	}
	if len(superseded) == 0 {
		return Recipe{}, false
	}
	sort.Strings(keep)
	sort.Strings(superseded)
	commands := []SuggestedCommand{FormatCommand("lsof", "-c", "codex", "-a", "-d", "txt", "-Fn")}
	for _, dir := range superseded {
		commands = append(commands, FormatCommand("rm", "-rf", "--", dir))
	}
	r := Recipe{
		ID:                 catalog.RecipeCodexReleases,
		Title:              "Codex superseded releases",
		State:              StateDiagnosticOnly,
		RebuildExpectation: rebuildOr(e, RebuildLow),
		Path:               path,
		Keep:               keep,
		SuggestedCommands:  commands,
		Rationale: []string{
			"Codex releases are diagnostic-only: spanwit never deletes them.",
			"Codex keeps every downloaded release beside a current link; new sessions launch current only.",
			fmt.Sprintf("Kept: current, its link target and the newest %d releases of each package.", codexKeepNewest),
			"A running session keeps executing the release it started from, which may be older than the newest kept. The lsof step lists the release executables codex processes have open; skip any release directory it names.",
			"Sessions, history and settings under the Codex home are out of scope.",
		},
		CatalogID: e.ID,
	}
	AnnotateRecipeTaxonomy(&r)
	return r, true
}

// codexReleaseLayout splits one package's releases into kept and superseded
// directories. Only plain directories matching the release shape count. The
// package is skipped entirely (nothing superseded) unless current is a symlink
// that resolves to one of its own release directories, so an unfamiliar
// layout never produces a suggestion.
func codexReleaseLayout(pkgDir string) (keep, superseded []string) {
	releasesDir := filepath.Join(pkgDir, "releases")
	if !plainDir(releasesDir) {
		return nil, nil
	}
	currentLink := filepath.Join(pkgDir, "current")
	info, err := os.Lstat(currentLink)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return nil, nil
	}
	target, err := os.Readlink(currentLink)
	if err != nil {
		return nil, nil
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(pkgDir, target)
	}
	target = filepath.Clean(target)
	if filepath.Dir(target) != releasesDir {
		return nil, nil
	}
	entries, err := os.ReadDir(releasesDir)
	if err != nil {
		return nil, nil
	}
	type release struct {
		path    string
		version [3]int
	}
	var releases []release
	currentFound := false
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		m := codexReleaseName.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}
		var v [3]int
		for i := range v {
			v[i], _ = strconv.Atoi(m[i+1])
		}
		full := filepath.Join(releasesDir, entry.Name())
		if full == target {
			currentFound = true
		}
		releases = append(releases, release{path: full, version: v})
	}
	if !currentFound {
		return nil, nil
	}
	sort.Slice(releases, func(i, j int) bool {
		a, b := releases[i].version, releases[j].version
		if a != b {
			return a[0] > b[0] || (a[0] == b[0] && (a[1] > b[1] || (a[1] == b[1] && a[2] > b[2])))
		}
		return releases[i].path > releases[j].path
	})
	for i, r := range releases {
		if r.path == target || i < codexKeepNewest {
			keep = append(keep, r.path)
			continue
		}
		superseded = append(superseded, r.path)
	}
	return keep, superseded
}

// plainDir reports whether path is a directory reached without following a
// symlink at its last component.
func plainDir(path string) bool {
	// #nosec G703 -- a catalog-resolved location (or a child of one), only lstat'ed; the result decides whether a print-only recipe is offered.
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}
