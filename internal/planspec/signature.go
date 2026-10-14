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
	// first line equals FirstLine exactly. Required when set.
	ContentMarker *ContentMarker `yaml:"content_marker,omitempty" json:"content_marker,omitempty"`
	// MinAgeFloor is an absolute minimum age (for example "1h"). A younger
	// match is withheld for age, whatever min_age the user configures.
	MinAgeFloor string `yaml:"min_age_floor,omitempty" json:"min_age_floor,omitempty"`
}

// ContentMarker names a marker file and its exact first line.
type ContentMarker struct {
	Path      string `yaml:"path" json:"path"`
	FirstLine string `yaml:"first_line" json:"first_line"`
}
