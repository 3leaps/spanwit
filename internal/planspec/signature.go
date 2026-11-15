package planspec

import (
	"strings"
)

// IDs flattens a signature catalog into the set of full-spine signature ids.
func (c SignatureCatalog) IDs() map[string]bool {
	out := map[string]bool{}
	for domain, ecosystems := range c {
		for ecosystem, signatures := range ecosystems {
			for name := range signatures {
				out[domain+"."+ecosystem+"."+name] = true
			}
		}
	}
	return out
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

type SignatureCatalog map[string]map[string]map[string]Signature

type Signature struct {
	Description           string   `yaml:"description,omitempty" json:"description,omitempty"`
	CandidatePatterns     []string `yaml:"candidate_patterns" json:"candidate_patterns"`
	RequiredAncestorFiles []string `yaml:"required_ancestor_files,omitempty" json:"required_ancestor_files,omitempty"`
	AnyChildPaths         []string `yaml:"any_child_paths,omitempty" json:"any_child_paths,omitempty"`
	Confidence            string   `yaml:"confidence,omitempty" json:"confidence,omitempty"`
	SafeToPrune           bool     `yaml:"safe_to_prune,omitempty" json:"safe_to_prune,omitempty"`
	// SiblingEvidence lists patterns relative to the candidate's parent
	// directory; at least one must match a plain regular file under a sibling
	// directory of the live-db shape (^[0-9]+$) that does not itself match the
	// signature's candidate patterns. Required when set.
	SiblingEvidence []string `yaml:"sibling_evidence,omitempty" json:"sibling_evidence,omitempty"`
	// ContentMarker requires a plain regular file under the candidate whose
	// leading lines equal the marker's expected lines exactly. Required when
	// set. A signature with no CandidatePatterns is discovered by its content
	// marker alone; without a marker it never matches.
	ContentMarker *ContentMarker `yaml:"content_marker,omitempty" json:"content_marker,omitempty"`
	// RequiredChildFiles lists local relative paths under the candidate; every
	// one must be a plain regular file with no symlink component. Required
	// when set (an AND list, unlike AnyChildPaths).
	RequiredChildFiles []string `yaml:"required_child_files,omitempty" json:"required_child_files,omitempty"`
	// The fields below are built-in only (never read from user config); a
	// user signature reusing a built-in id keeps the built-in values.
	//
	// ExcludedHomePaths lists paths relative to the real home directory that
	// the signature never matches (a tool's live default cache, which the
	// catalog keeps diagnostic-only behind its own recipe). When the home
	// directory cannot be resolved the signature matches nothing.
	ExcludedHomePaths []string `yaml:"-" json:"-"`
	// ExcludeLiveGoCache excludes the Go build cache the go command would
	// use: $GOCACHE, GOCACHE from the go env file, and the user cache dir's
	// go-build. Symlinked locations are resolved before comparing.
	ExcludeLiveGoCache bool `yaml:"-" json:"-"`
	// ForbiddenChildPaths lists names whose presence directly under the
	// candidate (any type) refuses the match: project or VCS markers that a
	// real cache never holds.
	ForbiddenChildPaths []string `yaml:"-" json:"-"`
	// AllowedChildNames, when set, is the complete layout: every entry
	// directly under the candidate must match one of these path.Match
	// patterns, or the match is refused.
	AllowedChildNames []string `yaml:"-" json:"-"`
	// KnownChildFiles, when set, lists the exact names of the only regular
	// files allowed directly under the candidate; any other non-directory
	// entry refuses the match.
	KnownChildFiles []string `yaml:"-" json:"-"`
	// KnownChildDirs lists path.Match patterns for directories the tool
	// itself creates directly under the candidate. With ProfileDirMarkers,
	// any other directory leaves the layout unrecognised: the match is
	// withheld (reason layout), never prunable.
	KnownChildDirs []string `yaml:"-" json:"-"`
	// ProfileDirMarkers names directories whose presence marks a direct
	// child (or a child of it, for target-triple directories) as tool
	// output even when its name is not known (custom Cargo profiles).
	ProfileDirMarkers []string `yaml:"-" json:"-"`
	// ProfileRequiredDirs lists known child directory names that must
	// themselves be profile-shaped (Cargo's debug/ and release/ always hold
	// .fingerprint after a build); otherwise the layout is unrecognised.
	ProfileRequiredDirs []string `yaml:"-" json:"-"`
	// DeepCheckChildDirs lists direct children whose own children are also
	// checked for ForbiddenChildPaths (Go's fuzz/ holds package paths).
	DeepCheckChildDirs []string `yaml:"-" json:"-"`
	// AnyChildDirsOnly requires the AnyChildPaths evidence to be a directory.
	AnyChildDirsOnly bool `yaml:"-" json:"-"`
	// MinAgeFloor is an absolute minimum age (for example "1h"). A younger
	// match is withheld for age, whatever min_age the user configures.
	MinAgeFloor string `yaml:"min_age_floor,omitempty" json:"min_age_floor,omitempty"`
}

// ContentMarker names a marker file and its exact leading lines. FirstLine is
// shorthand for a one-line Lines; when both are set, Lines[0] must equal
// FirstLine or the marker is invalid (it never matches).
type ContentMarker struct {
	Path      string   `yaml:"path" json:"path"`
	FirstLine string   `yaml:"first_line" json:"first_line"`
	Lines     []string `yaml:"lines,omitempty" json:"lines,omitempty"`
}

// ExpectedLines returns the exact leading lines the marker file must carry,
// or nil when the marker is invalid: no line given, an empty line, or a
// FirstLine that disagrees with Lines[0].
func (m ContentMarker) ExpectedLines() []string {
	lines := m.Lines
	if len(lines) == 0 {
		if m.FirstLine == "" {
			return nil
		}
		lines = []string{m.FirstLine}
	} else if m.FirstLine != "" && m.FirstLine != lines[0] {
		return nil
	}
	for _, l := range lines {
		if l == "" {
			return nil
		}
	}
	return append([]string(nil), lines...)
}
