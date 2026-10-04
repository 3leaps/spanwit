package space

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Temp-plane coverage names the OS temp directories a pressured `space` run
// did not walk, with a bounded size, so crisis triage does not stop at the
// analysis root. It is observation only: a plane listed here carries no prune
// authority, and the follow-up is a root-level `space` command the operator
// runs explicitly.
const (
	TempPlaneKindShared = "shared-temp"
	TempPlaneKindUser   = "user-temp"

	TempPlaneSizeMeasured    = "measured"
	TempPlaneSizePartial     = "partial"
	TempPlaneSizeUnavailable = "unavailable"

	// tempPlaneProbeDepth and tempPlaneProbeDeadline bound the size probe so a
	// large or stalled temp tree can never hold up crisis triage.
	tempPlaneProbeDepth    = 4
	tempPlaneProbeDeadline = 2 * time.Second
)

// TempPlaneCoverage is emitted only under warn/critical primary pressure.
type TempPlaneCoverage struct {
	// Trigger is the primary pressure level that caused the probe.
	Trigger string      `json:"trigger"`
	Planes  []TempPlane `json:"planes"`
}

// TempPlane discloses one canonical temp root and a bounded size. It never
// names children: no entries, session ids, or snapshot names.
type TempPlane struct {
	Root string `json:"root"`
	Kind string `json:"kind"`
	// SizeStatus is measured (exact apparent bytes), partial (SizeBytes is a
	// lower bound), or unavailable (no bytes reported).
	SizeStatus string `json:"size_status"`
	SizeBytes  *int64 `json:"size_bytes,omitempty"`
	SizeBasis  string `json:"size_basis"`
	FollowUp   string `json:"follow_up"`
}

type tempPlaneCandidate struct {
	Path string
	Kind string
}

// dirIdentity is a directory's (device, inode). Same device alone is never
// identity: APFS puts nearly everything on one device.
type dirIdentity struct {
	dev uint64
	ino uint64
}

// defaultTempPlaneCandidates returns the shared temp root and the per-user
// temp directory. No external command is run to find them.
func defaultTempPlaneCandidates() []tempPlaneCandidate {
	switch runtime.GOOS {
	case "darwin", "linux":
	default:
		return nil
	}
	out := []tempPlaneCandidate{{Path: "/tmp", Kind: TempPlaneKindShared}}
	if tmp := os.TempDir(); tmp != "" {
		out = append(out, tempPlaneCandidate{Path: tmp, Kind: TempPlaneKindUser})
	}
	return out
}

// canonicalTempPath cleans a path and lexically rewrites the trusted macOS
// volume aliases (/tmp, /var → /private/...). It never resolves arbitrary
// symlinks, so an operator path cannot redirect it to another tree.
func canonicalTempPath(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS != "darwin" {
		return p
	}
	for _, alias := range []string{"/tmp", "/var"} {
		if p == alias || strings.HasPrefix(p, alias+"/") {
			return "/private" + p
		}
	}
	return p
}

// pathWithin reports whether child equals parent or sits beneath it.
func pathWithin(child, parent string) bool {
	if child == parent {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// collectTempPlaneCoverage admits candidate planes, dedups them by directory
// identity, drops any plane that overlaps the analysis root, and probes the
// rest concurrently under a hard deadline.
func collectTempPlaneCoverage(ctx context.Context, analysisRoot, trigger string,
	candidates []tempPlaneCandidate, deadline time.Duration) *TempPlaneCoverage {
	if len(candidates) == 0 {
		return nil
	}
	root := canonicalTempPath(analysisRoot)
	rootID, rootIDOK := statDirIdentity(analysisRoot)

	type admitted struct {
		cand tempPlaneCandidate
		path string
		id   dirIdentity
	}
	var planes []admitted
	seen := map[dirIdentity]bool{}
	for _, c := range candidates {
		path := canonicalTempPath(c.Path)
		id, ok := admitTempPlane(path, c.Kind)
		if !ok || seen[id] {
			continue
		}
		if (rootIDOK && id == rootID) || pathWithin(path, root) || pathWithin(root, path) {
			continue
		}
		seen[id] = true
		planes = append(planes, admitted{cand: c, path: path, id: id})
	}
	if len(planes) == 0 {
		return nil
	}

	out := &TempPlaneCoverage{Trigger: trigger, Planes: make([]TempPlane, len(planes))}
	var wg sync.WaitGroup
	for i, p := range planes {
		wg.Add(1)
		go func(i int, p admitted) {
			defer wg.Done()
			status, bytes := probeTempPlane(ctx, p.path, p.id.dev, tempPlaneProbeDepth, deadline)
			plane := TempPlane{
				Root:       p.path,
				Kind:       p.cand.Kind,
				SizeStatus: status,
				SizeBasis:  "apparent",
				FollowUp:   "spanwit space " + ShellQuote(p.path),
			}
			if status != TempPlaneSizeUnavailable {
				b := bytes
				plane.SizeBytes = &b
			}
			out.Planes[i] = plane
		}(i, p)
	}
	wg.Wait()
	return out
}

// probeTempPlane sums apparent bytes of regular files using directory listing
// and lstat only: it never opens a regular file, never follows a symlink, and
// never crosses onto another device. The walk runs detached and publishes a
// running total; at the deadline the caller takes the snapshot and returns
// without waiting, so a syscall blocked in the kernel cannot hang triage.
func probeTempPlane(ctx context.Context, root string, rootDev uint64, maxDepth int,
	deadline time.Duration) (string, int64) {
	walkCtx, cancel := context.WithCancel(ctx)

	var total atomic.Int64
	var visited atomic.Int64
	var incomplete, rootUnreadable atomic.Bool
	done := make(chan struct{})

	go func() {
		defer close(done)
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if walkCtx.Err() != nil {
				return filepath.SkipAll
			}
			if err != nil {
				incomplete.Store(true)
				if p == root {
					rootUnreadable.Store(true)
					return filepath.SkipAll
				}
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if p == root {
				return nil
			}
			visited.Add(1)
			if d.IsDir() {
				rel, relErr := filepath.Rel(root, p)
				if relErr != nil || strings.Count(filepath.ToSlash(rel), "/") >= maxDepth {
					incomplete.Store(true)
					return filepath.SkipDir
				}
				info, infoErr := d.Info()
				if infoErr != nil {
					incomplete.Store(true)
					return filepath.SkipDir
				}
				if dev, ok := deviceOf(info); !ok || dev != rootDev {
					incomplete.Store(true)
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				incomplete.Store(true)
				return nil
			}
			total.Add(info.Size())
			return nil
		})
	}()

	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case <-done:
		cancel()
	case <-timer.C:
		cancel()
		incomplete.Store(true)
	case <-ctx.Done():
		cancel()
		incomplete.Store(true)
	}

	if rootUnreadable.Load() || (incomplete.Load() && visited.Load() == 0) {
		return TempPlaneSizeUnavailable, 0
	}
	if incomplete.Load() {
		return TempPlaneSizePartial, total.Load()
	}
	return TempPlaneSizeMeasured, total.Load()
}
