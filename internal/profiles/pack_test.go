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
	"github.com/3leaps/spanwit/internal/observe"
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

func makeCargoProject(t *testing.T, parent, name string, payloadBytes int, age time.Duration) string {
	t.Helper()
	proj := filepath.Join(parent, name)
	targetDebug := filepath.Join(proj, "target", "debug")
	if err := os.MkdirAll(targetDebug, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "Cargo.toml"), []byte("[package]\nname = \""+name+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(targetDebug, "artifact.bin")
	if err := os.WriteFile(artifact, bytes.Repeat([]byte("x"), payloadBytes), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := filepath.Walk(proj, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, stamp, stamp)
	}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(proj, "target")
}

func TestRecipeBudgetEvaluationAndOldestFirstSelection(t *testing.T) {
	root := t.TempDir()
	// Three prunable targets (48h, 72h, 96h old) and one recent protected target (2h old).
	t96 := makeCargoProject(t, root, "proj-96h", 4000, 96*time.Hour)
	t72 := makeCargoProject(t, root, "proj-72h", 4000, 72*time.Hour)
	t48 := makeCargoProject(t, root, "proj-48h", 4000, 48*time.Hour)
	t02 := makeCargoProject(t, root, "proj-02h", 4000, 2*time.Hour)

	buildPack := func(maxBytes int64) *Pack {
		t.Helper()
		policy := map[string]any{
			"version":  1,
			"defaults": map[string]any{"min_age": "24h"},
			"paths": []any{
				map[string]any{
					"path":      root,
					"max_depth": 8,
					"targets":   []any{map[string]any{"signature": "development.rust.cargo-target"}},
				},
			},
		}
		data := map[string]any{
			"$schema": Schema,
			"version": 0,
			"recipes": map[string]any{
				"cargo": map[string]any{
					"capability": "directory-prune",
					"budget": map[string]any{
						"max_bytes":      maxBytes,
						"eviction_order": EvictionOrderIdleOldestFirst,
						"authority":      AuthorityPropose,
					},
					"policy": policy,
				},
			},
			"profiles": map[string]any{
				"developer": map[string]any{
					"label":   "Developer",
					"recipes": []string{"cargo"},
				},
			},
		}
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		pack, err := Load(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return pack
	}

	// First inspect observed sizes with a generous ceiling so we know exact filesystem block/logical sizes.
	generous := buildPack(10 * 1024 * 1024)
	pvWithin, err := generous.Preview(context.Background(), "developer")
	if err != nil {
		t.Fatal(err)
	}
	bWithin := pvWithin.Recipes[0].Budget
	if bWithin == nil || bWithin.Status != BudgetStatusWithin || bWithin.OverageBytes != 0 || bWithin.SelectedCount != 0 || bWithin.ApprovalState != ApprovalStateNotNeeded {
		t.Fatalf("expected within budget, got %+v", bWithin)
	}
	if bWithin.RetainedBytes != bWithin.ObservedBytes || bWithin.EligibleBytes <= 0 || bWithin.ProtectedBytes <= 0 {
		t.Fatalf("unexpected byte breakdown: %+v", bWithin)
	}
	for _, c := range pvWithin.Recipes[0].Candidates {
		if c.BudgetSelected {
			t.Fatalf("expected no candidate selected when within budget: %+v", c)
		}
		if c.State == engine.CandidateStatePrunable && !c.BudgetRetained {
			t.Fatalf("expected prunable candidate marked retained when within budget: %+v", c)
		}
	}

	// Set ceiling so overage requires selecting the 2 oldest prunable candidates (t96 and t72) while retaining t48 and protected t02.
	oneCandSize := bWithin.EligibleBytes / 3
	ceilingForTwo := bWithin.ObservedBytes - (oneCandSize + 1)
	packTwo := buildPack(ceilingForTwo)
	pvTwo, err := packTwo.Preview(context.Background(), "developer")
	if err != nil {
		t.Fatal(err)
	}
	bTwo := pvTwo.Recipes[0].Budget
	if bTwo == nil || bTwo.Status != BudgetStatusExceeded || bTwo.SelectedCount != 2 || bTwo.ApprovalState != ApprovalStateApprovalRequired {
		t.Fatalf("expected exceeded with 2 oldest candidates selected: %+v", bTwo)
	}
	selectedPaths := map[string]bool{}
	retainedPaths := map[string]bool{}
	for _, c := range pvTwo.Recipes[0].Candidates {
		if c.BudgetSelected {
			selectedPaths[c.Path] = true
		}
		if c.BudgetRetained {
			retainedPaths[c.Path] = true
		}
	}
	if !selectedPaths[t96] || !selectedPaths[t72] || selectedPaths[t48] || selectedPaths[t02] {
		t.Fatalf("wrong oldest-first selection: selected=%v retained=%v", selectedPaths, retainedPaths)
	}
	if !retainedPaths[t48] {
		t.Fatalf("expected newest prunable candidate %s retained under ceiling", t48)
	}

	// Set ceiling smaller than protected bytes alone: protected_overage_bytes > 0 and protected t02 is still never selected.
	ceilingBelowProtected := bWithin.ProtectedBytes / 2
	if ceilingBelowProtected < 1 {
		ceilingBelowProtected = 1
	}
	packProtectedOver := buildPack(ceilingBelowProtected)
	pvProt, err := packProtectedOver.Preview(context.Background(), "developer")
	if err != nil {
		t.Fatal(err)
	}
	bProt := pvProt.Recipes[0].Budget
	if bProt == nil || bProt.Status != BudgetStatusExceeded || bProt.ProtectedOverageBytes != bWithin.ProtectedBytes-ceilingBelowProtected {
		t.Fatalf("expected protected overage reported: %+v", bProt)
	}
	for _, c := range pvProt.Recipes[0].Candidates {
		if c.Path == t02 && c.BudgetSelected {
			t.Fatalf("protected candidate must never be selected by budget: %+v", c)
		}
	}
}

func TestRecipeBudgetBlockedByProtectionAndUnknownOnIncompleteScan(t *testing.T) {
	root := t.TempDir()
	// Only a recent (2h old) candidate exists; min_age is 24h so 0 eligible bytes.
	_ = makeCargoProject(t, root, "proj-recent", 4000, 2*time.Hour)

	makeDoc := func(maxDepth int) *Pack {
		t.Helper()
		policy := map[string]any{
			"version":  1,
			"defaults": map[string]any{"min_age": "24h"},
			"paths": []any{
				map[string]any{
					"path":      root,
					"max_depth": maxDepth,
					"targets":   []any{map[string]any{"signature": "development.rust.cargo-target"}},
				},
			},
		}
		data := map[string]any{
			"$schema": Schema,
			"version": 0,
			"recipes": map[string]any{
				"cargo": map[string]any{
					"capability": "directory-prune",
					"budget": map[string]any{
						"max_bytes":      100,
						"eviction_order": EvictionOrderIdleOldestFirst,
						"authority":      AuthorityPropose,
					},
					"policy": policy,
				},
			},
			"profiles": map[string]any{
				"developer": map[string]any{
					"label":   "Developer",
					"recipes": []string{"cargo"},
				},
			},
		}
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		pack, err := Load(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return pack
	}

	pack := makeDoc(8)
	pv, err := pack.Preview(context.Background(), "developer")
	if err != nil {
		t.Fatal(err)
	}
	b := pv.Recipes[0].Budget
	if b == nil || b.Status != BudgetStatusExceeded || b.SelectedCount != 0 || b.ApprovalState != ApprovalStateBlockedByProtection || b.ProtectedOverageBytes <= 0 {
		t.Fatalf("expected blocked_by_protection when only protected candidates exceed ceiling: %+v", b)
	}

	// Now force an incomplete walk via an unreadable subdirectory inside the target so sizing/walk emits a warning.
	unreadable := filepath.Join(root, "proj-recent", "target", "debug", "locked-sub")
	if err := os.MkdirAll(unreadable, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(unreadable, 0700)
	})
	pvInc, err := pack.Preview(context.Background(), "developer")
	if err != nil {
		t.Fatal(err)
	}
	bInc := pvInc.Recipes[0].Budget
	if bInc == nil || bInc.Status != BudgetStatusUnknown || bInc.ApprovalState != ApprovalStateUnknownObservation {
		t.Fatalf("expected incomplete scan to report unknown budget status, got %+v", bInc)
	}
}

func TestVolumeObjectiveEvaluationAndCrossRecipeDeduplication(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "workspace")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	// Create a prunable cargo target inside root/workspace so two recipes scanning
	// `root` and `root/workspace` discover the exact same candidate path.
	_ = makeCargoProject(t, sub, "svc-a", 8192, 72*time.Hour)

	const (
		gi             = int64(1024 * 1024 * 1024)
		floorBytes     = 30 * gi
		recoveryTarget = 36 * gi
	)

	makePolicy := func(scanPath string) map[string]any {
		return map[string]any{
			"version":  1,
			"defaults": map[string]any{"min_age": "24h"},
			"paths": []any{
				map[string]any{
					"path":      scanPath,
					"max_depth": 8,
					"targets":   []any{map[string]any{"signature": "development.rust.cargo-target"}},
				},
			},
		}
	}

	data := map[string]any{
		"$schema": Schema,
		"version": 0,
		"recipes": map[string]any{
			"cargo-broad": map[string]any{
				"capability": "directory-prune",
				"budget": map[string]any{
					"max_bytes":      100,
					"eviction_order": EvictionOrderIdleOldestFirst,
					"authority":      AuthorityPropose,
				},
				"policy": makePolicy(root),
			},
			"cargo-sub": map[string]any{
				"capability": "directory-prune",
				"budget": map[string]any{
					"max_bytes":      100,
					"eviction_order": EvictionOrderIdleOldestFirst,
					"authority":      AuthorityPropose,
				},
				"policy": makePolicy(sub),
			},
		},
		"profiles": map[string]any{
			"developer": map[string]any{
				"label":   "Developer",
				"recipes": []string{"cargo-broad", "cargo-sub"},
				"volume_objective": map[string]any{
					"path":                  root,
					"floor_bytes":           floorBytes,
					"recovery_target_bytes": recoveryTarget,
					"max_sample_age":        "15m",
				},
			},
		},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 4, 15, 18, 0, 0, 0, time.UTC)

	// Case 1: Available is 32 GiB (>= 30 GiB floor, < 36 GiB recovery target),
	// and diagnostic level is "critical" (e.g. 96% used on a 1 TiB volume).
	// Verifies diagnostic level stays independent ("critical") while objective outcome is "met".
	samplerMet := &observe.FakeSampler{
		ByPath: map[string][]observe.Sample{
			root: {
				{
					VolumeID:     "apfs:test-vol",
					MountPath:    "/",
					CapturedAt:   now.Add(-2 * time.Minute),
					Basis:        observe.BasisStatfs,
					AvailBytes:   32 * gi,
					TotalBytes:   1000 * gi,
					UsedBytes:    968 * gi,
					UsedPercent:  96.8,
					Level:        observe.LevelCritical,
					Coverage:     observe.CoverageComplete,
					SizeComplete: true,
				},
			},
		},
	}
	pvMet, err := pack.PreviewWithOptions(context.Background(), "developer", PreviewOptions{
		Sampler: samplerMet,
		Now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	voMet := pvMet.VolumeObjective
	if voMet == nil {
		t.Fatal("missing volume_objective evaluation")
	}
	if voMet.Outcome != ObjectiveOutcomeMet || voMet.DiagnosticLevel != observe.LevelCritical {
		t.Fatalf("expected outcome=met with independent diagnostic_level=critical, got %+v", voMet)
	}
	if voMet.FloorShortfallBytes != 0 || voMet.RecoveryShortfallBytes != 4*gi {
		t.Fatalf("unexpected shortfalls: %+v", voMet)
	}
	if voMet.DroppedOverlapCount != 1 {
		t.Fatalf("expected 1 overlapping candidate deduplicated across recipes, got %+v", voMet)
	}
	if pvMet.Recipes[1].Candidates[0].DedupeSuperseded == "" {
		t.Fatalf("expected duplicate candidate in second recipe to record dedupe_superseded_by: %+v", pvMet.Recipes[1].Candidates[0])
	}
	singleCandBytes := pvMet.Recipes[0].Candidates[0].SizeBytes
	if voMet.DeduplicatedEligibleBytes != singleCandBytes || voMet.DeduplicatedSelectedBytes != singleCandBytes {
		t.Fatalf("expected deduplicated bytes %d (not double-counted %d), got %+v", singleCandBytes, singleCandBytes*2, voMet)
	}

	// Case 2: Available is below floor by less than singleCandBytes -> outcome="evaluable", approval_request_state="approval_required".
	samplerEval := &observe.FakeSampler{
		ByPath: map[string][]observe.Sample{
			root: {
				{
					VolumeID:     "apfs:test-vol",
					MountPath:    "/",
					CapturedAt:   now.Add(-1 * time.Minute),
					Basis:        observe.BasisStatfs,
					AvailBytes:   floorBytes - (singleCandBytes / 2),
					TotalBytes:   500 * gi,
					UsedBytes:    470 * gi,
					UsedPercent:  94.0,
					Level:        observe.LevelWarn,
					Coverage:     observe.CoverageComplete,
					SizeComplete: true,
				},
			},
		},
	}
	pvEval, err := pack.PreviewWithOptions(context.Background(), "developer", PreviewOptions{
		Sampler: samplerEval,
		Now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pvEval.VolumeObjective.Outcome != ObjectiveOutcomeEvaluable || pvEval.VolumeObjective.ApprovalRequestState != ApprovalStateApprovalRequired {
		t.Fatalf("expected outcome=evaluable and approval_required, got %+v", pvEval.VolumeObjective)
	}

	// Case 3: Available is 20 GiB (10 GiB shortfall > singleCandBytes) -> outcome="unmet_protected" with uncovered_shortfall_bytes > 0.
	samplerUnmet := &observe.FakeSampler{
		ByPath: map[string][]observe.Sample{
			root: {
				{
					VolumeID:     "apfs:test-vol",
					MountPath:    "/",
					CapturedAt:   now.Add(-1 * time.Minute),
					Basis:        observe.BasisStatfs,
					AvailBytes:   20 * gi,
					TotalBytes:   500 * gi,
					UsedBytes:    480 * gi,
					UsedPercent:  96.0,
					Level:        observe.LevelCritical,
					Coverage:     observe.CoverageComplete,
					SizeComplete: true,
				},
			},
		},
	}
	pvUnmet, err := pack.PreviewWithOptions(context.Background(), "developer", PreviewOptions{
		Sampler: samplerUnmet,
		Now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pvUnmet.VolumeObjective.Outcome != ObjectiveOutcomeUnmetProtected || pvUnmet.VolumeObjective.UncoveredShortfallBytes <= 0 {
		t.Fatalf("expected outcome=unmet_protected with uncovered_shortfall_bytes > 0, got %+v", pvUnmet.VolumeObjective)
	}

	// Case 4: Stale sample (20m old > 15m max_sample_age) -> outcome="unknown", approval_request_state="unknown_observation".
	samplerStale := &observe.FakeSampler{
		ByPath: map[string][]observe.Sample{
			root: {
				{
					VolumeID:     "apfs:test-vol",
					MountPath:    "/",
					CapturedAt:   now.Add(-20 * time.Minute),
					Basis:        observe.BasisStatfs,
					AvailBytes:   40 * gi,
					TotalBytes:   500 * gi,
					UsedBytes:    460 * gi,
					UsedPercent:  92.0,
					Level:        observe.LevelWarn,
					Coverage:     observe.CoverageComplete,
					SizeComplete: true,
				},
			},
		},
	}
	pvStale, err := pack.PreviewWithOptions(context.Background(), "developer", PreviewOptions{
		Sampler: samplerStale,
		Now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pvStale.VolumeObjective.Outcome != ObjectiveOutcomeUnknown || pvStale.VolumeObjective.ObservationFresh || pvStale.VolumeObjective.ApprovalRequestState != ApprovalStateUnknownObservation {
		t.Fatalf("expected stale sample to report outcome=unknown, got %+v", pvStale.VolumeObjective)
	}
}

func TestPackRejectsInvalidBudgetsAndVolumeObjectives(t *testing.T) {
	root := t.TempDir()
	base := func() map[string]any {
		var doc map[string]any
		if err := json.Unmarshal(fixture(t, root), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}

	cases := map[string]func(map[string]any){
		"zero max_bytes": func(doc map[string]any) {
			doc["recipes"].(map[string]any)["cargo"].(map[string]any)["budget"] = map[string]any{
				"max_bytes": 0,
			}
		},
		"unsupported authority": func(doc map[string]any) {
			doc["recipes"].(map[string]any)["cargo"].(map[string]any)["budget"] = map[string]any{
				"max_bytes": 1024,
				"authority": "auto",
			}
		},
		"unsupported eviction_order": func(doc map[string]any) {
			doc["recipes"].(map[string]any)["cargo"].(map[string]any)["budget"] = map[string]any{
				"max_bytes":      1024,
				"eviction_order": "largest-first",
			}
		},
		"recovery_target below floor": func(doc map[string]any) {
			doc["profiles"].(map[string]any)["developer"].(map[string]any)["volume_objective"] = map[string]any{
				"path":                  root,
				"floor_bytes":           3000,
				"recovery_target_bytes": 2000,
			}
		},
		"relative volume_objective path": func(doc map[string]any) {
			doc["profiles"].(map[string]any)["developer"].(map[string]any)["volume_objective"] = map[string]any{
				"path":                  "relative/vol",
				"floor_bytes":           1000,
				"recovery_target_bytes": 2000,
			}
		},
		"invalid max_sample_age": func(doc map[string]any) {
			doc["profiles"].(map[string]any)["developer"].(map[string]any)["volume_objective"] = map[string]any{
				"path":                  root,
				"floor_bytes":           1000,
				"recovery_target_bytes": 2000,
				"max_sample_age":        "not-a-duration",
			}
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := base()
			mutate(doc)
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Load(bytes.NewReader(raw)); err == nil {
				t.Fatalf("expected invalid pack %q to be rejected", name)
			}
		})
	}
}

func TestEvaluateRecipeBudgetWithholdsCandidatesWithUnknownActivity(t *testing.T) {
	cands := []Candidate{
		{
			Path:           "/tmp/cache/unknown-age",
			State:          engine.CandidateStatePrunable,
			SizeBytes:      5000,
			LastActivityAt: "",
		},
		{
			Path:           "/tmp/cache/known-old",
			State:          engine.CandidateStatePrunable,
			SizeBytes:      4000,
			LastActivityAt: "2026-04-10T00:00:00Z",
		},
	}
	eval := evaluateRecipeBudget(RecipeBudget{
		MaxBytes:      1000,
		EvictionOrder: EvictionOrderIdleOldestFirst,
		Authority:     AuthorityPropose,
	}, true, "complete", cands)
	if eval.EligibleBytes != 4000 || eval.ProtectedBytes != 5000 {
		t.Fatalf("expected unknown-activity candidate counted as protected (5000) and only known-activity candidate eligible (4000), got %+v", eval)
	}
	if cands[0].BudgetSelected || cands[0].BudgetRetained {
		t.Fatalf("unknown-activity candidate must never be selected or marked budget-retained: %+v", cands[0])
	}
	if !cands[1].BudgetSelected {
		t.Fatalf("known-activity candidate should be selected toward overage: %+v", cands[1])
	}
}

func TestDeduplicateRecipesWithoutVolumeObjectiveAndPrefersHigherPriorityCandidate(t *testing.T) {
	root := t.TempDir()
	// Create a candidate that is 48h old.
	// Recipe A requires min_age 72h (so candidate is withheld by age in Recipe A).
	// Recipe B requires min_age 24h and has a budget ceiling of 100 bytes (so candidate is prunable and BudgetSelected in Recipe B).
	// Even though Recipe A comes first alphabetically ("a-strict" before "b-relaxed") and the profile has NO volume_objective,
	// deduplication must run and Recipe B's prunable/BudgetSelected candidate must win over Recipe A's withheld candidate.
	targetPath := makeCargoProject(t, root, "shared-svc", 8192, 48*time.Hour)

	makePolicy := func(minAge string) map[string]any {
		return map[string]any{
			"version":  1,
			"defaults": map[string]any{"min_age": minAge},
			"paths": []any{
				map[string]any{
					"path":      root,
					"max_depth": 8,
					"targets":   []any{map[string]any{"signature": "development.rust.cargo-target"}},
				},
			},
		}
	}

	data := map[string]any{
		"$schema": Schema,
		"version": 0,
		"recipes": map[string]any{
			"a-strict": map[string]any{
				"capability": "directory-prune",
				"budget": map[string]any{
					"max_bytes":      100,
					"eviction_order": EvictionOrderIdleOldestFirst,
					"authority":      AuthorityPropose,
				},
				"policy": makePolicy("72h"),
			},
			"b-relaxed": map[string]any{
				"capability": "directory-prune",
				"budget": map[string]any{
					"max_bytes":      100,
					"eviction_order": EvictionOrderIdleOldestFirst,
					"authority":      AuthorityPropose,
				},
				"policy": makePolicy("24h"),
			},
		},
		"profiles": map[string]any{
			"developer": map[string]any{
				"label":   "Developer",
				"recipes": []string{"a-strict", "b-relaxed"},
			},
		},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	pack, err := Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	pv, err := pack.Preview(context.Background(), "developer")
	if err != nil {
		t.Fatal(err)
	}
	if pv.VolumeObjective != nil {
		t.Fatalf("expected nil volume_objective, got %+v", pv.VolumeObjective)
	}
	if len(pv.Recipes) != 2 {
		t.Fatalf("expected 2 recipes, got %d", len(pv.Recipes))
	}
	recA := pv.Recipes[0]
	recB := pv.Recipes[1]
	if recA.Candidates[0].Path != targetPath || recA.Candidates[0].DedupeSuperseded == "" {
		t.Fatalf("expected Recipe A's withheld candidate to be superseded by Recipe B's prunable candidate, got %+v", recA.Candidates[0])
	}
	if recB.Candidates[0].Path != targetPath || recB.Candidates[0].DedupeSuperseded != "" || !recB.Candidates[0].BudgetSelected {
		t.Fatalf("expected Recipe B's prunable candidate to win deduplication and remain selected, got %+v", recB.Candidates[0])
	}
	if recA.ProposedItemsCount != 0 || recB.ProposedItemsCount != 1 {
		t.Fatalf("expected ProposedItemsCount A=0, B=1, got A=%d B=%d", recA.ProposedItemsCount, recB.ProposedItemsCount)
	}
}
