package config

import (
	"fmt"
	"strings"
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
		},
	}
}

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
				merged[domain][ecosystem][name] = signature
			}
		}
	}
	return merged
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
