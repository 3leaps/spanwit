package coverageattestation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/corpus"
	"github.com/3leaps/spanwit/internal/inventory"
)

// TestInventoryCoverageAcceptanceMatrix exercises the production event path:
// Run -> Collector -> Build -> canonical schema validation. The cases retain
// only the claims the contract admits: four confirmed count units per root.
func TestInventoryCoverageAcceptanceMatrix(t *testing.T) {
	type testCase struct {
		name          string
		setup         func(*testing.T, *matrixSink) (context.Context, inventory.Options, []string)
		state         string
		gapCode       string
		affectingGaps int
		boundary      bool
		entryEmission int64
	}

	cases := []testCase{
		{
			name: "complete",
			setup: func(t *testing.T, _ *matrixSink) (context.Context, inventory.Options, []string) {
				root := t.TempDir()
				writeAcceptanceFile(t, filepath.Join(root, "private.bin"), 12)
				return context.Background(), acceptanceOptions(root), []string{root, "private.bin"}
			},
			state: coverageComplete, entryEmission: 1,
		},
		{
			name: "permission-partial",
			setup: func(t *testing.T, _ *matrixSink) (context.Context, inventory.Options, []string) {
				root := t.TempDir()
				sealed := filepath.Join(root, "sealed-private")
				if err := os.Mkdir(sealed, 0o755); err != nil {
					t.Fatal(err)
				}
				writeAcceptanceFile(t, filepath.Join(sealed, "secret.bin"), 12)
				if err := os.Chmod(sealed, 0); err != nil {
					t.Skipf("permission fixture unavailable: %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })
				if file, err := os.Open(sealed); err == nil {
					_ = file.Close()
					t.Skip("test identity can read mode-000 directory")
				}
				return context.Background(), acceptanceOptions(root), []string{root, "sealed-private", "secret.bin"}
			},
			state: coveragePartial, gapCode: "permission", affectingGaps: 1,
		},
		{
			name: "vanished-partial",
			setup: func(t *testing.T, _ *matrixSink) (context.Context, inventory.Options, []string) {
				root := t.TempDir()
				vanishing := filepath.Join(root, "vanishing-private")
				if err := os.Mkdir(vanishing, 0o755); err != nil {
					t.Fatal(err)
				}
				opts := acceptanceOptions(root)
				opts.ProgressInterval = time.Nanosecond
				removed := false
				opts.Progress = func(snapshot inventory.Progress) {
					if !removed && snapshot.VisitedDirectories == 2 {
						removed = true
						_ = os.Remove(vanishing)
					}
				}
				return context.Background(), opts, []string{root, "vanishing-private"}
			},
			state: coveragePartial, gapCode: "vanished", affectingGaps: 1,
		},
		{
			name: "queue-limit-partial",
			setup: func(t *testing.T, _ *matrixSink) (context.Context, inventory.Options, []string) {
				root := t.TempDir()
				for index := range 3 {
					if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("subtree-%d", index)), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				opts := acceptanceOptions(root)
				opts.MaxPendingDirs = 1
				return context.Background(), opts, []string{root, "subtree-"}
			},
			state: coveragePartial, gapCode: "directory-queue-limit", affectingGaps: 2,
		},
		{
			name: "canceled-partial",
			setup: func(t *testing.T, sink *matrixSink) (context.Context, inventory.Options, []string) {
				root := t.TempDir()
				writeAcceptanceFile(t, filepath.Join(root, "cancel-private.bin"), 12)
				ctx, cancel := context.WithCancel(context.Background())
				sink.entryFn = func(inventory.Entry) error {
					cancel()
					return nil
				}
				t.Cleanup(cancel)
				return ctx, acceptanceOptions(root), []string{root, "cancel-private.bin"}
			},
			state: coveragePartial, gapCode: "canceled", affectingGaps: 1, entryEmission: 1,
		},
		{
			name: "top-k-complete",
			setup: func(t *testing.T, _ *matrixSink) (context.Context, inventory.Options, []string) {
				root := t.TempDir()
				writeAcceptanceFile(t, filepath.Join(root, "small-private.bin"), 12)
				writeAcceptanceFile(t, filepath.Join(root, "large-private.bin"), 24)
				opts := acceptanceOptions(root)
				opts.Top = 1
				return context.Background(), opts, []string{root, "private.bin"}
			},
			state: coverageComplete, entryEmission: 1,
		},
		{
			name: "summary-only-complete",
			setup: func(t *testing.T, _ *matrixSink) (context.Context, inventory.Options, []string) {
				root := t.TempDir()
				writeAcceptanceFile(t, filepath.Join(root, "summary-private.bin"), 12)
				opts := acceptanceOptions(root)
				opts.EmissionMode = inventory.EmissionSummaryOnly
				return context.Background(), opts, []string{root, "summary-private.bin"}
			},
			state: coverageComplete,
		},
		{
			name: "excluded-subtree-complete",
			setup: func(t *testing.T, _ *matrixSink) (context.Context, inventory.Options, []string) {
				root := t.TempDir()
				writeAcceptanceFile(t, filepath.Join(root, "keep-private.bin"), 12)
				writeAcceptanceFile(t, filepath.Join(root, "excluded-private", "secret.bin"), 24)
				opts := acceptanceOptions(root)
				opts.Exclusions = []string{"excluded-private"}
				return context.Background(), opts, []string{root, "excluded-private", "secret.bin"}
			},
			state: coverageComplete, entryEmission: 1,
		},
		{
			name: "multi-root-complete",
			setup: func(t *testing.T, _ *matrixSink) (context.Context, inventory.Options, []string) {
				first, second := t.TempDir(), t.TempDir()
				writeAcceptanceFile(t, filepath.Join(first, "first-private.bin"), 12)
				writeAcceptanceFile(t, filepath.Join(second, "second-private.bin"), 24)
				opts := acceptanceOptions(first, second)
				return context.Background(), opts, []string{first, second, "private.bin"}
			},
			state: coverageComplete, entryEmission: 2,
		},
		{
			name: "one-filesystem-boundary-complete",
			setup: func(t *testing.T, _ *matrixSink) (context.Context, inventory.Options, []string) {
				root, boundary, exclusions := acceptanceMountFixture(t)
				opts := acceptanceOptions(root)
				opts.OneFilesystem = true
				opts.Exclusions = exclusions
				return context.Background(), opts, []string{root, boundary}
			},
			state: coverageComplete, boundary: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			downstream := &matrixSink{}
			ctx, opts, protected := tc.setup(t, downstream)
			collector, err := NewCollector(downstream, DefaultMaxGaps)
			if err != nil {
				t.Fatal(err)
			}
			summary, err := inventory.Run(ctx, opts, collector)
			if err != nil {
				t.Fatalf("inventory run: %v", err)
			}
			evidence, err := collector.Evidence()
			if err != nil {
				t.Fatalf("collect evidence: %v", err)
			}
			document, err := Build(evidence, deterministicBuildOptions())
			if err != nil {
				t.Fatalf("build attestation: %v", err)
			}
			payload, err := MarshalValidated(document)
			if err != nil {
				t.Fatalf("canonical validation: %v", err)
			}
			decoded, err := DecodeValidated(payload)
			if err != nil {
				t.Fatalf("canonical decode: %v", err)
			}

			if summary.Lifecycle != map[string]string{coverageComplete: inventory.LifecycleComplete, coveragePartial: inventory.LifecyclePartial}[tc.state] || decoded.CoverageState != tc.state {
				t.Fatalf("summary lifecycle=%q attestation state=%q", summary.Lifecycle, decoded.CoverageState)
			}
			if got := downstream.entryCount; got != tc.entryEmission {
				t.Fatalf("entry records=%d want=%d", got, tc.entryEmission)
			}
			if tc.gapCode != "" && !documentHasGapCode(decoded, tc.gapCode) {
				t.Fatalf("attestation gaps=%+v missing code %q", decoded.Gaps, tc.gapCode)
			}
			if got := len(evidence.Gaps); got != tc.affectingGaps || len(decoded.Gaps) != tc.affectingGaps {
				t.Fatalf("affecting gaps evidence=%d document=%d want=%d", got, len(decoded.Gaps), tc.affectingGaps)
			}
			if tc.boundary {
				if summary.BoundarySkipCount == 0 || len(decoded.Gaps) != 0 {
					t.Fatalf("boundary accounting=%d attestation gaps=%+v", summary.BoundarySkipCount, decoded.Gaps)
				}
			}
			assertCountClaims(t, evidence, decoded)
			if decoded.AsOf != summary.TerminalObservedAt.UTC().Format(time.RFC3339Nano) {
				t.Fatalf("as_of=%q terminal=%s", decoded.AsOf, summary.TerminalObservedAt)
			}
			for _, value := range protected {
				if value != "" && strings.Contains(string(payload), value) {
					t.Fatalf("attestation disclosed protected value %q:\n%s", value, payload)
				}
			}
		})
	}
}

func TestInventoryCoverageThreeRootCancellationAttestation(t *testing.T) {
	first, second, third := t.TempDir(), t.TempDir(), t.TempDir()
	writeAcceptanceFile(t, filepath.Join(first, "first-private.bin"), 12)
	writeAcceptanceFile(t, filepath.Join(second, "second-private.bin"), 24)
	writeAcceptanceFile(t, filepath.Join(third, "third-private.bin"), 36)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	downstream := &matrixSink{}
	downstream.entryFn = func(entry inventory.Entry) error {
		if entry.RootID == "root-2" {
			cancel()
		}
		return nil
	}
	collector, err := NewCollector(downstream, DefaultMaxGaps)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := inventory.Run(ctx, acceptanceOptions(first, second, third), collector)
	if err != nil {
		t.Fatalf("inventory run: %v", err)
	}
	if summary.Lifecycle != inventory.LifecyclePartial || !summary.Canceled {
		t.Fatalf("summary=%+v", summary)
	}
	evidence, err := collector.Evidence()
	if err != nil {
		t.Fatal(err)
	}
	document, err := Build(evidence, deterministicBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := MarshalValidated(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeValidated(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.CoverageState != coveragePartial ||
		decoded.AsOf != summary.TerminalObservedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("attestation=%+v terminal=%s", decoded, summary.TerminalObservedAt)
	}
	if len(decoded.Claims) != 12 {
		t.Fatalf("claims=%d want 12", len(decoded.Claims))
	}
	assertCountClaims(t, evidence, decoded)
	claimsByRoot := make(map[string]int, 3)
	for _, claim := range decoded.Claims {
		claimsByRoot[claim.Scope.Partition["root_id"]]++
	}
	if want := map[string]int{"root-1": 4, "root-2": 4, "root-3": 4}; !mapsEqual(claimsByRoot, want) {
		t.Fatalf("claims by root=%v want=%v", claimsByRoot, want)
	}
	if len(decoded.Gaps) != 2 {
		t.Fatalf("gaps=%+v want exactly two canceled gaps", decoded.Gaps)
	}
	gapsByRoot := make(map[string]int, 2)
	for _, gap := range decoded.Gaps {
		if gap.Code != "canceled" {
			t.Fatalf("non-canceled gap=%+v", gap)
		}
		gapsByRoot[gap.Scope.Partition["root_id"]]++
	}
	if want := map[string]int{"root-2": 1, "root-3": 1}; !mapsEqual(gapsByRoot, want) {
		t.Fatalf("gaps by root=%v want=%v", gapsByRoot, want)
	}
	for _, protected := range []string{first, second, third, "first-private.bin", "second-private.bin", "third-private.bin"} {
		if strings.Contains(string(payload), protected) {
			t.Fatalf("attestation disclosed protected value %q:\n%s", protected, payload)
		}
	}
}

type matrixSink struct {
	entryCount int64
	entryFn    func(inventory.Entry) error
}

func (*matrixSink) Header(inventory.Header) error { return nil }
func (s *matrixSink) Entry(entry inventory.Entry) error {
	s.entryCount++
	if s.entryFn != nil {
		return s.entryFn(entry)
	}
	return nil
}
func (*matrixSink) Gap(inventory.Gap) error         { return nil }
func (*matrixSink) Summary(inventory.Summary) error { return nil }

func acceptanceOptions(roots ...string) inventory.Options {
	return inventory.Options{Roots: roots, Backend: inventory.BackendSerial, Workers: 1}
}

func writeAcceptanceFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func documentHasGapCode(document Document, code string) bool {
	for _, gap := range document.Gaps {
		if gap.Code == code {
			return true
		}
	}
	return false
}

func mapsEqual(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func assertCountClaims(t *testing.T, evidence Evidence, document Document) {
	t.Helper()
	want := make(map[string]int64, len(evidence.Summary.Roots)*4)
	for _, root := range evidence.Summary.Roots {
		want[root.RootID+"/entries_visited"] = root.VisitedEntries
		want[root.RootID+"/directories_visited"] = root.VisitedDirectories
		want[root.RootID+"/entries_matched"] = root.MatchedCount
		want[root.RootID+"/entries_emitted"] = root.EmittedCount
	}
	if len(document.Claims) != len(want) {
		t.Fatalf("claims=%d want=%d", len(document.Claims), len(want))
	}
	for _, claim := range document.Claims {
		if claim.Basis != "confirmed" || claim.Method != "enumerated" || claim.Volume == nil {
			t.Fatalf("claim=%+v", claim)
		}
		rootID := claim.Scope.Partition["root_id"]
		key := rootID + "/" + claim.Volume.Unit
		observed, ok := want[key]
		if !ok || claim.Volume.Observed != observed {
			t.Fatalf("claim %q observed=%d want=%d", key, claim.Volume.Observed, observed)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing claims=%v", want)
	}
}

// acceptanceMountFixture isolates the walk to one real boundary so the
// expected complete lifecycle does not depend on unrelated host siblings.
func acceptanceMountFixture(t *testing.T) (root, boundary string, exclusions []string) {
	t.Helper()
	for _, candidate := range []string{"/dev", "/Volumes", "/run", "/mnt", "/media"} {
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			continue
		}
		boundaries, _, err := corpus.MountBoundariesUnder(candidate, 2)
		if err != nil || len(boundaries) == 0 {
			continue
		}
		parent := filepath.Dir(boundaries[0].Path)
		rules, err := corpus.IsolatingExclusions(parent, boundaries[0].Path)
		if err != nil {
			continue
		}
		return parent, boundaries[0].Path, rules
	}
	t.Skip("no observable nested filesystem boundary for one-filesystem acceptance case")
	return "", "", nil
}
