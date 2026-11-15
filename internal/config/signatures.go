package config

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func BuiltInSignatures() SignatureCatalog {
	return SignatureCatalog{
		"development": {
			"rust": {
				"cargo-target": {
					Description:           "Rust Cargo build output directory",
					CandidatePatterns:     []string{"**/target"},
					RequiredAncestorFiles: []string{"Cargo.toml"},
					AnyChildPaths:         []string{"debug", "release", ".rustc_info.json"},
					Confidence:            "high",
					SafeToPrune:           true,
				},
				// cargo-target-relocated is a Cargo target directory at any name
				// and place (CARGO_TARGET_DIR): no basename pattern and no
				// Cargo.toml ancestor. Cargo's own CACHEDIR.TAG (the generic
				// signature line plus cargo's comment line) and a debug/ or
				// release/ child are the evidence.
				"cargo-target-relocated": {
					Description: "Rust Cargo build output at a relocated path (CARGO_TARGET_DIR), recognised by its CACHEDIR.TAG",
					ContentMarker: &ContentMarker{
						Path:      "CACHEDIR.TAG",
						FirstLine: CacheDirTagSignatureLine,
						Lines:     []string{CacheDirTagSignatureLine, CargoCacheDirTagCommentLine},
					},
					AnyChildPaths:       []string{"debug", "release"},
					AnyChildDirsOnly:    true,
					ForbiddenChildPaths: projectRootMarkers(),
					KnownChildFiles:     []string{"CACHEDIR.TAG", ".rustc_info.json", ".cargo-lock", ".future-incompat-report.json", ".package-cache"},
					KnownChildDirs:      []string{"debug", "release", "doc", "package", "tmp", "build", "nextest", "criterion", "rust-analyzer", "flycheck[0-9]*", "llvm-cov-target"},
					ProfileDirMarkers:   []string{".fingerprint"},
					ProfileRequiredDirs: []string{"debug", "release"},
					MinAgeFloor:         "1h",
					Confidence:          "high",
					SafeToPrune:         true,
				},
			},
			"security": {
				"grype-db-download": {
					Description:       "grype vulnerability database partial download beside a live db/<N>",
					CandidatePatterns: []string{"**/grype/db/grype-db-download*"},
					AnyChildPaths:     []string{"vulnerability.db"},
					SiblingEvidence:   []string{"*/import.json", "*/vulnerability.db"},
					MinAgeFloor:       "1h",
					Confidence:        "high",
					SafeToPrune:       true,
				},
			},
			"go": {
				// build-cache-relocated is a Go build cache at any name and place
				// (GOCACHE pointed at a review HOME or a scratch dir): Go's README
				// marker plus trim.txt plus a two-hex-digit shard directory.
				"build-cache-relocated": {
					Description: "Go build cache at a relocated path (GOCACHE), recognised by its README marker",
					ContentMarker: &ContentMarker{
						Path:      "README",
						FirstLine: GoBuildCacheReadmeFirstLine,
					},
					RequiredChildFiles: []string{"trim.txt"},
					AnyChildPaths:      goBuildCacheShards(),
					AnyChildDirsOnly:   true,
					// Go's complete cache layout: anything else (a go.mod, .git,
					// sources) means GOCACHE was pointed at a directory that is
					// not only a cache, so the whole match is refused.
					AllowedChildNames:   []string{"README", "trim.txt", "testexpire.txt", "fuzz", "[0-9a-f][0-9a-f]"},
					ForbiddenChildPaths: projectRootMarkers(),
					DeepCheckChildDirs:  []string{"fuzz"},
					// The live default build cache stays diagnostic-only behind
					// the go-build-cache recipe (catalog development.go.go-build).
					ExcludedHomePaths:  []string{"Library/Caches/go-build", ".cache/go-build", "AppData/Local/go-build"},
					ExcludeLiveGoCache: true,
					MinAgeFloor:        "1h",
					Confidence:         "high",
					SafeToPrune:        true,
				},
				"repo-local-gocache": {
					Description:           "repo-local Go build cache",
					CandidatePatterns:     []string{"**/.gocache"},
					RequiredAncestorFiles: []string{"go.mod"},
					ContentMarker: &ContentMarker{
						Path:      "README",
						FirstLine: GoBuildCacheReadmeFirstLine,
					},
					Confidence:  "high",
					SafeToPrune: true,
				},
			},
		},
	}
}

// GoBuildCacheReadmeFirstLine is the first line Go writes to its build
// cache README; it is the content marker for repo-local .gocache.
const GoBuildCacheReadmeFirstLine = "This directory holds cached build artifacts from the Go build system."

// CacheDirTagSignatureLine is the first line of any CACHEDIR.TAG (the
// generic cache-directory tag; any tool may write it).
const CacheDirTagSignatureLine = "Signature: 8a477f597d28d172789f06886806bc55"

// CargoCacheDirTagCommentLine is the second line Cargo writes to the
// CACHEDIR.TAG at a target directory root; it is what makes the tag Cargo's.
const CargoCacheDirTagCommentLine = "# This file is a cache directory tag created by cargo."

// projectRootMarkers are entries a build-output directory never holds
// directly: a VCS directory or a project manifest. Their presence means the
// candidate is (or contains) a project, not a cache.
func projectRootMarkers() []string {
	return []string{
		".git", ".hg", ".jj", ".svn", "src",
		"Cargo.toml", "Cargo.lock", "go.mod", "package.json", "pyproject.toml", "setup.py",
		"Makefile", "pom.xml", "build.gradle", "build.gradle.kts", "CMakeLists.txt", "Gemfile", "composer.json",
	}
}

// goBuildCacheShards lists Go's build-cache shard directory names, 00 to ff.
func goBuildCacheShards() []string {
	out := make([]string, 0, 256)
	for i := 0; i < 256; i++ {
		out = append(out, fmt.Sprintf("%02x", i))
	}
	return out
}

// MergeSignatureCatalogs overlays user signatures on the built-ins. A user
// signature that reuses a built-in id keeps the built-in's safety properties:
// every evidence field the built-in declares stays required (a user may add
// evidence, never remove it), and the built-in min_age_floor stays a minimum
// (a user may raise it, never lower it). A looser rule needs a new id.
func MergeSignatureCatalogs(base SignatureCatalog, overrides SignatureCatalog) SignatureCatalog {
	merged := SignatureCatalog{}
	for domain, ecosystems := range base {
		if _, ok := merged[domain]; !ok {
			merged[domain] = map[string]map[string]Signature{}
		}
		for ecosystem, signatures := range ecosystems {
			if _, ok := merged[domain][ecosystem]; !ok {
				merged[domain][ecosystem] = map[string]Signature{}
			}
			for name, signature := range signatures {
				merged[domain][ecosystem][name] = signature
			}
		}
	}
	for domain, ecosystems := range overrides {
		if _, ok := merged[domain]; !ok {
			merged[domain] = map[string]map[string]Signature{}
		}
		for ecosystem, signatures := range ecosystems {
			if _, ok := merged[domain][ecosystem]; !ok {
				merged[domain][ecosystem] = map[string]Signature{}
			}
			for name, signature := range signatures {
				if builtIn, ok := base[domain][ecosystem][name]; ok {
					signature = keepBuiltInSafety(builtIn, signature)
				}
				merged[domain][ecosystem][name] = signature
			}
		}
	}
	return merged
}

// keepBuiltInSafety returns the user signature with the built-in's evidence
// fields re-imposed and its age floor kept as a minimum.
func keepBuiltInSafety(builtIn, user Signature) Signature {
	user.RequiredAncestorFiles = unionStrings(builtIn.RequiredAncestorFiles, user.RequiredAncestorFiles)
	user.SiblingEvidence = unionStrings(builtIn.SiblingEvidence, user.SiblingEvidence)
	user.RequiredChildFiles = unionStrings(builtIn.RequiredChildFiles, user.RequiredChildFiles)
	user.ExcludedHomePaths = unionStrings(builtIn.ExcludedHomePaths, user.ExcludedHomePaths)
	user.ExcludeLiveGoCache = user.ExcludeLiveGoCache || builtIn.ExcludeLiveGoCache
	user.ForbiddenChildPaths = unionStrings(builtIn.ForbiddenChildPaths, user.ForbiddenChildPaths)
	// Layout lists are closed: a user list could only widen them.
	if len(builtIn.KnownChildFiles) > 0 || len(builtIn.KnownChildDirs) > 0 || len(builtIn.ProfileDirMarkers) > 0 {
		user.KnownChildFiles = append([]string(nil), builtIn.KnownChildFiles...)
		user.KnownChildDirs = append([]string(nil), builtIn.KnownChildDirs...)
		user.ProfileDirMarkers = append([]string(nil), builtIn.ProfileDirMarkers...)
		user.ProfileRequiredDirs = append([]string(nil), builtIn.ProfileRequiredDirs...)
	}
	user.DeepCheckChildDirs = unionStrings(builtIn.DeepCheckChildDirs, user.DeepCheckChildDirs)
	user.AnyChildDirsOnly = user.AnyChildDirsOnly || builtIn.AnyChildDirsOnly
	// The allowed layout is a closed list: a user list could only widen it.
	if len(builtIn.AllowedChildNames) > 0 {
		user.AllowedChildNames = append([]string(nil), builtIn.AllowedChildNames...)
	}
	// CandidatePatterns and AnyChildPaths are OR lists: a user list replacing
	// the built-in could widen what matches (for example a pattern that admits
	// the live database), so the built-in lists are kept exactly. A different
	// pattern or child list needs a new id.
	user.CandidatePatterns = append([]string(nil), builtIn.CandidatePatterns...)
	if len(builtIn.AnyChildPaths) > 0 {
		user.AnyChildPaths = append([]string(nil), builtIn.AnyChildPaths...)
	}
	if builtIn.ContentMarker != nil {
		m := *builtIn.ContentMarker
		m.Lines = append([]string(nil), builtIn.ContentMarker.Lines...)
		user.ContentMarker = &m
	}
	user.MinAgeFloor = maxAge(builtIn.MinAgeFloor, user.MinAgeFloor)
	return user
}

func unionStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// maxAge returns the longer of two age strings. An unparseable user value
// never lowers a valid built-in floor.
func maxAge(builtIn, user string) string {
	b, berr := ParseAge(builtIn)
	u, uerr := ParseAge(user)
	switch {
	case berr != nil || builtIn == "":
		return user
	case uerr != nil || user == "":
		return builtIn
	case u > b:
		return user
	default:
		return builtIn
	}
}

// ParseAge parses an age like "90s", "30m", "1h", "7d" or "2w". Empty is 0.
func ParseAge(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	unit := s[len(s)-1]
	value := s[:len(s)-1]
	var n int64
	if _, err := fmt.Sscanf(value, "%d", &n); err != nil {
		return 0, err
	}
	switch unit {
	case 's':
		return time.Duration(n) * time.Second, nil
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported age unit %q", unit)
	}
}

func ValidateSignatureID(id string) error {
	parts := strings.Split(id, ".")
	if len(parts) != 3 {
		return fmt.Errorf("signature %q must use domain.ecosystem.name form", id)
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("signature %q contains an empty hierarchy segment", id)
		}
	}
	return nil
}

// BuiltInSignatureIDs returns every built-in signature id in evaluation
// order: name-pattern signatures first, then signatures discovered by content
// marker alone, each group sorted by id. A path that satisfies both a
// conventional signature and a relocated one (a target/ under Cargo.toml that
// also carries CACHEDIR.TAG) therefore keeps the conventional id.
func BuiltInSignatureIDs() []string {
	var named, markerOnly []string
	for domain, ecosystems := range BuiltInSignatures() {
		for ecosystem, signatures := range ecosystems {
			for name, sig := range signatures {
				id := domain + "." + ecosystem + "." + name
				if len(sig.CandidatePatterns) == 0 {
					markerOnly = append(markerOnly, id)
				} else {
					named = append(named, id)
				}
			}
		}
	}
	sort.Strings(named)
	sort.Strings(markerOnly)
	return append(named, markerOnly...)
}
