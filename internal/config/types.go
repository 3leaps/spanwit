package config

import "github.com/3leaps/spanwit/internal/catalog"

// Config is the file-backed configuration for spanwit.
type Config struct {
	Version    int              `yaml:"version"`
	Defaults   Defaults         `yaml:"defaults"`
	Signatures SignatureCatalog `yaml:"signatures,omitempty"`
	Domains    *DomainsConfig   `yaml:"domains,omitempty"`
	Paths      []PathProfile    `yaml:"paths"`
}

// DomainsConfig is the optional use-domain policy block. It authorizes recipes
// and reclaim eligibility (never view projection — that is 014 --domain/--class).
//
// Enabled uses pointer semantics so the loader can distinguish three cases:
//   - omitted (nil): built-in default enabled set (currently {development})
//   - explicit empty list: no domains enabled — broad inventory, but no
//     domain-driven recipes or reclaim eligibility
//   - explicit non-empty list: exactly those domains authorized
type DomainsConfig struct {
	Enabled *[]string       `yaml:"enabled,omitempty"`
	Catalog []catalog.Entry `yaml:"catalog,omitempty"`
}

// Defaults are optional global filters. Age uses a window on file age
// (now − mtime): min_age is the older-than floor, max_age is the younger-than
// ceiling. Both unset means no age gate.
type Defaults struct {
	MinSize string `yaml:"min_size,omitempty"`
	MinAge  string `yaml:"min_age,omitempty"`
	MaxAge  string `yaml:"max_age,omitempty"`
}

type PathProfile struct {
	Path     string   `yaml:"path"`
	Enabled  *bool    `yaml:"enabled,omitempty"`
	MinSize  string   `yaml:"min_size,omitempty"`
	MinAge   string   `yaml:"min_age,omitempty"`
	MaxAge   string   `yaml:"max_age,omitempty"`
	MaxDepth int      `yaml:"max_depth,omitempty"`
	Targets  []Target `yaml:"targets,omitempty"`
	Ignores  []string `yaml:"ignores,omitempty"`
}

func (p PathProfile) IsEnabled() bool {
	return p.Enabled == nil || *p.Enabled
}

type Target struct {
	Signature string `yaml:"signature,omitempty"`
	Pattern   string `yaml:"pattern,omitempty"`
	MinSize   string `yaml:"min_size,omitempty"`
	MinAge    string `yaml:"min_age,omitempty"`
	MaxAge    string `yaml:"max_age,omitempty"`
}

type SignatureCatalog map[string]map[string]map[string]Signature

type Signature struct {
	Description           string   `yaml:"description,omitempty" json:"description,omitempty"`
	CandidatePatterns     []string `yaml:"candidate_patterns" json:"candidate_patterns"`
	RequiredAncestorFiles []string `yaml:"required_ancestor_files,omitempty" json:"required_ancestor_files,omitempty"`
	AnyChildPaths         []string `yaml:"any_child_paths,omitempty" json:"any_child_paths,omitempty"`
	Confidence            string   `yaml:"confidence,omitempty" json:"confidence,omitempty"`
	SafeToPrune           bool     `yaml:"safe_to_prune,omitempty" json:"safe_to_prune,omitempty"`
}

// EmptyConfig returns a version-1 config shell with no policy defaults.
// Age and size gates are opt-in: omitted fields stay unset after load and mean
// "no gate". Do not seed min_size / min_age / max_age here — YAML decode-over
// would silently inject them when the user omits nested fields under defaults:.
func EmptyConfig() *Config {
	return &Config{
		Version: 1,
	}
}

// DefaultsConfig is an alias for EmptyConfig kept for call-site clarity in
// older code. Prefer EmptyConfig for new call sites.
func DefaultsConfig() *Config {
	return EmptyConfig()
}
