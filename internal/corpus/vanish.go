package corpus

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Vanisher removes corpus objects while a traversal is in progress, fixturing
// the case where a file exists when a directory is listed and is gone by the
// time it is stated.
//
// This shape cannot be a static tree: the defect it catches is a race, and a
// walker that treats a mid-traversal ENOENT as a fatal error, or that silently
// drops the entry without recording a gap, only fails when the removal lands
// inside the window between listing and stat.
//
// A Vanisher is safe for concurrent use. Removals are recorded so a test can
// assert that a walker's reported total plus its vanished count reconciles
// against the manifest, rather than asserting an exact total that the race
// makes unstable.
type Vanisher struct {
	mu       sync.Mutex
	root     string
	pending  []string
	removed  []string
	failures []VanishFailure
}

// VanishFailure records a removal that did not happen, with its reason. A
// vanisher that silently failed to remove anything would let a test pass while
// exercising none of the race it exists to create.
type VanishFailure struct {
	RelPath string
	Reason  string
}

// NewVanisher returns a Vanisher that will remove the given manifest-relative
// paths, in the order supplied.
func NewVanisher(m *Manifest, relPaths []string) *Vanisher {
	pending := make([]string, len(relPaths))
	copy(pending, relPaths)
	return &Vanisher{root: m.Root, pending: pending}
}

// VanishableFiles returns manifest-relative paths of regular files that are
// safe to remove mid-traversal: readable, unlinked, and not sparse controls.
// Requires the manifest to have been built with Spec.RecordEntries.
func VanishableFiles(m *Manifest, limit int) []string {
	var out []string
	for _, e := range m.Entries {
		if len(out) >= limit {
			break
		}
		if e.Class != ClassFile || e.Unreadable || e.LinkGroup != "" {
			continue
		}
		out = append(out, e.RelPath)
	}
	return out
}

// VanishNext removes the next pending object and returns its relative path.
// It returns an empty path when nothing is pending.
//
// Calling this from inside a walk callback makes the race deterministic: the
// object is removed at a known point in the traversal rather than at a random
// moment, so a failure reproduces.
func (v *Vanisher) VanishNext() (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.pending) == 0 {
		return "", nil
	}
	rel := v.pending[0]
	v.pending = v.pending[1:]

	if err := os.Remove(joinRoot(v.root, rel)); err != nil {
		v.failures = append(v.failures, VanishFailure{RelPath: rel, Reason: err.Error()})
		return "", fmt.Errorf("corpus: vanish %s: %w", rel, err)
	}
	v.removed = append(v.removed, rel)
	return rel, nil
}

// Churn removes pending objects at the given interval until stop is closed or
// nothing is pending. Intended for benchmark runs, where the point is sustained
// mid-traversal mutation rather than a reproducible instant.
func (v *Vanisher) Churn(stop <-chan struct{}, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			rel, err := v.VanishNext()
			if err == nil && rel == "" {
				return // nothing left to remove
			}
		}
	}
}

// Removed returns the paths successfully removed so far.
func (v *Vanisher) Removed() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]string, len(v.removed))
	copy(out, v.removed)
	return out
}

// Failures returns removals that were attempted and did not succeed.
func (v *Vanisher) Failures() []VanishFailure {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]VanishFailure, len(v.failures))
	copy(out, v.failures)
	return out
}

// Count returns how many objects were successfully removed.
func (v *Vanisher) Count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.removed)
}
