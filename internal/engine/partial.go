package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// safeSegmentPattern matches a single path segment safe for partial reclaim
// discovery (no separators, no "..", no shell metacharacters).
var safeSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// cargoIncrementalName is the only partial leaf admitted in v1.
const cargoIncrementalName = "incremental"

// IsSafePathSegment reports whether s is a single safe relative path segment.
func IsSafePathSegment(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 128 {
		return false
	}
	if strings.ContainsAny(s, `/\`) {
		return false
	}
	return safeSegmentPattern.MatchString(s)
}

// FindCargoIncrementalDirs locates incremental cache directories under a
// signature-verified cargo target root. Paths are absolute. Discovery never
// follows symlinks and only walks safe single-segment names.
//
// Admitted shapes (relative to target):
//   - incremental
//   - {profile}/incremental
//   - {triple}/{profile}/incremental
func FindCargoIncrementalDirs(targetPath string) ([]string, error) {
	targetPath = filepath.Clean(targetPath)
	info, err := os.Lstat(targetPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("target is not a plain directory")
	}

	var out []string
	// Depth 0: target/incremental
	if p, ok := plainChildDir(targetPath, cargoIncrementalName); ok {
		out = append(out, p)
	}

	// Depth 1: target/{seg}/incremental
	entries, err := os.ReadDir(targetPath)
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		name := e.Name()
		if name == cargoIncrementalName {
			continue // already handled
		}
		if !IsSafePathSegment(name) {
			continue
		}
		// ReadDir entries that are symlinks: Type may show symlink; reject.
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if !e.IsDir() {
			continue
		}
		mid := filepath.Join(targetPath, name)
		// Confirm mid is not a symlink (defense in depth).
		if mi, err := os.Lstat(mid); err != nil || !mi.IsDir() || mi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if p, ok := plainChildDir(mid, cargoIncrementalName); ok {
			out = append(out, p)
			continue
		}
		// Depth 2: target/{triple}/{profile}/incremental
		subEntries, err := os.ReadDir(mid)
		if err != nil {
			continue
		}
		for _, se := range subEntries {
			sname := se.Name()
			if !IsSafePathSegment(sname) || se.Type()&os.ModeSymlink != 0 || !se.IsDir() {
				continue
			}
			leafParent := filepath.Join(mid, sname)
			if li, err := os.Lstat(leafParent); err != nil || !li.IsDir() || li.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if p, ok := plainChildDir(leafParent, cargoIncrementalName); ok {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

func plainChildDir(parent, name string) (string, bool) {
	if !IsSafePathSegment(name) {
		return "", false
	}
	p := filepath.Join(parent, name)
	info, err := os.Lstat(p)
	if err != nil {
		return "", false
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	// Full path must have no symlink components.
	if err := ValidatePathNoSymlinkComponents(p); err != nil {
		return "", false
	}
	return p, true
}

// ValidateIncrementalUnderParent checks that candidate is a safe incremental
// path under an already-verified cargo target parent.
func ValidateIncrementalUnderParent(parent, candidate string) error {
	parent = filepath.Clean(parent)
	candidate = filepath.Clean(candidate)
	rel, err := filepath.Rel(parent, candidate)
	if err != nil {
		return fmt.Errorf("not under parent: %w", err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return fmt.Errorf("incremental path escapes parent")
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 1 || len(parts) > 3 {
		return fmt.Errorf("incremental path depth %d not in 1..3", len(parts))
	}
	if parts[len(parts)-1] != cargoIncrementalName {
		return fmt.Errorf("leaf must be %q", cargoIncrementalName)
	}
	for _, p := range parts {
		if !IsSafePathSegment(p) {
			return fmt.Errorf("unsafe path segment %q", p)
		}
	}
	if err := ValidatePathNoSymlinkComponents(candidate); err != nil {
		return err
	}
	return nil
}

// CollapseContainment drops candidates that are descendants of another
// candidate path (antichain). First-seen (walk/config order) ancestor wins.
// Returns the filtered list and the number of dropped descendants.
func CollapseContainment(jobs []CandidateJob) (unique []CandidateJob, dropped int) {
	if len(jobs) == 0 {
		return nil, 0
	}
	// Normalize paths first.
	type keyed struct {
		job CandidateJob
		key string
	}
	items := make([]keyed, 0, len(jobs))
	for _, j := range jobs {
		k := candidatePathKey(j.Path)
		j.Path = k
		items = append(items, keyed{job: j, key: k})
	}
	// Prefer shorter (ancestor) paths when deciding conflicts: process shorter first.
	sort.SliceStable(items, func(i, j int) bool {
		if len(items[i].key) != len(items[j].key) {
			return len(items[i].key) < len(items[j].key)
		}
		return items[i].key < items[j].key
	})
	kept := make([]keyed, 0, len(items))
	for _, it := range items {
		skip := false
		for _, k := range kept {
			if isPathWithin(k.key, it.key) {
				skip = true
				break
			}
		}
		if skip {
			dropped++
			continue
		}
		// If a longer path was somehow kept before a shorter one, remove descendants
		// already kept (should not happen after length sort).
		kept = append(kept, it)
	}
	// Restore original discovery order among survivors.
	order := make(map[string]int, len(jobs))
	for i, j := range jobs {
		order[candidatePathKey(j.Path)] = i
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return order[kept[i].key] < order[kept[j].key]
	})
	unique = make([]CandidateJob, 0, len(kept))
	for _, k := range kept {
		unique = append(unique, k.job)
	}
	return unique, dropped
}

// isPathWithin reports whether child is the same as parent or a descendant.
func isPathWithin(parent, child string) bool {
	if parent == child {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(child, parent+sep)
}
