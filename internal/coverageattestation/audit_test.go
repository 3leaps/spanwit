package coverageattestation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/inventory"
)

func writeAuditTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{"keep/a.bin", "keep/deep/b.bin", "small/c.bin"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 4096), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func auditRunOptions(root string) inventory.Options {
	return inventory.Options{
		Roots: []string{root}, Backend: inventory.BackendSerial, Workers: 1,
		EmissionMode: inventory.EmissionDirectoryAudit, DirectoryDepth: -1,
		MaxAggregateDirectories: 1000, SizeBasis: inventory.SizeApparent, RunID: "audit-attest",
	}
}

func runAuditCollected(t *testing.T, ctx context.Context, opts inventory.Options) (*AuditCollector, *bytes.Buffer, error) {
	t.Helper()
	var stream bytes.Buffer
	collector, err := NewAuditCollector(inventory.NewJSONLSink(&stream), DefaultMaxGaps)
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := inventory.Run(ctx, opts, collector)
	return collector, &stream, runErr
}

func buildAuditForTest(t *testing.T, collector *AuditCollector) (Document, error) {
	t.Helper()
	evidence, err := collector.Evidence()
	if err != nil {
		return Document{}, err
	}
	return BuildAudit(evidence, BuildOptions{EmitterName: "spanwit", EmitterVersion: "test"})
}

func claimVolume(t *testing.T, document Document, key, value, unit string) int64 {
	t.Helper()
	for _, claim := range document.Claims {
		if claim.Scope.Partition[key] == value && claim.Volume != nil && claim.Volume.Unit == unit {
			return claim.Volume.Observed
		}
	}
	t.Fatalf("no %s claim for %s=%s in %+v", unit, key, value, document.Claims)
	return 0
}

// The attestation covers the full traversal subject: a floor and top that
// select one row do not reduce traversal coverage or appear as gaps.
func TestBuildAuditAttestsFullSubjectNotSelection(t *testing.T) {
	root := writeAuditTree(t)
	opts := auditRunOptions(root)
	floor := int64(8192)
	opts.DirectoryFloor, opts.DirectoryTop = &floor, 1
	collector, stream, err := runAuditCollected(t, context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := inventory.ValidateAuditStream(bytes.NewReader(stream.Bytes())); err != nil {
		t.Fatal(err)
	}
	document, err := buildAuditForTest(t, collector)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarshalValidated(document); err != nil {
		t.Fatal(err)
	}
	if document.CoverageState != coverageComplete || len(document.Gaps) != 0 {
		t.Fatalf("selection changed coverage: state=%s gaps=%+v", document.CoverageState, document.Gaps)
	}
	if document.Emitter.Relation != "producer" || document.Emitter.RunID != "audit-attest" ||
		document.Subject.SubjectURI != "urn:spanwit:filesystem-inventory:audit-attest" {
		t.Fatalf("emitter/subject=%+v %+v", document.Emitter, document.Subject)
	}
	evidence, _ := collector.Evidence()
	if document.AsOf != evidence.summary.TerminalObservedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00") {
		t.Fatalf("as_of=%s summary=%s", document.AsOf, evidence.summary.TerminalObservedAt)
	}
	if got := claimVolume(t, document, "root_id", "root-1", "directories_visited"); got != evidence.summary.VisitedDirectories {
		t.Fatalf("directories_visited=%d summary=%d", got, evidence.summary.VisitedDirectories)
	}
	if got := claimVolume(t, document, "run_id", "audit-attest", unitDirectoriesEmitted); got != 1 {
		t.Fatalf("directories_emitted=%d want 1", got)
	}
	if got := claimVolume(t, document, "run_id", "audit-attest", unitDirectoriesSelectedBeforeTop); got < 2 {
		t.Fatalf("directories_selected_before_top=%d want >= 2 (top truncated)", got)
	}
	for _, claim := range document.Claims {
		switch claim.Volume.Unit {
		case "entries_matched", "entries_emitted":
			t.Fatalf("file-match unit leaked into a directory-audit attestation: %+v", claim)
		case unitDirectoriesSelectedBeforeTop:
			if claim.Method != "derived" || claim.Basis != "confirmed" {
				t.Fatalf("selection claim must be confirmed/derived: %+v", claim)
			}
		default:
			if claim.Method != "enumerated" || claim.Basis != "confirmed" {
				t.Fatalf("observed claim must be confirmed/enumerated: %+v", claim)
			}
		}
	}
}

// Complete traversal with unavailable allocation is complete traversal
// coverage; the document carries no byte claim at all.
func TestBuildAuditCompleteTraversalWithUnavailableAllocation(t *testing.T) {
	root := writeAuditTree(t)
	opts := auditRunOptions(root)
	opts.SizeBasis = inventory.SizeAllocated
	restore := inventory.SetFileAllocationUnavailableForTest()
	defer restore()
	collector, _, err := runAuditCollected(t, context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	document, err := buildAuditForTest(t, collector)
	if err != nil {
		t.Fatal(err)
	}
	if document.CoverageState != coverageComplete {
		t.Fatalf("state=%s", document.CoverageState)
	}
	payload, _ := json.Marshal(document)
	if strings.Contains(string(payload), "bytes") {
		t.Fatalf("attestation carries a byte claim: %s", payload)
	}
}

// An affecting traversal gap yields a partial attestation with an opaque,
// root-scoped gap; the protected directory name never appears.
func TestBuildAuditAffectingGapIsPartialAndOpaque(t *testing.T) {
	root := writeAuditTree(t)
	sealed := filepath.Join(root, "sealed-private-name")
	if err := os.Mkdir(sealed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0); err != nil {
		t.Skipf("permission fixture unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })
	if file, err := os.Open(sealed); err == nil {
		_ = file.Close()
		t.Skip("test identity can read mode-000 directory")
	}
	collector, _, err := runAuditCollected(t, context.Background(), auditRunOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	document, err := buildAuditForTest(t, collector)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := MarshalValidated(document)
	if err != nil {
		t.Fatal(err)
	}
	if document.CoverageState != coveragePartial || len(document.Gaps) != 1 ||
		document.Gaps[0].Code != "permission" ||
		!strings.HasPrefix(document.Gaps[0].Scope.Prefix, "opaque:hmac-sha256:") {
		t.Fatalf("gap projection=%+v state=%s", document.Gaps, document.CoverageState)
	}
	for _, leak := range []string{"sealed-private-name", root} {
		if bytes.Contains(payload, []byte(leak)) {
			t.Fatalf("attestation discloses %q:\n%s", leak, payload)
		}
	}
}

func requireWithheld(t *testing.T, collector *AuditCollector) {
	t.Helper()
	_, err := buildAuditForTest(t, collector)
	var withheld *WithheldError
	if !errors.As(err, &withheld) {
		t.Fatalf("want withheld attestation, got %v", err)
	}
}

// Failed, canceled and incomplete audits withhold the attestation.
func TestBuildAuditWithholdsUnpublishableResults(t *testing.T) {
	root := writeAuditTree(t)

	t.Run("budget failure", func(t *testing.T) {
		opts := auditRunOptions(root)
		opts.MaxAggregateDirectories = 1
		collector, _, err := runAuditCollected(t, context.Background(), opts)
		if err == nil {
			t.Fatal("expected budget failure")
		}
		requireWithheld(t, collector)
	})
	for name, hooks := range map[string]func(cancel context.CancelFunc) *inventory.AuditHooksForTest{
		"canceled before selection": func(cancel context.CancelFunc) *inventory.AuditHooksForTest {
			return &inventory.AuditHooksForTest{BeforeSelect: cancel}
		},
		"canceled during emitted prefix": func(cancel context.CancelFunc) *inventory.AuditHooksForTest {
			return &inventory.AuditHooksForTest{BeforeRow: func(i int) {
				if i == 1 {
					cancel()
				}
			}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts := auditRunOptions(root)
			inventory.SetAuditHooksForTest(&opts, hooks(cancel))
			collector, _, err := runAuditCollected(t, ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			requireWithheld(t, collector)
		})
	}
	t.Run("output failure before summary", func(t *testing.T) {
		collector, err := NewAuditCollector(&failingAuditSink{failRows: true}, DefaultMaxGaps)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inventory.Run(context.Background(), auditRunOptions(root), collector); err == nil {
			t.Fatal("expected output failure")
		}
		requireWithheld(t, collector)
	})
	t.Run("terminal summary write failure", func(t *testing.T) {
		collector, err := NewAuditCollector(&failingAuditSink{failSummary: true}, DefaultMaxGaps)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inventory.Run(context.Background(), auditRunOptions(root), collector); err == nil {
			t.Fatal("expected terminal write failure")
		}
		requireWithheld(t, collector)
	})
}

// A cancellation after every row committed does not undo the result, so the
// attestation is still published.
func TestBuildAuditPublishesAfterPostCommitCancellation(t *testing.T) {
	root := writeAuditTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := auditRunOptions(root)
	inventory.SetAuditHooksForTest(&opts, &inventory.AuditHooksForTest{AfterCommit: cancel})
	collector, _, err := runAuditCollected(t, ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	document, err := buildAuditForTest(t, collector)
	if err != nil || document.CoverageState != coverageComplete {
		t.Fatalf("post-commit cancellation: state=%s err=%v", document.CoverageState, err)
	}
}

// Evidence that contradicts the stream semantics is refused, not withheld.
func TestAuditCollectorRefusesInconsistentEvidence(t *testing.T) {
	root := t.TempDir()
	collector, err := NewAuditCollector(inventory.NewJSONLSink(io.Discard), DefaultMaxGaps)
	if err != nil {
		t.Fatal(err)
	}
	header := inventory.AuditHeader{
		RunID: "forged", Profile: inventory.AggregationProfileV2,
		Roots:          []inventory.Root{{ID: "root-1", Path: root}},
		EmissionMode:   inventory.EmissionDirectoryAudit,
		PathProtection: inventory.PathProtectionSourceStructure,
		DirectorySelection: inventory.AuditDirectorySelection{
			SizeBasis: inventory.SizeApparent, Depth: -1,
		},
	}
	if err := collector.AuditHeader(header); err != nil {
		t.Fatal(err)
	}
	// A complete summary that hides a root's visited directories.
	if err := collector.AuditSummary(inventory.AuditSummary{
		Lifecycle: inventory.LifecycleComplete, SelectionReconciled: true, EmissionCompleted: true,
		AuditSelectionCounts: &inventory.AuditSelectionCounts{},
		TerminalObservedAt:   time.Now(),
		VisitedDirectories:   5,
		Roots: []inventory.AuditRootSummary{{
			RootID: "root-1", Lifecycle: inventory.LifecycleComplete, VisitedDirectories: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = collector.Evidence()
	var withheld *WithheldError
	if err == nil || errors.As(err, &withheld) || !strings.Contains(err.Error(), "does not reconcile") {
		t.Fatalf("want reconciliation refusal, got %v", err)
	}
	if _, err := BuildAudit(AuditEvidence{header: header}, BuildOptions{
		EmitterName: "spanwit", EmitterVersion: "test",
	}); err == nil {
		t.Fatal("hand-built evidence bypassed the collector checks")
	}
}

// Collector overflow refuses publication.
func TestAuditCollectorGapOverflowRefuses(t *testing.T) {
	collector, err := NewAuditCollector(inventory.NewJSONLSink(io.Discard), 1)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := collector.AuditHeader(inventory.AuditHeader{
		RunID: "overflow", Roots: []inventory.Root{{ID: "root-1", Path: root}},
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := collector.Gap(inventory.Gap{RootID: "root-1", Kind: "permission", AffectsCompleteness: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := collector.AuditSummary(inventory.AuditSummary{TerminalObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Evidence(); err == nil {
		t.Fatal("expected refusal")
	}
}

// Root replacement between the walk and publication is refused.
func TestWriteAuditDocumentRefusesReplacedRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "patient")
	if err := os.MkdirAll(filepath.Join(root, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keep", "a.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	collector, _, err := runAuditCollected(t, context.Background(), auditRunOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := collector.Evidence()
	if err != nil {
		t.Fatal(err)
	}
	document, err := BuildAudit(evidence, BuildOptions{EmitterName: "spanwit", EmitterVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(base, "coverage.json")
	if err := WriteAuditDocument(destination, document, evidence); err == nil {
		t.Fatal("replaced root was attested")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination created: %v", err)
	}
}

type failingAuditSink struct {
	failRows    bool
	failSummary bool
}

func (s *failingAuditSink) Header(inventory.Header) error           { return nil }
func (s *failingAuditSink) Entry(inventory.Entry) error             { return nil }
func (s *failingAuditSink) Gap(inventory.Gap) error                 { return nil }
func (s *failingAuditSink) Summary(inventory.Summary) error         { return nil }
func (s *failingAuditSink) AuditHeader(inventory.AuditHeader) error { return nil }
func (s *failingAuditSink) AuditSummary(inventory.AuditSummary) error {
	if s.failSummary {
		return io.ErrClosedPipe
	}
	return nil
}
func (s *failingAuditSink) AuditDirectory(inventory.AuditDirectory) error {
	if s.failRows {
		return io.ErrClosedPipe
	}
	return nil
}

// Regression: checked evidence is a snapshot; editing a copy cannot change
// what was checked, and a missing selection-count block fails closed.
func TestBuildAuditEvidenceIsImmutableAndFailsClosed(t *testing.T) {
	root := writeAuditTree(t)
	collector, _, err := runAuditCollected(t, context.Background(), auditRunOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := collector.Evidence()
	if err != nil {
		t.Fatal(err)
	}
	evidence.summary.Roots[0].VisitedEntries = 999999
	evidence.summary.SelectedBeforeTop = 12345
	again, err := collector.Evidence()
	if err != nil {
		t.Fatal(err)
	}
	if again.summary.Roots[0].VisitedEntries == 999999 || again.summary.SelectedBeforeTop == 12345 {
		t.Fatal("editing returned evidence changed the collector's checked snapshot")
	}
	again.summary.AuditSelectionCounts = nil
	if _, err := BuildAudit(again, BuildOptions{EmitterName: "spanwit", EmitterVersion: "test"}); err == nil {
		t.Fatal("missing selection counts did not fail closed")
	}
}

// Regression: an unpublishable audit is withheld even when the collector also
// overflowed or saw an inconsistency, so the request does not change the
// audit's exit status.
func TestAuditCollectorWithholdsBeforeOverflowAndInvalid(t *testing.T) {
	collector, err := NewAuditCollector(inventory.NewJSONLSink(io.Discard), 1)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := collector.AuditHeader(inventory.AuditHeader{
		RunID: "canceled-overflow", Roots: []inventory.Root{{ID: "root-1", Path: root}},
	}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := collector.Gap(inventory.Gap{RootID: "root-1", Kind: "permission", AffectsCompleteness: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := collector.AuditSummary(inventory.AuditSummary{
		Lifecycle: inventory.LifecyclePartial, Canceled: true, TerminalObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err = collector.Evidence()
	var withheld *WithheldError
	if !errors.As(err, &withheld) {
		t.Fatalf("canceled audit with overflow: want withheld, got %v", err)
	}
}

// Regression: a reconciliation refusal names no directory.
func TestAuditCollectorRefusalIsPathFree(t *testing.T) {
	collector, err := NewAuditCollector(inventory.NewJSONLSink(io.Discard), DefaultMaxGaps)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := collector.AuditHeader(inventory.AuditHeader{
		RunID: "leak", Roots: []inventory.Root{{ID: "root-1", Path: root}},
		DirectorySelection: inventory.AuditDirectorySelection{SizeBasis: inventory.SizeApparent, Depth: -1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := collector.AuditDirectory(inventory.AuditDirectory{
		RootID: "root-1", RelativePath: "secret-client-name/../x",
	}); err != nil {
		t.Fatal(err)
	}
	if err := collector.AuditSummary(inventory.AuditSummary{
		Lifecycle: inventory.LifecycleComplete, SelectionReconciled: true, EmissionCompleted: true,
		AuditSelectionCounts: &inventory.AuditSelectionCounts{}, TerminalObservedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	_, err = collector.Evidence()
	if err == nil || strings.Contains(err.Error(), "secret-client-name") || strings.Contains(err.Error(), root) {
		t.Fatalf("refusal missing or discloses a path: %v", err)
	}
}
