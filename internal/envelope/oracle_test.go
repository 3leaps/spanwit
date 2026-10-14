package envelope_test

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/envelope"
)

// fieldClass is the disclosure role of a path-bearing schema field. The
// deriver classifies by the path string, not the field; this table is the
// oracle that every path-bearing field has been considered, and records the
// widest level it can reach.
type fieldClass int

const (
	analysis        fieldClass = iota // the analysis root or under it
	anchor                            // the pressure anchor: home (system) or root
	mount                             // a mount: system only for /, /System/Volumes/Data, /home
	catalog                           // a compiled catalog constant
	fileName                          // a file name only (compare inputs)
	imageRef                          // a container image ref (image_refs local)
	platform                          // an image os/arch platform string ("linux/amd64")
	goos                              // a runtime GOOS value ("linux"), never a slash
	pattern                           // a built-in signature glob ("**/target")
	evidence                          // evidence: "child:"/"pattern:" + built-in, or an absolute path
	processIdentity                   // a held-open holder (process_identity)
	full                              // any path (full)
)

// pathFields lists every path-bearing string field in each report schema.
var pathFields = map[string]map[string]fieldClass{
	"observe-health.v1": {
		"/proposals/*/evidence/*": evidence,
	},
	"space-report.v1": {
		"/container_runtime/image_refs/*/platform":                   platform,
		"/hotspots/*/evidence/*":                                     evidence,
		"/hotspots/*/pattern":                                        pattern,
		"/prune_handoff/candidates/*/evidence/*":                     evidence,
		"/prune_handoff/candidates/*/pattern":                        pattern,
		"/prune_handoff/exact_plan/candidates/*/evidence/*":          evidence,
		"/prune_handoff/exact_plan/candidates/*/pattern":             pattern,
		"/prune_handoff/temp_exact_plans/*/candidates/*/evidence/*":  evidence,
		"/prune_handoff/temp_exact_plans/*/candidates/*/pattern":     pattern,
		"/recipes/*/exact_plan/candidates/*/evidence/*":              evidence,
		"/recipes/*/exact_plan/candidates/*/pattern":                 pattern,
		"/unknown/*/evidence/*":                                      evidence,
		"/unknown/*/pattern":                                         pattern,
		"/unverified/entries/*/evidence/*":                           evidence,
		"/unverified/entries/*/pattern":                              pattern,
		"/verified_reclaimable/candidates/*/evidence/*":              evidence,
		"/verified_reclaimable/candidates/*/pattern":                 pattern,
		"/analysis_pressure/mount":                                   mount,
		"/analysis_pressure/path":                                    analysis,
		"/container_runtime/image_refs/*/ref":                        imageRef,
		"/container_runtime/recipe/*/command/*":                      full,
		"/hotspots/*/path":                                           full,
		"/pressure/mount":                                            mount,
		"/pressure/path":                                             anchor,
		"/prune_handoff/analysis_root":                               analysis,
		"/prune_handoff/candidates/*/parent_path":                    analysis,
		"/prune_handoff/candidates/*/path":                           analysis,
		"/prune_handoff/exact_plan/analysis_root":                    analysis,
		"/prune_handoff/exact_plan/candidates/*/parent_path":         analysis,
		"/prune_handoff/exact_plan/candidates/*/path":                analysis,
		"/prune_handoff/suggested_commands/*/args/*":                 full,
		"/prune_handoff/suggested_commands/*/display":                full,
		"/prune_handoff/temp_exact_plans/*/analysis_root":            full,
		"/prune_handoff/temp_exact_plans/*/candidates/*/parent_path": full,
		"/prune_handoff/temp_exact_plans/*/candidates/*/path":        full,
		"/recipes/*/exact_plan/analysis_root":                        analysis,
		"/recipes/*/exact_plan/candidates/*/parent_path":             analysis,
		"/recipes/*/exact_plan/candidates/*/path":                    analysis,
		"/recipes/*/keep/*":                                          full,
		"/recipes/*/path":                                            full,
		"/recipes/*/steps/*/command/args/*":                          full,
		"/recipes/*/steps/*/command/display":                         full,
		"/recipes/*/steps/*/paths/*":                                 full,
		"/recipes/*/suggested_commands/*/args/*":                     full,
		"/recipes/*/suggested_commands/*/display":                    full,
		"/root":                              analysis,
		"/temp_plane_coverage/planes/*/root": full,
		"/temp_roots/*/root":                 full,
		"/unknown/*/path":                    analysis,
		"/unverified/entries/*/path":         analysis,
		"/verified_reclaimable/candidates/*/parent_path": analysis,
		"/verified_reclaimable/candidates/*/path":        analysis,
	},
	"space-report.v2": {
		"/capacity_accounting/target/platform":                                 goos,
		"/container_runtime/image_refs/*/platform":                             platform,
		"/hotspots/*/evidence/*":                                               evidence,
		"/hotspots/*/pattern":                                                  pattern,
		"/prune_handoff/candidates/*/evidence/*":                               evidence,
		"/prune_handoff/candidates/*/pattern":                                  pattern,
		"/prune_handoff/exact_plan/candidates/*/evidence/*":                    evidence,
		"/prune_handoff/exact_plan/candidates/*/pattern":                       pattern,
		"/prune_handoff/temp_exact_plans/*/candidates/*/evidence/*":            evidence,
		"/prune_handoff/temp_exact_plans/*/candidates/*/pattern":               pattern,
		"/recipes/*/exact_plan/candidates/*/evidence/*":                        evidence,
		"/recipes/*/exact_plan/candidates/*/pattern":                           pattern,
		"/unknown/*/evidence/*":                                                evidence,
		"/unknown/*/pattern":                                                   pattern,
		"/unverified/entries/*/evidence/*":                                     evidence,
		"/unverified/entries/*/pattern":                                        pattern,
		"/verified_reclaimable/candidates/*/evidence/*":                        evidence,
		"/verified_reclaimable/candidates/*/pattern":                           pattern,
		"/analysis_pressure/mount":                                             mount,
		"/analysis_pressure/path":                                              analysis,
		"/capacity_accounting/planes/container/coverage/gaps/*/paths/*":        full,
		"/capacity_accounting/planes/filesystem/coverage/gaps/*/paths/*":       full,
		"/capacity_accounting/planes/held_open/coverage/gaps/*/paths/*":        full,
		"/capacity_accounting/planes/held_open/entries/*/holders/*/executable": processIdentity,
		"/capacity_accounting/planes/held_open/entries/*/path":                 full,
		"/capacity_accounting/planes/snapshots/coverage/gaps/*/paths/*":        full,
		"/capacity_accounting/planes/system_managed/coverage/gaps/*/paths/*":   full,
		"/capacity_accounting/planes/system_managed/entries/*/path":            catalog,
		"/capacity_accounting/planes/volumes/coverage/gaps/*/paths/*":          full,
		"/capacity_accounting/planes/volumes/entries/*/mount":                  mount,
		"/capacity_accounting/target/mount":                                    mount,
		"/capacity_accounting/target/path":                                     anchor,
		"/capacity_accounting/target/resolved_path":                            full,
		"/container_runtime/image_refs/*/ref":                                  imageRef,
		"/container_runtime/recipe/*/command/*":                                full,
		"/hotspots/*/path":                                                     full,
		"/pressure/mount":                                                      mount,
		"/pressure/path":                                                       anchor,
		"/prune_handoff/analysis_root":                                         analysis,
		"/prune_handoff/candidates/*/parent_path":                              analysis,
		"/prune_handoff/candidates/*/path":                                     analysis,
		"/prune_handoff/exact_plan/analysis_root":                              analysis,
		"/prune_handoff/exact_plan/candidates/*/parent_path":                   analysis,
		"/prune_handoff/exact_plan/candidates/*/path":                          analysis,
		"/prune_handoff/suggested_commands/*/args/*":                           full,
		"/prune_handoff/suggested_commands/*/display":                          full,
		"/prune_handoff/temp_exact_plans/*/analysis_root":                      full,
		"/prune_handoff/temp_exact_plans/*/candidates/*/parent_path":           full,
		"/prune_handoff/temp_exact_plans/*/candidates/*/path":                  full,
		"/recipes/*/exact_plan/analysis_root":                                  analysis,
		"/recipes/*/exact_plan/candidates/*/parent_path":                       analysis,
		"/recipes/*/exact_plan/candidates/*/path":                              analysis,
		"/recipes/*/keep/*":                                                    full,
		"/recipes/*/path":                                                      full,
		"/recipes/*/steps/*/command/args/*":                                    full,
		"/recipes/*/steps/*/command/display":                                   full,
		"/recipes/*/steps/*/paths/*":                                           full,
		"/recipes/*/suggested_commands/*/args/*":                               full,
		"/recipes/*/suggested_commands/*/display":                              full,
		"/root":                              analysis,
		"/temp_plane_coverage/planes/*/root": full,
		"/temp_roots/*/root":                 full,
		"/unknown/*/path":                    analysis,
		"/unverified/entries/*/path":         analysis,
		"/verified_reclaimable/candidates/*/parent_path": analysis,
		"/verified_reclaimable/candidates/*/path":        analysis,
	},
	"space-compare.v1": {
		"/left/platform":  goos,
		"/right/platform": goos,
		"/left/path":      fileName,
		"/right/path":     fileName,
	},
	"prune-plan.v1": {
		"/candidates/*/evidence/*":  evidence,
		"/candidates/*/pattern":     pattern,
		"/candidates/*/parent_path": analysis,
		"/candidates/*/path":        analysis,
		"/config_path":              full,
	},
	"observe-state.v1": {
		"/volumes/{}/last_attempt/mount_path": full,
		"/volumes/{}/last_success/mount_path": full,
		"/volumes/{}/volume/register_path":    full,
	},
	"exact-prune-plan.v1": {
		"/candidates/*/evidence/*":  evidence,
		"/candidates/*/pattern":     pattern,
		"/analysis_root":            analysis,
		"/candidates/*/parent_path": analysis,
		"/candidates/*/path":        analysis,
	},
}

var oracleSchemas = map[string][]byte{
	"space-report.v1":     spanwitschema.SpanwitSpaceReportV1,
	"space-report.v2":     spanwitschema.SpanwitSpaceReportV2,
	"space-compare.v1":    spanwitschema.SpanwitSpaceCompareV1,
	"prune-plan.v1":       spanwitschema.SpanwitPrunePlanV1,
	"observe-health.v1":   spanwitschema.SpanwitObserveHealthV1,
	"observe-state.v1":    spanwitschema.SpanwitObserveStateV1,
	"exact-prune-plan.v1": spanwitschema.SpanwitExactPrunePlanV1,
}

// pathName matches property names that can carry a path, a command line, an
// image ref or an executable.
var pathName = regexp.MustCompile(`(^|_)(path|paths|root|mount|keep|args|display|command|ref|executable|platform|pattern|evidence)$`)

// A path-bearing field missing from the oracle fails, so a new one cannot
// ship unclassified; a stale oracle entry fails too.
func TestEveryPathFieldIsClassified(t *testing.T) {
	for short, raw := range oracleSchemas {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", short, err)
		}
		defs, _ := doc["$defs"].(map[string]any)
		found := map[string]bool{}
		walkSchema(doc, "", defs, found, map[string]bool{})
		want := pathFields[short]
		var missing, stale []string
		for p := range found {
			if _, ok := want[p]; !ok {
				missing = append(missing, p)
			}
		}
		for p := range want {
			if !found[p] {
				stale = append(stale, p)
			}
		}
		sort.Strings(missing)
		sort.Strings(stale)
		for _, p := range missing {
			t.Errorf("%s%s: path-bearing field is not classified in pathFields", short, p)
		}
		for _, p := range stale {
			t.Errorf("%s%s: pathFields entry matches no schema field (stale)", short, p)
		}
	}
}

func walkSchema(s any, ptr string, defs map[string]any, found, seen map[string]bool) {
	m, ok := s.(map[string]any)
	if !ok {
		return
	}
	if ref, ok := m["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		key := name + "@" + ptr
		if seen[key] {
			return
		}
		seen[key] = true
		walkSchema(defs[name], ptr, defs, found, seen)
		return
	}
	for _, k := range []string{"allOf", "anyOf", "oneOf"} {
		if xs, ok := m[k].([]any); ok {
			for _, x := range xs {
				walkSchema(x, ptr, defs, found, seen)
			}
		}
	}
	for _, k := range []string{"then", "else"} {
		if x, ok := m[k]; ok {
			walkSchema(x, ptr, defs, found, seen)
		}
	}
	if isString(m["type"]) && ptr != "" && !strings.HasPrefix(ptr, "/disclosure/") {
		segs := strings.Split(ptr, "/")
		leaf := ""
		for i := len(segs) - 1; i >= 0; i-- {
			if segs[i] != "*" && segs[i] != "{}" && segs[i] != "" {
				leaf = segs[i]
				break
			}
		}
		if pathName.MatchString(leaf) {
			found[ptr] = true
		}
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for k, v := range props {
			walkSchema(v, ptr+"/"+k, defs, found, seen)
		}
	}
	if items, ok := m["items"]; ok {
		walkSchema(items, ptr+"/*", defs, found, seen)
	}
	if ap, ok := m["additionalProperties"].(map[string]any); ok {
		walkSchema(ap, ptr+"/{}", defs, found, seen)
	}
}

func isString(t any) bool {
	switch v := t.(type) {
	case string:
		return v == "string"
	case []any:
		for _, x := range v {
			if x == "string" {
				return true
			}
		}
	}
	return false
}

// Each class reaches the level the disclosure table states: a sample value
// placed at every classified field derives no wider than its class allows,
// and an arbitrary path there always derives full (classification is by the
// string, never the field).
func TestPathFieldClassesDeriveTheirLevel(t *testing.T) {
	const root, home = "/an", "/fixture/usr-u"
	ctx := envelope.Context{Roots: []string{root}, Home: home}
	sample := map[fieldClass]struct{ value, want string }{
		analysis:        {root + "/x", envelope.PathsAnalysis},
		anchor:          {home, envelope.PathsSystem},
		mount:           {"/System/Volumes/Data", envelope.PathsSystem},
		catalog:         {"/System/Library/AssetsV2", envelope.PathsAnalysis},
		fileName:        {"before.json", envelope.PathsNone},
		platform:        {"linux/amd64", envelope.PathsNone},
		goos:            {"darwin", envelope.PathsNone},
		pattern:         {"**/target", envelope.PathsNone},
		evidence:        {"pattern:**/.gocache", envelope.PathsNone},
		imageRef:        {"registry.example/app:1", envelope.PathsNone},
		processIdentity: {"Codex", envelope.PathsNone},
		full:            {"/fixture/usr-u/cache/x", envelope.PathsFull},
	}
	for short, fields := range pathFields {
		for ptr, class := range fields {
			s := sample[class]
			if got := envelope.Derive(place(ptr, s.value), ctx).Paths; got != s.want {
				t.Errorf("%s%s (class %d): %q derived %s, want %s", short, ptr, class, s.value, got, s.want)
			}
			if got := envelope.Derive(place(ptr, "/etc/secret"), ctx).Paths; got != envelope.PathsFull {
				t.Errorf("%s%s: an arbitrary path must derive full, got %s", short, ptr, got)
			}
		}
	}
}

// place builds a minimal document holding value at ptr ("*" is an array
// element, "{}" a map value).
func place(ptr, value string) map[string]any {
	segs := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	var build func(i int) any
	build = func(i int) any {
		if i == len(segs) {
			return value
		}
		switch segs[i] {
		case "*":
			return []any{build(i + 1)}
		case "{}":
			return map[string]any{"k": build(i + 1)}
		default:
			return map[string]any{segs[i]: build(i + 1)}
		}
	}
	return build(0).(map[string]any)
}
