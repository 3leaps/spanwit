package engine

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
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
// regular file whose leading lines equal the marker's expected lines exactly
// (each compared after stripping only "\n" or "\r\n"). It opens without
// following a symlink and confirms the opened file is the one that was
// inspected, then reads at most markerReadLimit bytes. Every expected line but
// the last must end with a newline inside the cap; the last may end at EOF.
func contentMarkerEvidence(path string, marker *config.ContentMarker) (string, bool) {
	if marker == nil || !isLocalRelativeName(marker.Path) {
		return "", false
	}
	want := marker.ExpectedLines()
	if len(want) == 0 {
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
	rest := buf
	for i, expected := range want {
		line, tail, found := bytes.Cut(rest, []byte("\n"))
		if !found {
			// The last expected line may end at EOF, but never at the cap
			// (the line could continue past what was read).
			if i != len(want)-1 || len(buf) >= markerReadLimit {
				return "", false
			}
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		if string(line) != expected {
			return "", false
		}
		rest = tail
	}
	return candidate, true
}

// requiredChildFilesEvidence reports whether every required child is a plain
// regular file under path with no symlink component.
func requiredChildFilesEvidence(path string, children []string) ([]string, bool) {
	var found []string
	for _, child := range children {
		if !isLocalRelativeName(child) {
			return nil, false
		}
		candidate := filepath.Join(path, filepath.FromSlash(child))
		if !isWithinDir(path, candidate) || !isPlainEvidence(candidate, true) {
			return nil, false
		}
		found = append(found, "file:"+child)
	}
	return found, true
}

// markerDiscovered reports whether a signature is discovered by its content
// marker alone (no candidate patterns). Such a signature matches only where
// its full evidence holds; it never covers a path by name, so it neither
// guards pattern-only targets nor contributes a path-matched age floor.
func markerDiscovered(sig config.Signature) bool {
	return len(sig.CandidatePatterns) == 0
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

// descendantsMeetMinAge applies any effective min_age (configured, signature
// or temp) to the newest activity the full sizing walk observed beneath a
// candidate. A directory's own mtime does not move when a file inside it is
// written, so the pre-sizing gate on the candidate's mtime alone admits an
// active build. With no min_age it admits. Otherwise the newest observed
// mtime must be at least min_age old, and activity that may be understated
// (depth bound, symlink, walk error) withholds. A regular-file candidate's
// walk visits only itself, so its own mtime is its activity.
func descendantsMeetMinAge(effMinAge string, sized SizeResult, now time.Time) bool {
	raw := displayFilterRaw(effMinAge)
	if raw == "" {
		return true
	}
	d, err := config.ParseAge(raw)
	if err != nil {
		return false
	}
	if sized.ActivityIncomplete || sized.Incomplete {
		return false
	}
	if sized.NewestMtime.IsZero() {
		// Nothing beneath the candidate: the pre-sizing gate on its own
		// mtime already decided.
		return true
	}
	return now.Sub(sized.NewestMtime) >= d
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

// signatureDisplayPattern is the pattern a candidate reports for its
// signature: the joined candidate patterns, or "marker:<path>" for a
// signature discovered by content marker alone.
func signatureDisplayPattern(sig config.Signature) string {
	if markerDiscovered(sig) && sig.ContentMarker != nil {
		return "marker:" + sig.ContentMarker.Path
	}
	return strings.Join(sig.CandidatePatterns, ",")
}

// excludedLiveCache reports whether path is one of the signature's excluded
// live caches, or must be treated as one. Exclusion locations are resolved
// with os.Stat (following a symlinked cache location to its target) and
// compared to the candidate with os.SameFile, so a case, firmlink or symlink
// spelling of the same directory is still excluded. It fails closed (true)
// when the candidate's identity or a required home directory is unknown.
func excludedLiveCache(path string, sig config.Signature) bool {
	if len(sig.ExcludedHomePaths) == 0 && !sig.ExcludeLiveGoCache {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil {
		return true
	}
	same := func(other string) bool {
		if other == "" || !filepath.IsAbs(other) {
			return false
		}
		// #nosec G703 -- a cleaned absolute location (real home join, user
		// cache dir, or the go command's GOCACHE setting), only stat'ed to
		// compare identity; it can only add an exclusion, never widen a
		// match, and is never opened or mutated.
		o, err := os.Stat(filepath.Clean(other))
		return err == nil && os.SameFile(info, o)
	}
	if len(sig.ExcludedHomePaths) > 0 {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return true
		}
		for _, rel := range sig.ExcludedHomePaths {
			if isLocalRelativeName(rel) && same(filepath.Join(home, filepath.FromSlash(rel))) {
				return true
			}
		}
	}
	if sig.ExcludeLiveGoCache {
		for _, loc := range liveGoCacheLocations() {
			if same(loc) {
				return true
			}
		}
	}
	return false
}

// liveGoCacheLocations returns every place the go command may keep its build
// cache for this user: $GOCACHE, GOCACHE written by `go env -w` to the go env
// file, and the default under os.UserCacheDir (which honours XDG_CACHE_HOME).
func liveGoCacheLocations() []string {
	var out []string
	if v := os.Getenv("GOCACHE"); v != "" {
		out = append(out, v)
	}
	if v := goEnvFileValue("GOCACHE"); v != "" {
		out = append(out, v)
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		out = append(out, filepath.Join(dir, "go-build"))
	}
	return out
}

// goEnvFileValue reads key from the go env file ($GOENV, or go/env under
// os.UserConfigDir), the file `go env -w` writes. It reads at most
// markerReadLimit bytes and returns "" when the file is absent or GOENV=off.
func goEnvFileValue(key string) string {
	file := os.Getenv("GOENV")
	if file == "off" {
		return ""
	}
	if file == "" {
		dir, err := os.UserConfigDir()
		if err != nil || dir == "" {
			return ""
		}
		file = filepath.Join(dir, "go", "env")
	}
	if !filepath.IsAbs(file) {
		return ""
	}
	file = filepath.Clean(file)
	// #nosec G703 -- the go command's own env file, metadata only here.
	before, err := os.Lstat(file)
	if err != nil || !before.Mode().IsRegular() {
		return "" // a FIFO, device or link would block or mislead
	}
	f, err := openNoFollowNonBlock(file)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return ""
	}
	buf, err := io.ReadAll(io.LimitReader(f, markerReadLimit))
	if err != nil {
		return ""
	}
	val := ""
	for _, line := range strings.Split(string(buf), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(k) == key {
			val = strings.TrimSpace(v) // last assignment wins, as in go env
		}
	}
	return val
}

// childLayoutEvidence enforces ForbiddenChildPaths (no project or VCS marker
// directly under the candidate) and AllowedChildNames (every direct entry
// matches the closed layout). The directory listing is bounded; a larger
// listing refuses the match.
func childLayoutEvidence(path string, sig config.Signature) bool {
	for _, name := range sig.ForbiddenChildPaths {
		if !isLocalRelativeName(name) {
			return false
		}
		if _, err := os.Lstat(filepath.Join(path, filepath.FromSlash(name))); err == nil || !os.IsNotExist(err) {
			return false
		}
	}
	if len(sig.KnownChildFiles) > 0 && !openLayoutEvidence(path, sig) {
		return false
	}
	if !deepCheckEvidence(path, sig) {
		return false
	}
	if len(sig.AllowedChildNames) == 0 {
		return true
	}
	d, err := os.Open(path) // #nosec G304 -- a walked candidate directory, listed only.
	if err != nil {
		return false
	}
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(layoutListLimit + 1)
	if (err != nil && err != io.EOF) || len(names) > layoutListLimit {
		return false
	}
	for _, n := range names {
		ok := false
		for _, pat := range sig.AllowedChildNames {
			if m, _ := pathpkg.Match(pat, n); m {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// layoutListLimit bounds the direct-child listing of a closed-layout cache
// (Go's has 256 shards plus a few files).
const layoutListLimit = 1024

// isHomeOrAncestor reports whether path is the real home directory or one of
// its ancestors, by identity: home and each of its ancestors are os.Stat'ed
// and compared to the candidate with os.SameFile, so another spelling of a
// home ancestor (/private/var vs /var, /System/Volumes/Data/Users) is still
// caught. An unknown, relative or unstattable home counts as true (fail
// closed). A marker-only signature never reaches that high: a cache seeded
// into a home or a parent of it is a misconfiguration, not reclaimable output.
func isHomeOrAncestor(path string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !filepath.IsAbs(home) {
		return true
	}
	cand, err := os.Stat(path)
	if err != nil {
		return true
	}
	for p := filepath.Clean(home); ; {
		info, err := os.Stat(p)
		if err != nil {
			return true
		}
		if os.SameFile(info, cand) {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// isPlainDir reports whether path is a directory (Lstat, never a symlink).
func isPlainDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// markerFloorMinAge raises the effective min_age by a marker-only signature's
// own floor. Such a signature matches no path pattern, so ageFloorFor never
// sees it. The raised min_age is enforced against the newest activity of the
// full sizing walk (descendantsMeetMinAge), not the entry-capped floor scan,
// so a large cache is age-gated rather than withheld for an incomplete read.
func markerFloorMinAge(effMinAge string, sig config.Signature) string {
	if !markerDiscovered(sig) || sig.MinAgeFloor == "" {
		return effMinAge
	}
	return raiseMinAge(effMinAge, sig.MinAgeFloor)
}

// UnderSharedTemp reports whether path is a shared temp directory or lies
// beneath one (see underSharedTemp).
func UnderSharedTemp(path string) bool { return underSharedTemp(path) }

// stickyAncestorCheck (the world-writable ancestor rule) is on outside
// tests that place fixtures under /tmp.
var stickyAncestorCheck = true

// underSharedTemp reports whether path is, or lies beneath, a shared temp
// directory. It compares identities, not spellings, so case variants and
// firmlink spellings (/System/Volumes/Data/private/tmp) of a shared root are
// caught. Path and every ancestor are os.Stat'ed (following links, since
// /tmp is itself a link): one that is the same file as a known shared temp
// root, or that is world-writable (sticky or not: the mode that lets other
// users plant or rename entries, which also covers /dev/shm and bind
// mounts), makes the path shared. Any stat error counts as shared (fail closed).
func underSharedTemp(path string) bool {
	var roots []os.FileInfo
	for _, s := range sharedTempPaths {
		if info, err := os.Stat(s); err == nil {
			roots = append(roots, info)
		}
	}
	p := filepath.Clean(path)
	if !filepath.IsAbs(p) {
		return true
	}
	for {
		info, err := os.Stat(p)
		if err != nil {
			return true
		}
		for _, r := range roots {
			if os.SameFile(info, r) {
				return true
			}
		}
		if stickyAncestorCheck && info.IsDir() && info.Mode().Perm()&0o002 != 0 {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// readDirBounded lists dir, failing (ok false) on any error other than
// io.EOF or when it holds more than layoutListLimit entries.
func readDirBounded(dir string) ([]os.DirEntry, bool) {
	d, err := os.Open(dir) // #nosec G304 -- a walked candidate directory, listed only.
	if err != nil {
		return nil, false
	}
	entries, err := d.ReadDir(layoutListLimit + 1)
	_ = d.Close()
	if (err != nil && err != io.EOF) || len(entries) > layoutListLimit {
		return nil, false
	}
	return entries, true
}

// forbiddenInside reports whether any ForbiddenChildPaths name exists
// directly inside dir ("src" is skipped below the candidate's top: rustdoc
// writes doc/src). Any Lstat error other than not-exist counts as present.
func forbiddenInside(dir string, sig config.Signature) bool {
	for _, name := range sig.ForbiddenChildPaths {
		if name == "src" {
			continue
		}
		if !isLocalRelativeName(name) {
			return true
		}
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(name))); err == nil || !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

// openLayoutEvidence refuses a match whose direct entries are not the
// tool's: a non-directory must be a regular file with an exact known name
// (no patterns), and no directory may hold a project or VCS marker directly.
// Whether every directory is recognisable is decided separately
// (layoutRecognised), which withholds rather than refuses.
func openLayoutEvidence(path string, sig config.Signature) bool {
	entries, ok := readDirBounded(path)
	if !ok {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			if e.Type().IsRegular() && containsString(sig.KnownChildFiles, e.Name()) {
				continue
			}
			return false
		}
		if forbiddenInside(filepath.Join(path, e.Name()), sig) {
			return false
		}
	}
	return true
}

// deepCheckEvidence refuses a match when a DeepCheckChildDirs directory has a
// child directory holding a project or VCS marker (Go's fuzz/<pkg>/...).
func deepCheckEvidence(path string, sig config.Signature) bool {
	for _, name := range sig.DeepCheckChildDirs {
		dir := filepath.Join(path, name)
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.IsDir() {
			return false
		}
		if forbiddenInside(dir, sig) {
			return false
		}
		entries, ok := readDirBounded(dir)
		if !ok {
			return false
		}
		for _, e := range entries {
			if e.IsDir() && e.Type()&os.ModeSymlink == 0 && forbiddenInside(filepath.Join(dir, e.Name()), sig) {
				return false
			}
		}
	}
	return true
}

// layoutRecognised reports whether every direct directory of a matched
// candidate is one the tool creates: a KnownChildDirs name, or a directory
// holding a ProfileDirMarkers entry directly or in one of its own child
// directories (a target triple holds profiles). An unrecognised directory
// (user data in a reused target dir) withholds the match with reason layout.
// Signatures without KnownChildDirs are always recognised.
func layoutRecognised(path string, sig config.Signature) bool {
	if len(sig.KnownChildDirs) == 0 && len(sig.ProfileDirMarkers) == 0 {
		return true
	}
	entries, ok := readDirBounded(path)
	if !ok {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue // files were decided by openLayoutEvidence
		}
		sub := filepath.Join(path, e.Name())
		if containsString(sig.ProfileRequiredDirs, e.Name()) {
			if !hasPlainDirChild(sub, sig.ProfileDirMarkers) {
				return false
			}
			continue
		}
		if matchesAnyName(e.Name(), sig.KnownChildDirs) {
			continue
		}
		if !profileShaped(sub, sig) {
			return false
		}
	}
	return true
}

// profileShaped reports whether dir is a build profile (it holds a profile
// marker directly) or a target-triple directory (one of its child
// directories holds the marker and no project or VCS marker).
func profileShaped(dir string, sig config.Signature) bool {
	if hasPlainDirChild(dir, sig.ProfileDirMarkers) {
		return true
	}
	entries, ok := readDirBounded(dir)
	if !ok {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		child := filepath.Join(dir, e.Name())
		if hasPlainDirChild(child, sig.ProfileDirMarkers) && !forbiddenInside(child, sig) {
			return true
		}
	}
	return false
}

func hasPlainDirChild(dir string, names []string) bool {
	for _, n := range names {
		if isLocalRelativeName(n) && isPlainDir(filepath.Join(dir, n)) {
			return true
		}
	}
	return false
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func matchesAnyName(name string, patterns []string) bool {
	for _, pat := range patterns {
		if m, _ := pathpkg.Match(pat, name); m {
			return true
		}
	}
	return false
}
