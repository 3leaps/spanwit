package corpus

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// MountBoundary is a directory whose device identity differs from its parent's,
// which is what --one-filesystem must stop at.
type MountBoundary struct {
	// Path is the absolute path of the boundary directory.
	Path string

	// ParentDevice and Device are the differing device identifiers.
	ParentDevice uint64
	Device       uint64
}

// MountBoundariesUnder reports directories below root that sit on a different
// device from their parent, to a bounded depth.
//
// Creating a mount point requires privilege spanwit does not assume, so this
// finds real boundaries rather than fabricating them. When it returns none, a
// caller measuring device-boundary behavior has not exercised it, and should
// say so rather than treat an empty result as a pass. FixtureOmission renders
// that statement.
//
// Off unix, device identity is not observable and this always returns nil with
// ok=false.
func MountBoundariesUnder(root string, maxDepth int) (boundaries []MountBoundary, ok bool, err error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, false, fmt.Errorf("corpus: stat root: %w", err)
	}
	if _, _, identityOK := inodeIdentity(rootInfo); !identityOK {
		return nil, false, nil
	}

	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // unreadable subtrees are not boundary evidence
		}
		if !d.IsDir() {
			return nil
		}
		if depthBelow(root, p) > maxDepth {
			return fs.SkipDir
		}
		if p == root {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // vanished mid-walk; not boundary evidence
		}
		dev, _, devOK := inodeIdentity(info)
		if !devOK {
			return nil
		}
		parentDev, parentOK := deviceOf(filepath.Dir(p))
		if !parentOK || dev == parentDev {
			return nil
		}
		boundaries = append(boundaries, MountBoundary{
			Path:         p,
			ParentDevice: parentDev,
			Device:       dev,
		})
		// A boundary's own subtree is a different filesystem; nested
		// boundaries below it are not this root's concern.
		return fs.SkipDir
	})
	if walkErr != nil {
		return nil, true, fmt.Errorf("corpus: scan for mount boundaries: %w", walkErr)
	}

	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i].Path < boundaries[j].Path })
	return boundaries, true, nil
}

// FixtureOmission renders the omission a caller should record when it wanted
// to exercise device-boundary behavior and the environment supplied no
// boundary to exercise it against.
func FixtureOmission(observable bool, found int) (Omission, bool) {
	switch {
	case !observable:
		return Omission{
			Element: "mount-boundaries",
			Reason:  "device identity is not observable on this platform",
		}, true
	case found == 0:
		return Omission{
			Element: "mount-boundaries",
			Reason:  "no mount boundary exists below the measured root; creating one requires privilege",
		}, true
	default:
		return Omission{}, false
	}
}

// deviceOf returns the device identifier of a path.
func deviceOf(p string) (uint64, bool) {
	info, err := os.Lstat(p)
	if err != nil {
		return 0, false
	}
	dev, _, ok := inodeIdentity(info)
	return dev, ok
}

// depthBelow returns how many path elements p sits below root.
func depthBelow(root, p string) int {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." {
		return 0
	}
	depth := 1
	for _, r := range rel {
		if r == filepath.Separator {
			depth++
		}
	}
	return depth
}

// joinRoot joins a slash-separated relative path onto an absolute root.
func joinRoot(root, rel string) string {
	return filepath.Join(root, filepath.FromSlash(rel))
}

// IsolatingExclusions returns root-relative exclusion rules for every entry of
// parent except keep, so a walk rooted at parent reaches only the boundary.
//
// A real boundary's parent (for example /Volumes) holds unrelated siblings the
// caller does not own: a root-only Time Machine directory, other volumes, a
// network mount. Walking them makes a boundary fixture depend on whatever the
// host has mounted. The walker applies exclusions before any metadata read or
// open, so excluded siblings are never descended and never become gaps.
func IsolatingExclusions(parent, keep string) ([]string, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, fmt.Errorf("corpus: list boundary parent: %w", err)
	}
	keepName := filepath.Base(keep)
	if filepath.Dir(filepath.Clean(keep)) != filepath.Clean(parent) {
		return nil, fmt.Errorf("corpus: %s is not a direct child of %s", keep, parent)
	}
	var rules []string
	for _, e := range entries {
		if e.Name() == keepName {
			continue
		}
		rules = append(rules, "./"+e.Name())
	}
	return rules, nil
}
