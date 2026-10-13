package config

import (
	"fmt"
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

func (c SignatureCatalog) Lookup(id string) (Signature, bool) {
	parts := strings.Split(id, ".")
	if len(parts) != 3 {
		return Signature{}, false
	}
	ecosystems, ok := c[parts[0]]
	if !ok {
		return Signature{}, false
	}
	signatures, ok := ecosystems[parts[1]]
	if !ok {
		return Signature{}, false
	}
	signature, ok := signatures[parts[2]]
	return signature, ok
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
