package space

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/catalog"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/engine"
	"github.com/3leaps/spanwit/internal/inventory"
)

func TestObservationSizingPreservesDepthBoundsAndEmptyZero(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "shallow"), 7)
	writeFile(t, filepath.Join(root, "nested", "f"), 31)
	writeFile(t, filepath.Join(root, "nested", "deep", "f"), 101)
	empty := t.TempDir()
	for _, workers := range []int{1, 4} {
		for _, depth := range []int{0, 1, 2, -1} {
			coverage := newObservationCoverage()
			rows := newObservationWalker(Options{Workers: workers}).size(context.Background(),
				[]Entry{{Path: root}, {Path: empty}}, depth, 0, coverage)
			if len(rows) != 2 {
				t.Fatalf("workers=%d depth=%d: rows=%+v", workers, depth, rows)
			}
			for _, row := range rows {
				legacy, err := engine.DirSizeContext(context.Background(), row.Path, depth)
				if err != nil || row.SizeBytes != legacy.Bytes || row.SizeIncomplete != legacy.Incomplete {
					t.Fatalf("depth semantics changed: depth=%d row=%+v legacy=%+v err=%v", depth, row, legacy, err)
				}
				if row.Path == empty && (row.SizeBytes != 0 || row.SizeIncomplete) {
					t.Fatalf("measured empty is not exact zero: %+v", row)
				}
			}
		}
	}
}

func TestObservationAdmissionGapsDoNotFabricateZerosOrLoseSiblings(t *testing.T) {
	root := t.TempDir()
	good, missing := filepath.Join(root, "good"), filepath.Join(root, "missing")
	writeFile(t, filepath.Join(good, "f"), 17)
	coverage := newObservationCoverage()
	rows := newObservationWalker(Options{}).size(context.Background(), []Entry{{Path: missing}, {Path: good}}, -1, 0, coverage)
	if len(rows) != 1 || rows[0].Path != good || rows[0].SizeBytes != 17 || rows[0].SizeIncomplete {
		t.Fatalf("bad accounting: %+v", rows)
	}
	warnings := coverage.warnings("test")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "vanished=1; wholly_unmeasured=1") || strings.Contains(warnings[0], root) {
		t.Fatalf("missing path-free coverage: %v", warnings)
	}
}

// The same real walker feeds the adapter; the wrapper injects terminal coverage
// to isolate its projection from filesystem permissions and network services.
// Actual no-open remote and real stalled-open behavior are exercised by inventory.
type observationFaultSink struct {
	inventory.Sink
	kind string
}

func (s observationFaultSink) Summary(summary inventory.Summary) error {
	for i := range summary.Roots {
		r := &summary.Roots[i]
		if err := s.Gap(inventory.Gap{RootID: r.RootID, Kind: s.kind, AffectsCompleteness: s.kind != "remote"}); err != nil {
			return err
		}
		observed := false
		r.Observed = &observed
		if s.kind == "remote" {
			r.RemoteSkipCount++ // inventory calls this complete; space must not.
		} else {
			r.Lifecycle = inventory.LifecyclePartial
			r.GapCount++
		}
	}
	return s.Sink.Summary(summary)
}

func TestAnalyzeObservationFaultsCannotChangeHandoff(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	cat, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(home, filepath.FromSlash(catalogRel(t, cat, "development.go.go-build")))
	writeFile(t, filepath.Join(cache, "f"), 101)
	writeFile(t, filepath.Join(root, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(root, "app", "target", "debug", "f"), 4096)
	writeFile(t, filepath.Join(root, "other", "target", "f"), 71)
	writeFile(t, filepath.Join(root, "misc", "f"), 31)
	opts := Options{Path: root, IncludeHomeCaches: true, DisableRecipes: true,
		MaxDepth: 6, Top: 1, MinSize: "1", ForcePressureLevel: PressureOK,
		Now: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)}
	baseline, err := Analyze(context.Background(), opts)
	if err != nil || baseline.PruneHandoff == nil || baseline.PruneHandoff.PrunableCount != 1 ||
		len(baseline.Hotspots) == 0 || baseline.Unverified.Count != 1 || len(baseline.Unknown) == 0 {
		t.Fatalf("vacuous baseline: %+v err=%v", baseline, err)
	}
	baselineHandoff, err := json.Marshal(baseline.PruneHandoff)
	if err != nil {
		t.Fatal(err)
	}
	orig := observationInventoryRun
	t.Cleanup(func() { observationInventoryRun = orig })
	for _, kind := range []string{"stalled", "remote", "permission", "canceled"} {
		for _, filtered := range []bool{false, true} {
			calls := 0
			observationInventoryRun = func(ctx context.Context, o inventory.Options, sink inventory.Sink) (inventory.Summary, error) {
				calls++
				return orig(ctx, o, observationFaultSink{Sink: sink, kind: kind})
			}
			if filtered {
				opts.ClassFilters = []string{StatePrunable}
			} else {
				opts.ClassFilters = nil
			}
			report, err := Analyze(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(report.PruneHandoff)
			if err != nil {
				t.Fatal(err)
			}
			// Filter provenance legitimately differs; compare against an unfaulted
			// report with identical filters, never by removing handoff fields.
			want := baselineHandoff
			if filtered {
				observationInventoryRun = orig
				control, err := Analyze(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				want, err = json.Marshal(control.PruneHandoff)
				if err != nil {
					t.Fatal(err)
				}
			}
			if calls == 0 || !bytes.Equal(got, want) {
				t.Fatalf("%s filtered=%v: handoff changed\n%s\n%s", kind, filtered, got, want)
			}
			if len(report.Hotspots)+len(report.Unverified.Entries)+len(report.Unknown) != 0 || report.Unverified.TotalBytes != 0 {
				t.Fatalf("%s: unmeasured became numeric: %+v", kind, report)
			}
			if report.Completion.Lifecycle != CompletionPartial || len(report.Warnings) != 3 {
				t.Fatalf("%s filtered=%v: coverage dropped: %+v %v", kind, filtered, report.Completion, report.Warnings)
			}
			for _, warning := range report.Warnings {
				if !strings.Contains(warning, kind+"=") || !strings.Contains(warning, "wholly_unmeasured=") ||
					strings.Contains(warning, root) || strings.Contains(warning, home) {
					t.Fatalf("invalid coverage summary: %q", warning)
				}
			}
			raw, _ := json.Marshal(report)
			if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
				t.Fatalf("schema: %v", err)
			}
		}
	}
}

func TestAnalyzeObservationWorkersProduceSameRows(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, "a", "target", "one"), 10)
	writeFile(t, filepath.Join(root, "b", "build", "one"), 11)
	writeFile(t, filepath.Join(root, "misc", "nested", "one"), 12)
	opts := Options{Path: root, Workers: 1, MaxDepth: 2, DisableRecipes: true,
		ForcePressureLevel: PressureOK, Now: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)}
	one, err := Analyze(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Workers = 4
	four, err := Analyze(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one.Hotspots, four.Hotspots) || !reflect.DeepEqual(one.Unverified, four.Unverified) ||
		!reflect.DeepEqual(one.Unknown, four.Unknown) || !reflect.DeepEqual(one.Warnings, four.Warnings) ||
		!reflect.DeepEqual(one.PruneHandoff, four.PruneHandoff) {
		t.Fatalf("worker-dependent report:\n%+v\n%+v", one, four)
	}
}

func TestAbandonPrimitiveHasNoExportedCallable(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "inventory", "stall.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.IsExported() {
			t.Fatalf("stall primitive exported: %s", fn.Name.Name)
		}
	}
	// Guard against making the existing opener itself an exported function var.
	for _, d := range f.Decls {
		decl, ok := d.(*ast.GenDecl)
		if !ok || decl.Tok != token.VAR {
			continue
		}
		for _, spec := range decl.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				if name.IsExported() {
					t.Fatalf("stall variable exported: %s", name.Name)
				}
			}
		}
	}
}

func TestObservationCanceledBeforeReadIsNotMeasuredEmpty(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cov := newObservationCoverage()
	rows := newObservationWalker(Options{}).size(ctx, []Entry{{Path: root}}, -1, 0, cov)
	if len(rows) != 0 || cov.unmeasured != 1 || cov.gaps["canceled"] != 1 {
		t.Fatalf("canceled root became empty: rows=%v coverage=%+v", rows, cov)
	}
}

func TestObservationWalkerOptionsReachInventory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "file"), 17)
	orig := observationInventoryRun
	t.Cleanup(func() { observationInventoryRun = orig })
	var calls, alerts int
	observationInventoryRun = func(ctx context.Context, opts inventory.Options, sink inventory.Sink) (inventory.Summary, error) {
		calls++
		if opts.Workers != 4 || opts.Backend != inventory.BackendParallel || !opts.IncludeRemote ||
			opts.StallTimeout != -1 || opts.MutationContract != inventory.MutationContractReadOnly ||
			opts.EmissionMode != inventory.EmissionSummaryOnly || opts.Traversal == nil ||
			!opts.Traversal.DiscoveredRoots || opts.Traversal.MaxDepth != 2 || opts.OnStall == nil {
			t.Fatalf("walker options lost: %+v", opts)
		}
		opts.OnStall(inventory.StallAlert{Pending: 1})
		return orig(ctx, opts, sink)
	}
	walker := newObservationWalker(Options{Workers: 4, Backend: inventory.BackendParallel,
		IncludeRemote: true, StallTimeout: -1, MutationContract: inventory.MutationContractReadOnly,
		OnStall: func(inventory.StallAlert) { alerts++ }})
	rows := walker.size(context.Background(), []Entry{{Path: root}}, 2, 0, newObservationCoverage())
	if calls != 1 || alerts != 1 || len(rows) != 1 || rows[0].SizeBytes != 17 {
		t.Fatalf("vacuous wiring: calls=%d alerts=%d rows=%+v", calls, alerts, rows)
	}
}
