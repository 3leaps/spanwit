package bench

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteWitness detects files created or modified beneath a set of directories
// across a run, and separates the ones this application could be responsible
// for from the ones it could not.
//
// This tests the guarantee that is actually ours to make: the application
// creates no file, temp, cache, or database of its own. It is deliberately
// separate from whole-system write counters, which move for reasons no
// implementation controls — filesystem metadata writeback, another process, or
// the caller's own redirection of our output to a file on the volume being
// measured. Those counters are benchmark evidence; this is the promise.
//
// Attribution is the part that is easy to get wrong. Watching the whole user
// cache directory and reporting everything that changed would charge this tool
// with a browser's cache writes, on a machine where a browser happens to be
// running. That is a number labelled as something it does not measure — the
// same error the accounting rules elsewhere in this package exist to prevent —
// so changes are split: those under a path this application owns are
// attributable and fail the promise, and everything else is counted as ambient
// machine activity and reported as context.
type WriteWitness struct {
	roots    []string
	owned    []string
	streams  []fileIdentity
	baseline map[string]witnessEntry
}

// fileIdentity is a device and inode pair, used to recognize a file regardless
// of the path it is reached by.
type fileIdentity struct {
	dev uint64
	ino uint64
}

type witnessEntry struct {
	size    int64
	modTime int64
}

// NewWriteWitness snapshots the given directories, treating changes under any
// owned prefix as attributable to this application.
//
// Directories that do not exist are skipped, not treated as errors: a cache
// directory the tool has never created is exactly the state being defended.
func NewWriteWitness(roots, owned []string) (*WriteWitness, error) {
	w := &WriteWitness{roots: roots, owned: owned, baseline: map[string]witnessEntry{}}
	snapshot, err := w.snapshot()
	if err != nil {
		return nil, err
	}
	w.baseline = snapshot
	return w, nil
}

// IgnoreStreams excludes files the given handles are connected to, matched by
// device and inode rather than by path.
//
// An operator redirecting the harness's own output into the working directory
// — `harness sweep 2> sweep.log` — produces a file that grew during the run
// inside a witnessed root. The witness is right that something wrote there and
// wrong about what it means: the harness's own diagnostic stream is not the
// application writing uninvited. Identity matching handles this without a path
// filter, which would also silence a genuine write to the same name.
//
// Handles connected to a terminal or a pipe have no stable identity to record
// and are skipped.
func (w *WriteWitness) IgnoreStreams(files ...*os.File) {
	for _, f := range files {
		if f == nil {
			continue
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if id, ok := identityOf(info); ok {
			w.streams = append(w.streams, id)
		}
	}
}

// DefaultWitnessRoots returns the locations a filesystem tool would plausibly
// write to without being asked: the temporary directory, the user's config and
// cache directories, and the working directory.
func DefaultWitnessRoots() []string {
	var roots []string
	roots = append(roots, os.TempDir())
	if dir, err := os.UserConfigDir(); err == nil {
		roots = append(roots, dir)
	}
	if dir, err := os.UserCacheDir(); err == nil {
		roots = append(roots, dir)
	}
	if dir, err := os.Getwd(); err == nil {
		roots = append(roots, dir)
	}
	return roots
}

// OwnedPaths returns the path prefixes an application of the given name would
// write to if it wrote anything: its config and cache directories, its
// temporary files, and the working directory.
//
// The name is supplied by the caller rather than hardcoded here, so this stays
// correct if the binary is ever renamed or refitted.
//
// Prefixes match as strings, not as directory containment, so a name of
// "spanwit" covers both a "spanwit/" directory and "spanwit-1234" temp files.
func OwnedPaths(app string) []string {
	var owned []string
	if app == "" {
		return owned
	}
	owned = append(owned, filepath.Join(os.TempDir(), app))
	if dir, err := os.UserConfigDir(); err == nil {
		owned = append(owned, filepath.Join(dir, app))
	}
	if dir, err := os.UserCacheDir(); err == nil {
		owned = append(owned, filepath.Join(dir, app))
	}
	// A filesystem tool writing into the user's working directory
	// uninvited is precisely the failure this promise forbids, so the whole
	// directory is treated as ours for attribution purposes.
	if dir, err := os.Getwd(); err == nil {
		owned = append(owned, dir)
	}
	return owned
}

// Violation is a file that appeared or changed during a run.
type Violation struct {
	Path   string `json:"path"`
	Change string `json:"change"`
}

// WitnessReport separates changes this application could have caused from
// changes it could not.
type WitnessReport struct {
	// Attributable lists changes under a path this application owns. A
	// non-empty list falsifies the zero-write promise.
	Attributable []Violation `json:"attributable"`

	// Ambient counts changes elsewhere on the machine during the run: other
	// processes doing their own work. Recorded as context, never as a
	// finding, because nothing here can attribute them.
	Ambient int `json:"ambient"`

	// AmbientSample holds a few ambient paths, enough to recognize what was
	// running without listing a busy machine's entire cache.
	AmbientSample []string `json:"ambient_sample,omitempty"`
}

// Clean reports whether the application wrote nothing it should not have.
func (r WitnessReport) Clean() bool { return len(r.Attributable) == 0 }

// Summary states the result in one line, including the ambient context that
// makes a clean result meaningful rather than merely quiet.
func (r WitnessReport) Summary() string {
	if r.Clean() {
		return fmt.Sprintf("no attributable writes (%d ambient changes elsewhere on the machine)", r.Ambient)
	}
	return fmt.Sprintf("%d attributable write(s), %d ambient", len(r.Attributable), r.Ambient)
}

// ambientSampleSize bounds how many ambient paths are retained.
const ambientSampleSize = 5

// Check returns files created or modified since the snapshot.
//
// Paths under a directory the caller names in ignore are excluded, which is
// how a corpus being built or a results file being written is kept out of the
// finding. Every ignore is a deliberate, named exception rather than a broad
// filter.
func (w *WriteWitness) Check(ignore ...string) (WitnessReport, error) {
	current, err := w.snapshot()
	if err != nil {
		return WitnessReport{}, err
	}

	var report WitnessReport
	for path, now := range current {
		if underAny(path, ignore) {
			continue
		}
		before, existed := w.baseline[path]
		var change string
		switch {
		case !existed:
			change = "created"
		case before.size != now.size || before.modTime != now.modTime:
			change = "modified"
		default:
			continue
		}

		if w.isOwnStream(path) {
			continue
		}
		if !hasAnyPrefix(path, w.owned) {
			report.Ambient++
			if len(report.AmbientSample) < ambientSampleSize {
				report.AmbientSample = append(report.AmbientSample, path)
			}
			continue
		}
		report.Attributable = append(report.Attributable, Violation{Path: path, Change: change})
	}

	sort.Slice(report.Attributable, func(i, j int) bool {
		return report.Attributable[i].Path < report.Attributable[j].Path
	})
	sort.Strings(report.AmbientSample)
	return report, nil
}

// snapshot records file identities beneath the witness roots.
func (w *WriteWitness) snapshot() (map[string]witnessEntry, error) {
	out := map[string]witnessEntry{}
	for _, root := range w.roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // an unreadable subtree cannot be witnessed either way
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil //nolint:nilerr // vanished between listing and stat
			}
			out[p] = witnessEntry{size: info.Size(), modTime: info.ModTime().UnixNano()}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("bench: snapshot %s: %w", root, err)
		}
	}
	return out, nil
}

// isOwnStream reports whether path is a file one of the harness's own output
// streams is connected to.
func (w *WriteWitness) isOwnStream(path string) bool {
	if len(w.streams) == 0 {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	id, ok := identityOf(info)
	if !ok {
		return false
	}
	for _, s := range w.streams {
		if s == id {
			return true
		}
	}
	return false
}

// hasAnyPrefix reports whether path begins with any of the given string
// prefixes. Used for ownership attribution, where a prefix may name a file
// stem such as a temp-file prefix rather than a directory.
func hasAnyPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func underAny(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if prefix == "" {
			continue
		}
		rel, err := filepath.Rel(prefix, path)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
