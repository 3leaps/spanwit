package inventory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// TraversalOptions is an opt-in, summary-only observation policy. It does not
// expose the stalled-open primitive or grant deletion authority.
type TraversalOptions struct {
	// MaxDepth bounds directory descent: root=0, immediate child=1. Files in
	// directories at the bound are measured; deeper subtrees emit depth gaps.
	// -1 is unlimited. Zero intentionally means root files only.
	MaxDepth int
	// DiscoveredRoots keeps automatically selected roots subject to remote
	// exclusion. Their admission failures are visible gaps, not batch failures.
	DiscoveredRoots bool
	// OnDirectory observes child directories before remote skipping/descent,
	// within MaxDepth. Return true to prune discovery at that directory (e.g.
	// measure a selected candidate in a subsequent run). Calls are serialized.
	// The root itself is not offered. No filesystem mutation is permitted.
	OnDirectory func(DirectoryVisit) bool
}

// TraversalScope declares the effective bounded discovery policy in the header.
type TraversalScope struct {
	MaxDepth         int  `json:"max_depth"`
	DiscoveredRoots  bool `json:"discovered_roots"`
	DirectoryPruning bool `json:"directory_pruning"`
}

// DirectoryVisit identifies a discovered directory, not an admitted prune path.
type DirectoryVisit struct {
	RootID       string
	RelativePath string
	Path         string
	Depth        int
}

// admitDiscoveredRoots retains stable identities even for unstatable roots.
// Lexical sorting makes ancestor-first overlap rejection deterministic. Exact
// duplicates share one identity; nested roots get an explicit affecting gap.
// Automatic roots never resolve a final symlink into permission to traverse it.
func admitDiscoveredRoots(requested []string) ([]Root, map[string]Gap, error) {
	paths := make([]string, 0, len(requested))
	seen := make(map[string]bool)
	for _, raw := range requested {
		if raw == "" || strings.IndexByte(raw, 0) >= 0 || !filepath.IsAbs(raw) {
			return nil, nil, fmt.Errorf("discovered root must be an absolute filesystem path")
		}
		p := filepath.Clean(raw)
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	roots := make([]Root, 0, len(paths))
	gaps := make(map[string]Gap)
	for _, p := range paths {
		r := Root{ID: "root-" + strconv.Itoa(len(roots)+1), Path: p}
		info, err := os.Lstat(p)
		if err != nil {
			gaps[r.ID] = pathGap(r, p, err)
		} else if !info.IsDir() {
			gaps[r.ID] = Gap{RootID: r.ID, Kind: "root-not-directory", AffectsCompleteness: true}
		} else {
			r.DeviceID = metadataOf(info).deviceID
			for _, prior := range roots {
				if _, failed := gaps[prior.ID]; !failed && pathContains(prior.Path, p) {
					gaps[r.ID] = Gap{RootID: r.ID, Kind: "overlapping-root", AffectsCompleteness: true,
						Detail: "discovered root overlaps an earlier root; not measured twice"}
					break
				}
			}
		}
		roots = append(roots, r)
	}
	return roots, gaps, nil
}

func (s *runState) observeDirectoryRead(root Root) {
	if s.opts.Traversal == nil {
		return
	}
	s.mu.Lock()
	*s.rootState[root.ID].Observed = true
	s.mu.Unlock()
}

func (s *runState) discoverDirectory(root Root, path string, depth int) bool {
	if s.opts.Traversal == nil || s.opts.Traversal.OnDirectory == nil {
		return false
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	return s.opts.Traversal.OnDirectory(DirectoryVisit{
		RootID: root.ID, RelativePath: relative(root, path), Path: path, Depth: depth,
	})
}
