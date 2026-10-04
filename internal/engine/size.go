package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SizeResult is a directory size measurement that may be incomplete when a
// depth bound is applied. Activity fields capture newest observed file mtime
// for advisory idle ranking (never a deletion trigger).
type SizeResult struct {
	Bytes      int64
	Incomplete bool // true if maxDepth stopped descent before the full tree
	// NewestMtime is the newest file mtime observed under path (zero if none).
	// Symlink directories are not followed; encountering one marks activity incomplete.
	NewestMtime time.Time
	// ActivityIncomplete is true when activity may be understated (depth bound,
	// symlink stop, or walk errors that skip subtrees).
	ActivityIncomplete bool
}

// DirSize returns the total size of all files under path (unbounded depth).
func DirSize(path string) (int64, error) {
	r, err := DirSizeContext(context.Background(), path, -1)
	return r.Bytes, err
}

// DirSizeContext returns the size of files under path, honoring ctx cancellation
// and an optional maxDepth relative to path.
//
// maxDepth < 0 means unlimited depth (scan/prune reclaim accuracy). Unexpected
// filesystem errors (permissions, vanished entries mid-walk) fail closed: the
// returned error is non-nil so callers omit the candidate rather than admitting
// an understated executable total.
//
// maxDepth >= 0 is a triage bound (space). Descent stops at the bound with
// Incomplete=true (lower-bound sizes). Walk errors on bounded walks are
// skipped with Incomplete/ActivityIncomplete rather than hard-failing the
// whole measurement — space is diagnostic, not an execute plan.
//
// Activity: newest file mtime is tracked with Lstat semantics (DirEntry.Info).
// Symlink children are not followed; they stop descent and mark activity incomplete.
func DirSizeContext(ctx context.Context, path string, maxDepth int) (SizeResult, error) {
	var result SizeResult
	fullDepth := maxDepth < 0
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			result.Incomplete = true
			result.ActivityIncomplete = true
			if fullDepth {
				// Executable full-depth plans must not understate reclaim totals.
				return err
			}
			// Bounded triage: skip the unreadable subtree and continue.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Symlinks are not followed (WalkDir policy). Mark activity incomplete
		// because content behind a symlink is unobserved.
		if d.Type()&os.ModeSymlink != 0 {
			result.ActivityIncomplete = true
			// Still record the link's own metadata size/mtime when it is a file-like entry.
		}
		if p != path {
			rel, relErr := filepath.Rel(path, p)
			if relErr != nil {
				result.ActivityIncomplete = true
				if fullDepth {
					return relErr
				}
				return filepath.SkipDir
			}
			depth := sizeWalkDepth(rel)
			if maxDepth >= 0 {
				if d.IsDir() && depth >= maxDepth {
					// Bound stops further descent; measurement is incomplete
					// unless this directory is empty of all deeper content
					// (we cannot know without looking — mark incomplete).
					result.Incomplete = true
					result.ActivityIncomplete = true
					return filepath.SkipDir
				}
				if !d.IsDir() && depth > maxDepth {
					return nil
				}
			}
		}
		if !d.IsDir() {
			info, infoErr := d.Info()
			if infoErr != nil {
				result.ActivityIncomplete = true
				if fullDepth {
					return infoErr
				}
				return nil
			}
			result.Bytes += info.Size()
			mt := info.ModTime()
			if result.NewestMtime.IsZero() || mt.After(result.NewestMtime) {
				result.NewestMtime = mt
			}
		}
		return nil
	})
	return result, err
}

// sizeWalkDepth counts path separators under the walk root (immediate child = 0).
func sizeWalkDepth(rel string) int {
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, "/")
}

// ParseSize parses human size strings like 100M, 1G into bytes.
func ParseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	m := int64(1)
	switch {
	case strings.HasSuffix(s, "B"):
		m, s = 1, strings.TrimSuffix(s, "B")
	case strings.HasSuffix(s, "K"):
		m, s = 1024, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		m, s = 1024*1024, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		m, s = 1024*1024*1024, strings.TrimSuffix(s, "G")
	}
	var v int64
	_, err := fmt.Sscanf(s, "%d", &v)
	return v * m, err
}

// HumanSize formats a byte count for display.
func HumanSize(b int64) string {
	const u = 1024
	if b < u {
		return fmt.Sprintf("%dB", b)
	}
	div, e := int64(u), 0
	for n := b / u; n >= u; n /= u {
		div *= u
		e++
	}
	return fmt.Sprintf("%.1f%c", float64(b)/float64(div), "KMGTPE"[e])
}

// CleanConfiguredPath expands ~ and returns an absolute path.
func CleanConfiguredPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("configured path is required")
	}
	if path == "~" {
		homeDir, _ := os.UserHomeDir()
		path = homeDir
	} else if strings.HasPrefix(path, "~/") {
		homeDir, _ := os.UserHomeDir()
		path = filepath.Join(homeDir, path[2:])
	}
	return filepath.Abs(path)
}
