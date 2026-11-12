// Package profiles compiles reviewed persona bundles into existing policies.
// It has no activation, scheduling, command execution or deletion path.
package profiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/engine"
)

const MaxDocumentBytes = 1 << 20
const Schema = "https://schemas.3leaps.dev/spanwit/profile-pack/v0.json"

type Profile struct {
	Label   string   `json:"label"`
	Recipes []string `json:"recipes"`
}
type Recipe struct {
	Capability  string          `json:"capability"`
	Description string          `json:"description,omitempty"`
	Policy      json.RawMessage `json:"policy"`
}
type Document struct {
	Schema   string             `json:"$schema"`
	Version  int                `json:"version"`
	Recipes  map[string]Recipe  `json:"recipes"`
	Profiles map[string]Profile `json:"profiles"`
}
type Pack struct {
	document Document
	revision string
	policies map[string]*config.Config
}

// Load rejects duplicate object keys and excessive nesting before schema
// evaluation. The revision hashes the exact reviewed bytes, not a normalized
// semantic representation; even a whitespace edit creates a new revision.
func Load(r io.Reader) (*Pack, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxDocumentBytes {
		return nil, fmt.Errorf("profile document exceeds %d bytes", MaxDocumentBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := checkValue(dec, 0); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing profile data")
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitProfilePackV0, raw); err != nil {
		return nil, err
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	pack := &Pack{document: doc, revision: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), policies: map[string]*config.Config{}}
	for _, id := range sortedKeys(doc.Recipes) {
		recipe := doc.Recipes[id]
		cfg, err := config.Parse(recipe.Policy)
		if err != nil {
			return nil, fmt.Errorf("recipe %s: %w", id, err)
		}
		// Pattern-only matching is intentionally not an authoring capability here.
		// Custom signatures remain supported through the existing evidence contract.
		signatures := config.MergeSignatureCatalogs(config.BuiltInSignatures(), cfg.Signatures)
		for _, path := range cfg.Paths {
			if !filepath.IsAbs(path.Path) && !strings.HasPrefix(path.Path, "~/") {
				return nil, fmt.Errorf("recipe %s requires absolute or home-relative roots", id)
			}
			for _, target := range path.Targets {
				if target.Signature == "" {
					return nil, fmt.Errorf("recipe %s requires signature-backed targets", id)
				}
				sig, _ := signatures.Lookup(target.Signature)
				if len(sig.RequiredAncestorFiles) == 0 && len(sig.AnyChildPaths) == 0 && len(sig.SiblingEvidence) == 0 && sig.ContentMarker == nil {
					return nil, fmt.Errorf("recipe %s requires context evidence beyond a candidate pattern", id)
				}
			}
		}
		pack.policies[id] = cfg
	}
	for _, id := range sortedKeys(doc.Profiles) {
		for _, recipe := range doc.Profiles[id].Recipes {
			if _, ok := pack.policies[recipe]; !ok {
				return nil, fmt.Errorf("profile %s references unknown recipe %s", id, recipe)
			}
		}
	}
	return pack, nil
}

func (p *Pack) Revision() string { return p.revision }
func (p *Pack) Names() []string  { return sortedKeys(p.document.Profiles) }

type Candidate struct {
	Path           string `json:"path"`
	State          string `json:"state"`
	Signature      string `json:"signature"`
	SizeBytes      int64  `json:"size_bytes"`
	SizeBasis      string `json:"size_basis"`
	SizeIncomplete bool   `json:"size_incomplete"`
	WithheldReason string `json:"withheld_reason,omitempty"`
}
type RecipePreview struct {
	Roots      []string    `json:"roots"`
	ID         string      `json:"id"`
	State      string      `json:"state"`
	Candidates []Candidate `json:"candidates"`
	Warnings   []string    `json:"warnings"`
}
type Preview struct {
	Version             int             `json:"version"`
	Revision            string          `json:"revision"`
	Profile             string          `json:"profile"`
	State               string          `json:"state"`
	ExecutionAuthorized bool            `json:"execution_authorized"`
	Recipes             []RecipePreview `json:"recipes"`
}

// Preview uses the existing planner without exporting a deletion handoff. Rows
// from different recipes may overlap; no misleading aggregate reclaim is given.
func (p *Pack) Preview(ctx context.Context, name string) (Preview, error) {
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	profile, ok := p.document.Profiles[name]
	if !ok {
		return Preview{}, fmt.Errorf("unknown profile %q", name)
	}
	out := Preview{Revision: p.revision, Profile: name, State: "complete", Recipes: []RecipePreview{}}
	for _, id := range profile.Recipes {
		if err := ctx.Err(); err != nil {
			return Preview{}, err
		}
		plan, err := engine.BuildPrunePlan(ctx, p.policies[id])
		if err != nil {
			return Preview{}, fmt.Errorf("recipe %s: %w", id, err)
		}
		row := RecipePreview{ID: id, State: "complete", Roots: []string{}, Candidates: []Candidate{}, Warnings: []string{}}
		for _, path := range p.policies[id].Paths {
			if path.IsEnabled() {
				root, err := engine.CleanConfiguredPath(path.Path)
				if err != nil {
					return Preview{}, err
				}
				row.Roots = append(row.Roots, root)
			}
		}
		for _, warning := range plan.Warnings {
			row.Warnings = append(row.Warnings, warning.Error())
		}
		if len(plan.Warnings) > 0 || plan.SizeIncomplete {
			row.State = "partial"
			out.State = "partial"
		}
		for _, c := range plan.Candidates {
			row.Candidates = append(row.Candidates, Candidate{Path: c.Path, State: c.State, Signature: c.Signature, SizeBytes: c.Size, SizeBasis: c.SizeBasis, SizeIncomplete: c.SizeIncomplete, WithheldReason: c.WithheldReason})
		}
		out.Recipes = append(out.Recipes, row)
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func checkValue(d *json.Decoder, depth int) error {
	if depth > 32 {
		return fmt.Errorf("profile nesting exceeds 32")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return fmt.Errorf("duplicate or invalid profile key")
			}
			seen[s] = true
			if err := checkValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("invalid profile object")
		}
	case '[':
		for d.More() {
			if err := checkValue(d, depth+1); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("invalid profile array")
		}
	default:
		return fmt.Errorf("invalid profile delimiter")
	}
	return nil
}

// Change exposes both sides of a reviewed edit, including dormant recipes.
type Change struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}
type Diff struct {
	Version      int      `json:"version"`
	FromRevision string   `json:"from_revision"`
	ToRevision   string   `json:"to_revision"`
	Recipes      []Change `json:"recipes"`
	Profiles     []Change `json:"profiles"`
}

func (p *Pack) Diff(next *Pack) Diff {
	return Diff{FromRevision: p.revision, ToRevision: next.revision, Recipes: changes(p.document.Recipes, next.document.Recipes), Profiles: changes(p.document.Profiles, next.document.Profiles)}
}
func changes[V any](before, after map[string]V) []Change {
	all := map[string]bool{}
	for id := range before {
		all[id] = true
	}
	for id := range after {
		all[id] = true
	}
	out := []Change{}
	for _, id := range sortedKeys(all) {
		a, aok := before[id]
		b, bok := after[id]
		switch {
		case !aok:
			out = append(out, Change{ID: id, Kind: "added", After: b})
		case !bok:
			out = append(out, Change{ID: id, Kind: "removed", Before: a})
		default:
			// All values come from strictly validated JSON. Marshaling sorts map keys
			// and compacts embedded policy JSON for a stable review comparison.
			ar, _ := json.Marshal(a)
			br, _ := json.Marshal(b)
			if !bytes.Equal(ar, br) {
				out = append(out, Change{ID: id, Kind: "changed", Before: a, After: b})
			}
		}
	}
	return out
}
