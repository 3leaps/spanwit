package profiles

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/config"
	"github.com/3leaps/spanwit/internal/engine"
)

func fixture(t *testing.T, root string) []byte {
	t.Helper()
	policy := map[string]any{"version": 1, "defaults": map[string]any{"min_age": "24h"}, "paths": []any{map[string]any{"path": root, "max_depth": 8, "targets": []any{map[string]any{"signature": "development.rust.cargo-target"}}}}}
	data := map[string]any{"$schema": Schema, "version": 0, "recipes": map[string]any{"cargo": map[string]any{"capability": "directory-prune", "policy": policy}}, "profiles": map[string]any{"developer": map[string]any{"label": "Developer", "recipes": []string{"cargo"}}, "media": map[string]any{"label": "Media", "recipes": []string{}}}}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestPreviewPlannerParityAndNoMutation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target", "debug")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "Cargo.toml"), filepath.Join(target, "object")} {
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, old, old)
	}); err != nil {
		t.Fatal(err)
	}
	raw := fixture(t, root)
	pack, err := Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := pack.Names(); !reflect.DeepEqual(got, []string{"developer", "media"}) {
		t.Fatal(got)
	}
	preview, err := pack.Preview(context.Background(), "developer")
	if err != nil {
		t.Fatal(err)
	}
	if preview.ExecutionAuthorized || preview.State != "complete" || len(preview.Recipes) != 1 || len(preview.Recipes[0].Candidates) != 1 {
		t.Fatalf("%+v", preview)
	}
	cfg, err := config.Parse(pack.document.Recipes["cargo"].Policy)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := engine.BuildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := preview.Recipes[0].Candidates[0]
	want := direct.Candidates[0]
	if got.Path != want.Path || got.State != want.State || got.SizeBytes != want.Size || got.State != "prunable" {
		t.Fatalf("preview=%+v direct=%+v", got, want)
	}
	content, err := os.ReadFile(filepath.Join(target, "object"))
	if err != nil || string(content) != "keep" {
		t.Fatal("preview mutated candidate", err)
	}
	empty, err := pack.Preview(context.Background(), "media")
	if err != nil || len(empty.Recipes) != 0 {
		t.Fatal(empty, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pack.Preview(ctx, "developer"); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := pack.Preview(context.Background(), "missing"); err == nil {
		t.Fatal("accepted unknown profile")
	}
}
func TestPackRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	raw := string(fixture(t, t.TempDir()))
	for name, data := range map[string]string{
		"duplicate":              strings.Replace(raw, `"version":0`, `"version":0,"version":0`, 1),
		"unknown field":          strings.Replace(raw, `"version":0`, `"version":0,"activate":true`, 1),
		"unsupported capability": strings.Replace(raw, "directory-prune", "shell", 1),
		"dangling recipe":        strings.Replace(raw, `"recipes":["cargo"]`, `"recipes":["missing"]`, 1),
		"unknown signature":      strings.Replace(raw, "development.rust.cargo-target", "development.rust.missing", 1),
		"pattern only":           strings.Replace(raw, `"signature":"development.rust.cargo-target"`, `"pattern":"**/target"`, 1),
		"relative root":          string(fixture(t, "relative/cache")),
		"oversized":              strings.Repeat(" ", MaxDocumentBytes+1),
		"deep":                   strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34),
		"trailing":               raw + " {}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(strings.NewReader(data)); err == nil {
				t.Fatal("accepted", name)
			}
		})
	}
}
func TestRevisionBindsExactDocument(t *testing.T) {
	raw := fixture(t, t.TempDir())
	a, err := Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Load(bytes.NewReader(append(raw, '\n')))
	if err != nil {
		t.Fatal(err)
	}
	if a.Revision() == b.Revision() {
		t.Fatal("revision omitted reviewed bytes")
	}
}
func TestEmbeddedPolicySchemaParity(t *testing.T) {
	var pack, policy map[string]any
	if err := json.Unmarshal(spanwitschema.SpanwitProfilePackV0, &pack); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(spanwitschema.SpanwitConfigV1, &policy); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pack["$defs"].(map[string]any)["policy"], policy) {
		t.Fatal("profile policy diverges from existing config schema")
	}
}

func TestDiffShowsPolicyAndMembershipChanges(t *testing.T) {
	raw := fixture(t, t.TempDir())
	before, err := Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	recipes := doc["recipes"].(map[string]any)
	recipes["cargo"].(map[string]any)["description"] = "Retain active consumers"
	doc["profiles"].(map[string]any)["developer"].(map[string]any)["recipes"] = []string{}
	changed, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Load(bytes.NewReader(changed))
	if err != nil {
		t.Fatal(err)
	}
	diff := before.Diff(after)
	if diff.FromRevision != before.Revision() || diff.ToRevision != after.Revision() || len(diff.Recipes) != 1 || len(diff.Profiles) != 1 {
		t.Fatalf("incomplete review diff: %+v", diff)
	}
	if diff.Recipes[0].Before == nil || diff.Recipes[0].After == nil || diff.Profiles[0].Kind != "changed" {
		t.Fatal(diff)
	}
	same := before.Diff(before)
	if len(same.Recipes) != 0 || len(same.Profiles) != 0 {
		t.Fatal(same)
	}
}

func TestNewSignatureUsesExistingCapabilityWithoutCode(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cache", "objects"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "PROJECT.marker"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "cache", "objects", "compiled")
	if err := os.WriteFile(file, []byte("regenerable"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := filepath.Walk(root, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, old, old)
	}); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(fixture(t, root), &doc); err != nil {
		t.Fatal(err)
	}
	policy := doc["recipes"].(map[string]any)["cargo"].(map[string]any)["policy"].(map[string]any)
	policy["signatures"] = map[string]any{"development": map[string]any{"custom": map[string]any{"marker-cache": map[string]any{"candidate_patterns": []string{"**/cache"}, "required_ancestor_files": []string{"PROJECT.marker"}, "any_child_paths": []string{"objects"}, "min_age_floor": "1h", "safe_to_prune": true}}}}
	policy["paths"].([]any)[0].(map[string]any)["targets"] = []any{map[string]any{"signature": "development.custom.marker-cache"}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := pack.Preview(context.Background(), "developer")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Recipes[0].Candidates) != 1 || preview.Recipes[0].Candidates[0].State != "prunable" {
		t.Fatal(preview)
	}
	if err := os.Chtimes(file, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	preview, err = pack.Preview(context.Background(), "developer")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Recipes[0].Candidates) != 1 || preview.Recipes[0].Candidates[0].State != "withheld" || preview.Recipes[0].Candidates[0].WithheldReason != "age" {
		t.Fatal(preview)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("preview removed refreshed cache", err)
	}
}

func TestSignatureNameCannotDisguisePatternOnlyPolicy(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(fixture(t, t.TempDir()), &doc); err != nil {
		t.Fatal(err)
	}
	policy := doc["recipes"].(map[string]any)["cargo"].(map[string]any)["policy"].(map[string]any)
	policy["signatures"] = map[string]any{"development": map[string]any{"custom": map[string]any{"unsafe": map[string]any{"candidate_patterns": []string{"**/*"}, "safe_to_prune": true}}}}
	policy["paths"].([]any)[0].(map[string]any)["targets"] = []any{map[string]any{"signature": "development.custom.unsafe"}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bytes.NewReader(raw)); err == nil {
		t.Fatal("pattern-only policy gained a capability through a signature ID")
	}
}

func TestPreviewHomeRootsFailClosedAndIgnoreWorkingDirectory(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "cache")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	pack, err := Load(bytes.NewReader(fixture(t, "~/cache")))
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := Load(bytes.NewReader(fixture(t, root)))
	if err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{t.TempDir(), t.TempDir()} {
		t.Run(filepath.Base(cwd), func(t *testing.T) {
			t.Chdir(cwd)
			// A real fallback directory makes silent cwd enrollment observable.
			if err := os.Mkdir("cache", 0700); err != nil {
				t.Fatal(err)
			}
			for _, state := range []string{"absent", "empty", "relative", "absolute"} {
				t.Run(state, func(t *testing.T) {
					t.Setenv("HOME", "")
					switch state {
					case "absent":
						if err := os.Unsetenv("HOME"); err != nil {
							t.Fatal(err)
						}
					case "relative":
						t.Setenv("HOME", "relative-home")
					case "absolute":
						t.Setenv("HOME", home)
					}
					got, err := pack.Preview(context.Background(), "developer")
					if state != "absolute" {
						if err == nil || got.State != "" || len(got.Recipes) != 0 {
							t.Fatalf("unresolved home produced success: %+v, %v", got, err)
						}
					} else if err != nil || got.State != "complete" || !reflect.DeepEqual(got.Recipes[0].Roots, []string{root}) {
						t.Fatalf("wrong home scope: %+v, %v", got, err)
					}
					got, err = absolute.Preview(context.Background(), "developer")
					if err != nil || got.State != "complete" || !reflect.DeepEqual(got.Recipes[0].Roots, []string{root}) {
						t.Fatalf("absolute root depends on HOME: %+v, %v", got, err)
					}
					if pack.policies["cargo"].Paths[0].Path != "~/cache" {
						t.Fatal("preview changed compiled source policy")
					}
				})
			}
		})
	}
}
