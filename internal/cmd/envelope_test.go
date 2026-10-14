package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"

	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/envelope"
	"github.com/3leaps/spanwit/internal/observe"
)

// Every whole-document report spanwit writes carries the envelope: $schema,
// version, generated_at, tool, a disclosure summary that covers its own
// content, and completion where the document describes a capture.
func TestEveryEmittedReportCarriesTheEnvelope(t *testing.T) {
	id := &appidentity.Identity{BinaryName: "spanwit"}
	analysis := t.TempDir()

	emit := map[string]func(t *testing.T) []byte{
		"space full": func(t *testing.T) []byte {
			var out, errOut bytes.Buffer
			code := runSpace(id, &out, &errOut, spaceRunOpts{
				path: analysis, format: "json", minSize: "1K", top: 5,
			})
			if code > 1 {
				t.Fatalf("space code=%d stderr=%s", code, errOut.String())
			}
			return out.Bytes()
		},
		"space capacity-only": func(t *testing.T) []byte {
			var out, errOut bytes.Buffer
			code := runSpace(id, &out, &errOut, spaceRunOpts{
				path: analysis, format: "json", capacityOnly: true, capacityCollector: linuxCapacityCollector(),
			})
			if code != 0 {
				t.Fatalf("capacity code=%d stderr=%s", code, errOut.String())
			}
			return out.Bytes()
		},
		"space compare": func(t *testing.T) []byte {
			left := captureWithRuntime(t, nil)
			right := captureWithRuntime(t, nil)
			var out, errOut bytes.Buffer
			code := runSpace(id, &out, &errOut, spaceRunOpts{
				format: "json", comparePaths: []string{left, right},
				compareNow: func() time.Time { return time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC) },
			})
			if code != 0 {
				t.Fatalf("compare code=%d stderr=%s", code, errOut.String())
			}
			return out.Bytes()
		},
		"prune plan": func(t *testing.T) []byte {
			var out bytes.Buffer
			plan := PrunePlan{Candidates: []PruneCandidate{{
				Path: filepath.Join(analysis, "target"), Size: 1, State: candidateStatePrunable,
				Signature: "development.rust.cargo-target", Confidence: "high",
			}}}
			if err := writePrunePlanJSON(&out, plan, "allowlist:"+analysis, false, time.Now()); err != nil {
				t.Fatal(err)
			}
			return out.Bytes()
		},
		"observe health": func(t *testing.T) []byte {
			var out bytes.Buffer
			h := observe.Health{
				Schema: observe.HealthSchemaID, Version: 1, GeneratedAt: time.Now().UTC(),
				Volumes:          []observe.VolumeHealth{{VolumeID: "fs:1", Label: "Data", Level: "ok", Coverage: "complete"}},
				MutationContract: "open",
			}
			if err := writeHealthJSON(&out, h); err != nil {
				t.Fatal(err)
			}
			return out.Bytes()
		},
		"observe state": func(t *testing.T) []byte {
			store := &observe.FileStore{Path: filepath.Join(t.TempDir(), "state.json")}
			doc := &observe.StateDocument{Volumes: map[string]*observe.VolumeRuntime{
				"fs:1": {Volume: observe.RegisteredVolume{ID: "fs:1", RegisterPath: "/data"},
					Level: "ok", Coverage: "incomparable", NextSeq: 1},
			}}
			if err := store.Save(doc); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(store.Path)
			if err != nil {
				t.Fatal(err)
			}
			return raw
		},
	}
	wantCompletion := map[string]bool{"space full": true, "space capacity-only": true, "space compare": true}
	// Content-derived levels for these fixtures (see the disclosure table).
	wantPaths := map[string]string{
		"space compare":  envelope.PathsNone,
		"observe health": envelope.PathsNone,
		"prune plan":     envelope.PathsFull,
		"observe state":  envelope.PathsFull,
	}

	for name, run := range emit {
		t.Run(name, func(t *testing.T) {
			raw := run(t)
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("decode: %v\n%s", err, raw)
			}
			for _, k := range []string{"$schema", "version", "generated_at", "tool", "disclosure"} {
				if _, ok := doc[k]; !ok {
					t.Errorf("missing envelope field %q", k)
				}
			}
			if tool, ok := doc["tool"].(map[string]any); ok {
				if tool["name"] != "spanwit" || tool["version"] == "" || tool["version"] == nil {
					t.Errorf("tool must name spanwit and a version, got %v", tool)
				}
				for k := range tool {
					if k != "name" && k != "version" && k != "commit" {
						t.Errorf("tool carries %q: only name, version, commit are allowed", k)
					}
				}
			}
			if _, ok := doc["completion"]; ok != wantCompletion[name] {
				t.Errorf("completion present=%v, want %v", ok, wantCompletion[name])
			}
			if want, ok := wantPaths[name]; ok {
				d, _ := doc["disclosure"].(map[string]any)
				if d["paths"] != want {
					t.Errorf("disclosure.paths = %v, want %s", d["paths"], want)
				}
			}
			roots := []string{analysis}
			if r, ok := doc["root"].(string); ok {
				roots = append(roots, r)
			}
			if _, err := envelope.Check(raw, roots, envelope.Disclosure{}); err != nil {
				t.Errorf("the emitted summary must cover its own content: %v", err)
			}
		})
	}
}

// A capacity-only capture emits completion: complete with no coverage gaps,
// partial (with the gap count) otherwise.
func TestCapacityOnlyCompletionFollowsCoverageGaps(t *testing.T) {
	id := &appidentity.Identity{BinaryName: "spanwit"}
	run := func(t *testing.T, collector *capacity.Collector) map[string]any {
		t.Helper()
		var out, errOut bytes.Buffer
		opts := spaceRunOpts{path: t.TempDir(), format: "json", capacityOnly: true, capacityCollector: collector}
		if code := runSpace(id, &out, &errOut, opts); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errOut.String())
		}
		var doc map[string]any
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		return doc["completion"].(map[string]any)
	}
	if c := run(t, linuxCapacityCollector()); c["lifecycle"] != envelope.CompletionComplete || c["warning_count"] != float64(0) {
		t.Fatalf("no gaps must be complete/0, got %v", c)
	}
	if c := run(t, darwinGapCollector()); c["lifecycle"] != envelope.CompletionPartial || c["warning_count"].(float64) < 1 {
		t.Fatalf("a coverage gap must be partial with a count, got %v", c)
	}
}

// A compare is partial when either input capture was partial.
func TestCompareCompletionInheritsPartialInputs(t *testing.T) {
	partialCapture := func(t *testing.T) string {
		var out, errOut bytes.Buffer
		code := runSpace(&appidentity.Identity{BinaryName: "spanwit"}, &out, &errOut, spaceRunOpts{
			path: t.TempDir(), format: "json", capacityOnly: true, capacityCollector: darwinGapCollector(),
		})
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errOut.String())
		}
		p := filepath.Join(t.TempDir(), "partial.json")
		if err := os.WriteFile(p, out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	r := compareCaptures(t, captureWithRuntime(t, nil), captureWithRuntime(t, nil))
	if r.Completion == nil || r.Completion.Lifecycle != envelope.CompletionComplete {
		t.Fatalf("two complete inputs compare complete, got %+v", r.Completion)
	}
	r = compareCaptures(t, partialCapture(t), captureWithRuntime(t, nil))
	if r.Completion == nil || r.Completion.Lifecycle != envelope.CompletionPartial || r.Completion.WarningCount != 1 {
		t.Fatalf("a partial input makes the compare partial, got %+v", r.Completion)
	}
}

// darwinGapCollector reports a permission_denied coverage gap on the
// system-managed plane.
func darwinGapCollector() *capacity.Collector {
	c := linuxCapacityCollector()
	c.Platform = "darwin"
	c.FilesystemSampler = func(_ context.Context, path string) (capacity.FilesystemSample, error) {
		return capacity.FilesystemSample{Path: path, Mount: "/System/Volumes/Data", VolumeID: "fs:1", FSType: "apfs",
			Total: 1000, Used: 600, Available: 400}, nil
	}
	c.DarwinRunner = capacity.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return nil, &capacity.RunError{Code: "command_failed"}
	})
	c.ManagedRoots = []capacity.ManagedRoot{{ID: "apple_assets_v2", Path: "/System/Library/AssetsV2",
		Remediation: capacity.RemediationOSOrUpdateMechanism}}
	c.ManagedRootMeasurer = func(context.Context, string, int) capacity.ManagedMeasurement {
		return capacity.ManagedMeasurement{Present: true, Gaps: []capacity.CoverageGap{{
			Code: capacity.GapPermissionDenied, Scope: "system_managed"}}}
	}
	return c
}

// devrev 6dtspr9x oracle: a realistic full report — a verified Cargo
// target, its structural recipe with an exact plan, notes and evidence —
// built from the analysis root alone (home caches off) derives exactly the
// level its content allows. A writer string the allow-list misses would
// widen it and fail here instead of silently.
func TestRealisticFullReportDerivesItsTableLevel(t *testing.T) {
	analysis := t.TempDir()
	e2eCargo(t, filepath.Join(analysis, "app"))
	e2eAge(t, analysis)
	var out, errOut bytes.Buffer
	code := runSpace(&appidentity.Identity{BinaryName: "spanwit"}, &out, &errOut, spaceRunOpts{
		path: analysis, format: "json", minSize: "1K", top: 5,
		includeHomeCaches: false, includeRecipes: true,
	})
	if code > 1 {
		t.Fatalf("space code=%d stderr=%s", code, errOut.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["prune_handoff"]; !ok {
		t.Fatalf("fixture must produce a prune handoff:\n%s", out.String())
	}
	// Only the pressure anchor (home) and mount sit outside the analysis
	// root, unless pressure adds temp-plane roots (full by the table).
	want := envelope.PathsSystem
	if _, ok := doc["temp_plane_coverage"]; ok {
		want = envelope.PathsFull
	}
	got := doc["disclosure"].(map[string]any)["paths"]
	if got != want {
		// Name the words that widened it.
		d := envelope.Derive(withoutTempPlanes(doc), envelope.Context{Roots: []string{doc["root"].(string)}, Home: envelope.Home()})
		t.Fatalf("realistic full report derived paths=%v, want %s (without temp planes: %s)\n%s", got, want, d.Paths, out.String())
	}
	if d := envelope.Derive(withoutTempPlanes(doc), envelope.Context{Roots: []string{doc["root"].(string)}, Home: envelope.Home()}); d.Paths != envelope.PathsSystem {
		t.Fatalf("with temp planes removed the report must be system, got %s:\n%s", d.Paths, out.String())
	}
}

func withoutTempPlanes(doc map[string]any) map[string]any {
	c := map[string]any{}
	for k, v := range doc {
		if k != "temp_plane_coverage" {
			c[k] = v
		}
	}
	return c
}
