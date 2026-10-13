package engine

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/3leaps/spanwit/internal/config"
)

// Sibling and content-marker evidence for reclaim signatures. Both follow the
// same discipline as ancestor and child evidence: configured names must be
// local relative paths, every inspected entry goes through isPlainEvidence
// (Lstat, no symlink in any component), and a symlink never contributes
// deletion authority.

const (
	// siblingScanLimit bounds the parent-directory listing for sibling
	// evidence, so a name collision in a large directory cannot become an
	// unbounded scan. Over the limit, sibling evidence fails closed.
	siblingScanLimit = 256
	// markerReadLimit caps the bytes read from a content-marker file.
	markerReadLimit = 4 << 10
)

// liveDBDir is the only directory shape that can vouch as a sibling: a
// numeric grype schema directory (db/<N>). It keeps two download residue
// directories from vouching for each other.
var liveDBDir = regexp.MustCompile(`^[0-9]+$`)

// siblingEvidence reports whether one sibling directory of path's parent
// holds every sibling-evidence file as a plain regular file. Each pattern is
// "<dir-glob>/<file>" relative to the parent; all patterns must share the same
// directory glob, and they must all be satisfied in the SAME sibling directory
// (for grype: db/<N>/import.json and db/<N>/vulnerability.db together). The
// sibling directory must have the live-db shape, must not match the
// signature's own candidate patterns, and is never path itself.
func siblingEvidence(root, path string, signature config.Signature) (string, bool) {
	parent := filepath.Dir(path)
	if !isWithinDir(root, parent) && parent != root {
		return "", false
	}
	dirGlob, files, ok := siblingSpec(signature.SiblingEvidence)
	if !ok {
		return "", false
	}
	entries, err := readDirLimited(parent, siblingScanLimit)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		name := e.Name()
		sib := filepath.Join(parent, name)
		if sib == path || !liveDBDir.MatchString(name) {
			continue
		}
		if m, err := filepath.Match(dirGlob, name); err != nil || !m {
			continue
		}
		if matchesAnyCandidatePattern(root, sib, signature) {
			continue
		}
		all := true
		for _, file := range files {
			if !isPlainEvidence(filepath.Join(sib, file), true) {
				all = false
				break
			}
		}
		if all {
			return sib, true
		}
	}
	return "", false
}

// siblingSpec splits sibling patterns into one shared directory glob and the
// files required in it. Mixed directory globs or unsafe names fail closed.
func siblingSpec(patterns []string) (string, []string, bool) {
	var dirGlob string
	var files []string
	for _, pattern := range patterns {
		if !isLocalRelativeName(pattern) {
			return "", nil, false
		}
		d, file, ok := strings.Cut(filepath.ToSlash(pattern), "/")
		if !ok || file == "" || strings.Contains(file, "/") || !isLocalRelativeName(file) {
			return "", nil, false
		}
		if dirGlob == "" {
			dirGlob = d
		} else if d != dirGlob {
			return "", nil, false
		}
		files = append(files, file)
	}
	return dirGlob, files, dirGlob != "" && len(files) > 0
}

// readDirLimited lists dir without following a symlinked dir, failing closed
// when it holds more than limit entries.
func readDirLimited(dir string, limit int) ([]os.DirEntry, error) {
	if !isPlainEvidence(dir, false) {
		return nil, os.ErrPermission
	}
	before, err := os.Lstat(dir)
	if err != nil || !before.IsDir() {
		return nil, os.ErrPermission
	}
	// Open without following a symlink, then confirm the opened directory is
	// the one inspected, as for the marker file.
	f, err := openNoFollow(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !after.IsDir() || !os.SameFile(before, after) {
		return nil, os.ErrPermission
	}
	entries, err := f.ReadDir(limit + 1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > limit {
		return nil, os.ErrInvalid
	}
	return entries, nil
}

func matchesAnyCandidatePattern(root, path string, signature config.Signature) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, p := range signature.CandidatePatterns {
		if matchesPattern(rel, p) {
			return true
		}
	}
	return false
}

// contentMarkerEvidence reports whether the marker file under path is a plain
// regular file whose first line equals the expected line exactly. It opens
// without following a symlink and confirms the opened file is the one that
// was inspected, then reads at most markerReadLimit bytes.
func contentMarkerEvidence(path string, marker *config.ContentMarker) (string, bool) {
	if marker == nil || !isLocalRelativeName(marker.Path) || marker.FirstLine == "" {
		return "", false
	}
	candidate := filepath.Join(path, filepath.FromSlash(marker.Path))
	if !isWithinDir(path, candidate) || !isPlainEvidence(candidate, true) {
		return "", false
	}
	before, err := os.Lstat(candidate)
	if err != nil || !before.Mode().IsRegular() {
		return "", false
	}
	f, err := openNoFollow(candidate)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return "", false
	}
	buf, err := io.ReadAll(io.LimitReader(f, markerReadLimit))
	if err != nil {
		return "", false
	}
	line, _, found := bytes.Cut(buf, []byte("\n"))
	if !found && len(buf) >= markerReadLimit {
		return "", false // no line end within the cap
	}
	line = bytes.TrimSuffix(line, []byte("\r"))
	if string(line) != marker.FirstLine {
		return "", false
	}
	return candidate, true
}

// ageFloorFor returns the longest min_age_floor among every catalog signature
// whose candidate patterns match rel, whatever the entry's provenance: a
// pattern-only target or exact-plan entry cannot escape a signature's floor.
// It returns "" when no matching signature declares a floor.
func ageFloorFor(rel string, signatures config.SignatureCatalog) string {
	best := ""
	var bestDur int64 = -1
	for _, ecosystems := range signatures {
		for _, sigs := range ecosystems {
			for _, sig := range sigs {
				if sig.MinAgeFloor == "" || !matchesAny(rel, sig.CandidatePatterns) {
					continue
				}
				d, err := config.ParseAge(sig.MinAgeFloor)
				if err != nil {
					continue
				}
				if int64(d) > bestDur {
					best, bestDur = sig.MinAgeFloor, int64(d)
				}
			}
		}
	}
	return best
}

// raiseMinAge returns the longer of the effective min age and the floor, so a
// floor is a minimum the configured window can raise but never lower.
func raiseMinAge(effMinAge, floor string) string {
	if floor == "" {
		return effMinAge
	}
	cur, err := config.ParseAge(displayFilterRaw(effMinAge))
	f, ferr := config.ParseAge(floor)
	if ferr != nil {
		return effMinAge
	}
	if err != nil || f > cur {
		return floor
	}
	return effMinAge
}

// floorMatchedPath reports whether rel matches any floored catalog signature.
func floorMatchedPath(rel string, signatures config.SignatureCatalog) bool {
	return ageFloorFor(rel, signatures) != ""
}

// guarded reports whether a signature declares evidence beyond a name match
// that a pattern-only target could otherwise bypass.
func guarded(sig config.Signature) bool {
	return len(sig.SiblingEvidence) > 0 || sig.ContentMarker != nil || sig.MinAgeFloor != ""
}

// guardedSignatureCovers reports whether rel is a path a guarded signature
// protects: either a candidate it covers, or a live evidence directory it
// depends on (a sibling directory of a candidate pattern, such as grype's
// live db/<N>). Pattern-only targets and exact entries on such a path are
// refused, so they can neither skip a signature's evidence and floor nor
// delete the live data its evidence points at.
func guardedSignatureCovers(rel string, signatures config.SignatureCatalog) bool {
	for _, ecosystems := range signatures {
		for _, sigs := range ecosystems {
			for _, sig := range sigs {
				if !guarded(sig) {
					continue
				}
				if matchesAny(rel, sig.CandidatePatterns) || isSiblingEvidenceDir(rel, sig) {
					return true
				}
			}
		}
	}
	return false
}

// isSiblingEvidenceDir reports whether rel is a live-db-shaped directory
// beside a signature's candidates: its parent is where the candidate pattern
// matches. For grype, rel ".../grype/db/6" sits where
// ".../grype/db/grype-db-download*" matches, so it is protected evidence.
func isSiblingEvidenceDir(rel string, sig config.Signature) bool {
	if len(sig.SiblingEvidence) == 0 {
		return false
	}
	base := filepath.Base(filepath.FromSlash(rel))
	if !liveDBDir.MatchString(base) {
		return false
	}
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
	for _, p := range sig.CandidatePatterns {
		dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(p)))
		if dir == "." {
			continue
		}
		if matchesPattern(parent, dir) {
			return true
		}
	}
	return false
}

const (
	// floorScanEntryLimit and floorScanDepth bound the activity read behind an
	// age floor, so a large or deep candidate cannot stall the plan.
	floorScanEntryLimit = 10000
	floorScanDepth      = 8
)

// newestActivity returns the newest modification time of dir and every entry
// beneath it, walked without following symlinks. complete is false when the
// walk hit an error, the depth bound or the entry limit: the newest time is
// then unknown, and an age floor must withhold rather than admit. A
// directory's own mtime does not move when a file inside it is written, so a
// floor over an in-flight download needs the descendants' times.
func newestActivity(dir string) (time.Time, bool) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return time.Time{}, false
	}
	newest := info.ModTime()
	complete := true
	entries := 0
	base := strings.Count(filepath.Clean(dir), string(filepath.Separator))
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			complete = false
			return filepath.SkipAll
		}
		if p == dir {
			return nil
		}
		entries++
		if entries > floorScanEntryLimit {
			complete = false
			return filepath.SkipAll
		}
		if d.IsDir() && strings.Count(p, string(filepath.Separator))-base >= floorScanDepth {
			complete = false
			return filepath.SkipAll
		}
		fi, err := d.Info()
		if err != nil {
			complete = false
			return filepath.SkipAll
		}
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		return nil
	})
	if walkErr != nil {
		complete = false
	}
	return newest, complete
}

// floorAgeOK reports whether a candidate guarded by an age floor is old
// enough to admit: its newest activity (directory or any descendant) must be
// at least the effective min age old, and the activity read must be complete.
func floorAgeOK(path, effMinAge string, now time.Time) bool {
	newest, complete := newestActivity(path)
	if !complete {
		return false
	}
	raw := displayFilterRaw(effMinAge)
	if raw == "" {
		return true
	}
	d, err := config.ParseAge(raw)
	if err != nil {
		return false
	}
	return now.Sub(newest) >= d
}

// tempRootAgeFloor is the age floor on every candidate under an admitted temp
// root, raised by configured min_age and never lowered. Cargo and pattern
// targets carry no signature floor, so without this an in-flight build in a
// temp checkout would be deletable.
const tempRootAgeFloor = "1h"

// groupOrOtherWritable reports whether a directory entry is writable by group
// or other. Under a temp root such a directory is not descended: another user
// could have planted its contents.
func groupOrOtherWritable(d fs.DirEntry) bool {
	info, err := d.Info()
	if err != nil {
		return true
	}
	return info.Mode().Perm()&0o022 != 0
}

// writableComponentBetween reports whether any directory from root down to
// and including path is group- or other-writable (or a symlink). The temp
// walk skips such a directory before matching, so revalidation refuses it the
// same way, including when the candidate itself was made writable later.
func writableComponentBetween(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return true
	}
	cur := root
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			return true
		}
	}
	return false
}
