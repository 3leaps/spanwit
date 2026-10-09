package space

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/3leaps/spanwit/internal/catalog"
	"github.com/3leaps/spanwit/internal/engine"
)

// grypeResidueName is the exact shape of the temporary directories grype
// creates beside its live database while downloading an update: the
// grype-db-download prefix followed by digits. Interrupted or superseded
// downloads are left behind and never removed by grype itself. A name of any
// other shape is never emitted as a command argument, even when it starts with
// the prefix.
var grypeResidueName = regexp.MustCompile(`^grype-db-download[0-9]+$`)

// grypeDBResidueRecipe lists leftover grype download directories under
// <cache>/db, beside the live schema directory. It is propose-only: it never
// marks a path prunable, never names the live database, and is omitted when no
// residue exists. Each suggested command removes one exact residue directory.
func grypeDBResidueRecipe(ctx context.Context, opts RecipeOptions, e catalog.Entry) (Recipe, bool) {
	if err := ctx.Err(); err != nil {
		return Recipe{}, false
	}
	path, ok := entryHotspotPath(opts, e)
	if !ok || !filepath.IsAbs(path) || !dirExists(path) {
		return Recipe{}, false
	}
	residue, live := grypeDBLayout(filepath.Join(path, "db"))
	if len(residue) == 0 {
		return Recipe{}, false
	}
	commands := make([]SuggestedCommand, 0, len(residue))
	for _, dir := range residue {
		commands = append(commands, FormatCommand("rm", "-rf", "--", dir))
	}
	rationale := []string{
		"grype download residue is diagnostic-only: spanwit never deletes it.",
		"Each grype-db-download* directory is an interrupted or superseded database download; grype does not clean them up.",
	}
	if len(live) > 0 {
		rationale = append(rationale, "Keep the live database: "+strings.Join(live, ", ")+".")
	} else {
		rationale = append(rationale, "No live schema directory was found; run grype db update before removing residue.")
	}
	r := Recipe{
		ID:                 catalog.RecipeGrypeDBResidue,
		Title:              "grype database download residue",
		State:              StateDiagnosticOnly,
		RebuildExpectation: rebuildOr(e, RebuildLow),
		Path:               filepath.Join(path, "db"),
		Keep:               live,
		SuggestedCommands:  commands,
		Rationale:          rationale,
		CatalogID:          e.ID,
	}
	AnnotateRecipeTaxonomy(&r)
	return r, true
}

// grypeDBLayout splits the entries of a grype db directory into download
// residue and live schema directories. Only plain directories count; a symlink
// or file never appears in either list.
func grypeDBLayout(dbDir string) (residue []string, live []string) {
	entries, err := os.ReadDir(dbDir)
	if err != nil {
		return nil, nil
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		full := filepath.Join(dbDir, entry.Name())
		switch {
		case grypeResidueName.MatchString(entry.Name()):
			residue = append(residue, full)
		case isSchemaDir(entry.Name()):
			live = append(live, full)
		}
	}
	sort.Strings(residue)
	sort.Strings(live)
	return residue, live
}

func isSchemaDir(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// vmDiskTrimRecipe explains how to reclaim space held inside colima/lima VM
// data disks. The disks are never targets; every step runs inside the VM, and
// only the final trim returns freed blocks to the host volume.
func vmDiskTrimRecipe(ctx context.Context, opts RecipeOptions, e catalog.Entry) (Recipe, bool) {
	if err := ctx.Err(); err != nil {
		return Recipe{}, false
	}
	path, ok := entryHotspotPath(opts, e)
	if !ok || !dirExists(path) {
		return Recipe{}, false
	}
	r := Recipe{
		ID:                 catalog.RecipeVMDiskTrim,
		Title:              "VM data disk (reclaim inside the VM, then trim)",
		State:              StateDiagnosticOnly,
		RebuildExpectation: rebuildOr(e, RebuildLow),
		Path:               path,
		Keep:               []string{path},
		SuggestedCommands: []SuggestedCommand{
			FormatCommand("docker", "system", "df"),
			FormatCommand("docker", "image", "prune", "-a"),
			FormatCommand("docker", "builder", "prune"),
			FormatCommand("colima", "ssh", "--", "sudo", "fstrim", "-av"),
		},
		Rationale: append(vmDiskAllocationNotes(path), []string{
			"VM disks are diagnostic-only: spanwit never deletes or resizes them.",
			"Review docker system df first; image and build-cache prunes re-download or rebuild on demand.",
			"Volumes can hold data and are not suggested here; review docker volume ls before any volume prune.",
			"The disk is sparse: space freed inside the VM returns to the host only after fstrim in the guest.",
		}...),
		CatalogID: e.ID,
	}
	AnnotateRecipeTaxonomy(&r)
	return r, true
}

// vmDiskAllocationNotes reports, across the disk files directly under each VM's
// disk directory, the total logical size and the bytes actually allocated on
// the host.
// The hotspot size is logical; for a sparse disk the allocated figure is the
// real host cost. Symlinks are never followed.
func vmDiskAllocationNotes(disksDir string) []string {
	vms, err := os.ReadDir(disksDir)
	if err != nil {
		return nil
	}
	var disks int
	var logicalTotal, allocatedTotal int64
	for _, vm := range vms {
		if !vm.IsDir() || vm.Type()&os.ModeSymlink != 0 {
			continue
		}
		files, err := os.ReadDir(filepath.Join(disksDir, vm.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Type()&os.ModeSymlink != 0 {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			logical, allocated, ok := fileAllocation(info)
			if !ok || logical == 0 {
				continue
			}
			disks++
			logicalTotal += logical
			allocatedTotal += allocated
		}
	}
	if disks == 0 {
		return nil
	}
	// Aggregate only: VM and disk file names are machine data and never reach
	// the report text.
	return []string{fmt.Sprintf("%d VM disk file(s): %s logical, %s allocated on the host.",
		disks, engine.HumanSize(logicalTotal), engine.HumanSize(allocatedTotal))}
}
