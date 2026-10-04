package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"

	"github.com/3leaps/spanwit/internal/coverageattestation"
	"github.com/3leaps/spanwit/internal/inventory"
)

func TestRunInventoryTextAndJSONL(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	writeInventoryFile(t, filepath.Join(root, "large.bin"), 2048, now.Add(-48*time.Hour))
	writeInventoryFile(t, filepath.Join(root, "small.bin"), 10, now.Add(-48*time.Hour))

	for _, format := range []string{"text", "jsonl"} {
		t.Run(format, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, inventoryRunOpts{
				roots: []string{root}, entryType: "file", minSize: "1KiB",
				olderThan: "1d", sizeBasis: "apparent", workers: "1",
				backend: "auto", format: format, quiet: true,
				now: now, runID: "cmd-test",
			})
			if code != 0 {
				t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
			}
			if !strings.Contains(stdout.String(), "large.bin") ||
				strings.Contains(stdout.String(), "small.bin") {
				t.Fatalf("unexpected output:\n%s", stdout.String())
			}
			if strings.Contains(stdout.String(), "--execute") ||
				strings.Contains(stdout.String(), `"prunable"`) {
				t.Fatalf("inventory implies action authority:\n%s", stdout.String())
			}
			if format == "text" {
				if !strings.Contains(stdout.String(), "lifecycle=complete") {
					t.Fatalf("missing terminal summary:\n%s", stdout.String())
				}
				return
			}
			lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
			if len(lines) != 3 {
				t.Fatalf("jsonl records=%d\n%s", len(lines), stdout.String())
			}
			var terminal map[string]any
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &terminal); err != nil {
				t.Fatal(err)
			}
			if terminal["type"] != "spanwit.inventory.summary.v1" {
				t.Fatalf("terminal=%v", terminal)
			}
		})
	}
}

func TestRunInventoryDirectorySummaryJSONL(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInventoryFile(t, filepath.Join(root, "nested", "file.bin"), 42, time.Now())
	var stdout, stderr bytes.Buffer
	code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, inventoryRunOpts{
		roots: []string{root}, entryType: "file", minSize: "1MiB",
		workers: "1", backend: "serial", format: "jsonl", quiet: true,
		directorySummary: true, directoryDepth: 0, directoryTop: 1,
		maxAggregateDirectories: 10,
	})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("records=%d\n%s", len(lines), stdout.String())
	}
	var header, directory map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &directory); err != nil {
		t.Fatal(err)
	}
	if header["type"] != inventory.RecordHeader ||
		header["data"].(map[string]any)["profile"] != inventory.AggregationProfileV0 {
		t.Fatalf("header=%v", header)
	}
	data := directory["data"].(map[string]any)
	if directory["type"] != inventory.RecordDirectory || data["relative_path"] != "." ||
		data["apparent_bytes_sum"] != float64(42) || data["file_count"] != float64(1) {
		t.Fatalf("directory=%v", directory)
	}
	if _, hasAccounting := data["accounting"]; hasAccounting {
		t.Fatalf("v0 directory record must not carry accounting: %v", directory)
	}
}

func TestRunInventoryDirectoryAccountingJSONL(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInventoryFile(t, filepath.Join(root, "nested", "file.bin"), 42, time.Now())
	var stdout, stderr bytes.Buffer
	code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, inventoryRunOpts{
		roots: []string{root}, entryType: "file",
		workers: "1", backend: "serial", format: "jsonl", quiet: true,
		directorySummary: true, directoryDepth: 0, directoryTop: 1,
		maxAggregateDirectories: 10, directoryAccounting: true,
	})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("records=%d\n%s", len(lines), stdout.String())
	}
	var header, directory map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &directory); err != nil {
		t.Fatal(err)
	}
	if header["type"] != inventory.RecordHeader ||
		header["data"].(map[string]any)["profile"] != inventory.AggregationProfileV1 ||
		header["data"].(map[string]any)["directory_accounting"] != true {
		t.Fatalf("header=%v", header)
	}
	data := directory["data"].(map[string]any)
	accounting, ok := data["accounting"].(map[string]any)
	if !ok {
		t.Fatalf("directory=%v", directory)
	}
	apparent := accounting["apparent"].(map[string]any)
	if apparent["status"] != "measured" || apparent["bound"] != "exact" ||
		apparent["basis"] != "path_entry_logical_size_sum" ||
		apparent["bytes"] != float64(42) {
		t.Fatalf("apparent claim=%v", apparent)
	}
	reclaim := accounting["expected_reclaim"].(map[string]any)
	if reclaim["status"] != "unsupported" || reclaim["bytes"] != nil {
		t.Fatalf("expected_reclaim claim=%v", reclaim)
	}
}

func TestRunInventoryPrint0IsPathsOnly(t *testing.T) {
	root := t.TempDir()
	writeInventoryFile(t, filepath.Join(root, "a.bin"), 10, time.Now())
	writeInventoryFile(t, filepath.Join(root, "b.bin"), 10, time.Now())

	var stdout, stderr bytes.Buffer
	code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, inventoryRunOpts{
		roots: []string{root}, entryType: "file", workers: "1",
		backend: "serial", format: "text", print0: true, quiet: true,
	})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	parts := bytes.Split(bytes.TrimSuffix(stdout.Bytes(), []byte{0}), []byte{0})
	got := make([]string, 0, len(parts))
	for _, part := range parts {
		got = append(got, filepath.Base(string(part)))
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"a.bin", "b.bin"}) {
		t.Fatalf("paths=%v raw=%q", got, stdout.Bytes())
	}
	if !strings.Contains(stderr.String(), "Inventory summary: lifecycle=complete") {
		t.Fatalf("missing stderr summary: %s", stderr.String())
	}
}

func TestRunInventoryUsageErrorsPrecedeOutput(t *testing.T) {
	cases := []inventoryRunOpts{
		{entryType: "file", workers: "auto", format: "text"},
		{roots: []string{t.TempDir()}, entryType: "dir", workers: "auto", format: "text"},
		{roots: []string{t.TempDir()}, entryType: "file", minSize: "1.5G", workers: "auto", format: "text"},
		{roots: []string{t.TempDir()}, entryType: "file", olderThan: "1y", workers: "auto", format: "text"},
		{roots: []string{t.TempDir()}, entryType: "file", olderThan: "30d", newerThan: "7d", workers: "auto", format: "text"},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "zero", format: "text"},
		{roots: []string{t.TempDir()}, entryType: "file", sizeBasis: "disk", workers: "auto", format: "text"},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "auto", backend: "find", format: "text"},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "4", backend: "serial", format: "text"},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "1", backend: "parallel", format: "text"},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "auto", top: -1, format: "text"},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "auto", maxPendingDirs: -1, format: "text"},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "auto", format: "text", directorySummary: true, maxAggregateDirectories: 10},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "auto", format: "jsonl", directorySummary: true, top: 1, maxAggregateDirectories: 10},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "auto", format: "jsonl", directorySummary: true, directoryDepth: -2, maxAggregateDirectories: 10},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "auto", format: "jsonl", directorySummary: true, maxAggregateDirectories: 0},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "auto", format: "jsonl", directoryAccounting: true},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "auto", format: "text", directorySummary: true, directoryAccounting: true, maxAggregateDirectories: 10},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "auto", format: "jsonl", print0: true},
		{roots: []string{t.TempDir()}, entryType: "file", workers: "auto", format: "text", stallTimeout: -time.Second},
	}
	for i, opts := range cases {
		var stdout, stderr bytes.Buffer
		code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, opts)
		if code != 3 {
			t.Errorf("case %d exit=%d stderr=%s", i, code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("case %d wrote payload before usage rejection: %q", i, stdout.String())
		}
	}
}

func TestInventoryCommandHasNoExecuteFlag(t *testing.T) {
	cmd := newInventoryCmd(inventoryTestIdentity())
	if cmd.Flags().Lookup("execute") != nil {
		t.Fatal("inventory must not expose --execute")
	}
	if !strings.Contains(cmd.Long, "no execute or deletion path") {
		t.Fatalf("help does not state read-only boundary: %s", cmd.Long)
	}
	for _, name := range []string{"summary-only", "exclude", "coverage-attest", "quiet"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("inventory command missing --%s", name)
		}
	}
}

func TestRunInventorySummaryOnlyExclusionsAndAttestation(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "patient")
	if err := os.MkdirAll(filepath.Join(root, "excluded"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC)
	writeInventoryFile(t, filepath.Join(root, "keep.bin"), 20, now)
	writeInventoryFile(t, filepath.Join(root, "excluded", "skip.bin"), 30, now)
	attestationPath := filepath.Join(base, "coverage.json")

	var stdout, stderr bytes.Buffer
	code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, inventoryRunOpts{
		roots: []string{root}, entryType: "file", workers: "1",
		backend: "serial", format: "jsonl", quiet: true,
		summaryOnly: true, exclusions: []string{"excluded"},
		coverageAttest: attestationPath, now: now, runID: "summary-run",
	})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if strings.Contains(stdout.String(), `"type":"spanwit.inventory.entry.v1"`) {
		t.Fatalf("summary-only emitted entry:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"emission_mode":"summary_only"`) ||
		!strings.Contains(stdout.String(), `"matched_count":1`) ||
		!strings.Contains(stdout.String(), `"emitted_count":0`) ||
		!strings.Contains(stdout.String(), `"exclusion_count":1`) {
		t.Fatalf("summary-only profile accounting missing:\n%s", stdout.String())
	}
	payload, err := os.ReadFile(attestationPath)
	if err != nil {
		t.Fatal(err)
	}
	document, err := coverageattestation.DecodeValidated(payload)
	if err != nil {
		t.Fatal(err)
	}
	terminalObservedAt := inventoryTerminalObservedAt(t, stdout.String())
	if document.CoverageState != "complete" ||
		document.Subject.SubjectURI != "urn:spanwit:filesystem-inventory:summary-run" {
		t.Fatalf("attestation=%+v", document)
	}
	if document.AsOf != terminalObservedAt {
		t.Fatalf("attestation as_of=%q terminal_observed_at=%q", document.AsOf, terminalObservedAt)
	}
	observed := map[string]int64{}
	for _, claim := range document.Claims {
		if claim.Volume != nil {
			observed[claim.Volume.Unit] = claim.Volume.Observed
		}
	}
	if observed["entries_matched"] != 1 || observed["entries_emitted"] != 0 {
		t.Fatalf("attestation volumes=%v", observed)
	}
	if strings.Contains(string(payload), root) ||
		strings.Contains(string(payload), "keep.bin") ||
		strings.Contains(string(payload), "excluded") {
		t.Fatalf("attestation leaked protected paths:\n%s", payload)
	}
}

func TestRunInventoryProgressIsStderrOnlyAndQuietSuppresses(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC)
	writeInventoryFile(t, filepath.Join(root, "private-name.bin"), 10, now)

	run := func(quiet bool) (string, string, int) {
		var stdout, stderr bytes.Buffer
		code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, inventoryRunOpts{
			roots: []string{root}, entryType: "file", workers: "1",
			backend: "serial", format: "jsonl", quiet: quiet,
			now: now, runID: "progress-run",
		})
		return stdout.String(), stderr.String(), code
	}
	stdout, stderr, code := run(false)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "Inventory progress:") ||
		!strings.Contains(stderr, "Inventory progress:") {
		t.Fatalf("progress routing stdout=%q stderr=%q", stdout, stderr)
	}
	if strings.Contains(stderr, root) || strings.Contains(stderr, "private-name.bin") {
		t.Fatalf("progress disclosed path: %s", stderr)
	}

	quietStdout, quietStderr, code := run(true)
	if code != 0 {
		t.Fatalf("quiet exit=%d stderr=%s", code, quietStderr)
	}
	if strings.Contains(quietStderr, "Inventory progress:") ||
		!reflect.DeepEqual(inventoryJSONLWithoutTimestamps(t, quietStdout),
			inventoryJSONLWithoutTimestamps(t, stdout)) {
		t.Fatalf("quiet changed payload/progress stdout=%q stderr=%q", quietStdout, quietStderr)
	}
}

func TestRunInventoryCoverageDestinationFailsBeforeWalk(t *testing.T) {
	root := t.TempDir()
	writeInventoryFile(t, filepath.Join(root, "data.bin"), 10, time.Now())
	destination := filepath.Join(root, "coverage.json")

	var stdout, stderr bytes.Buffer
	code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, inventoryRunOpts{
		roots: []string{root}, entryType: "file", workers: "1",
		backend: "serial", format: "jsonl", quiet: true,
		coverageAttest: destination,
	})
	if code != 3 || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("invalid destination created: %v", err)
	}
}

func TestPublishInventoryAttestationAllowsTrustworthyFailedTerminal(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "patient")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	collector, err := coverageattestation.NewCollector(inventory.NewJSONLSink(io.Discard), 1)
	if err != nil {
		t.Fatal(err)
	}
	terminal := time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC)
	if err := collector.Header(inventory.Header{
		RunID: "trustworthy-failed", Profile: inventory.ProfileV0,
		Roots:          []inventory.Root{{ID: "root-1", Path: root}},
		EmissionMode:   inventory.EmissionEntries,
		PathProtection: inventory.PathProtectionSourceStructure,
	}); err != nil {
		t.Fatal(err)
	}
	if err := collector.Summary(inventory.Summary{
		Lifecycle: inventory.LifecycleFailed, EmissionMode: inventory.EmissionEntries,
		TerminalObservedAt: terminal, VisitedDirectories: 1,
		Roots: []inventory.RootSummary{{
			RootID: "root-1", Lifecycle: inventory.LifecycleFailed,
			Failed: true, VisitedDirectories: 1,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(base, "coverage.json")
	if err := publishInventoryAttestation(collector, destination, inventoryTestIdentity()); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	document, err := coverageattestation.DecodeValidated(payload)
	if err != nil {
		t.Fatal(err)
	}
	if document.CoverageState != "partial" || document.AsOf != terminal.Format(time.RFC3339Nano) {
		t.Fatalf("attestation=%+v", document)
	}
}

func TestPublishInventoryAttestationRejectsTerminalSinkFailure(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "patient")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	collector, err := coverageattestation.NewCollector(summaryFailureInventorySink{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := collector.Header(inventory.Header{
		RunID: "terminal-sink-failure", Roots: []inventory.Root{{ID: "root-1", Path: root}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := collector.Summary(inventory.Summary{Lifecycle: inventory.LifecycleComplete}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("terminal sink error=%v want closed pipe", err)
	}
	if _, err := collector.Evidence(); err == nil || !strings.Contains(err.Error(), "no terminal summary") {
		t.Fatalf("terminal sink failure unexpectedly produced evidence: %v", err)
	}
	destination := filepath.Join(base, "coverage.json")
	if err := publishInventoryAttestation(collector, destination, inventoryTestIdentity()); err == nil {
		t.Fatal("terminal sink failure unexpectedly published an attestation")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination created after terminal sink failure: %v", err)
	}
}

type summaryFailureInventorySink struct{}

func (summaryFailureInventorySink) Header(inventory.Header) error { return nil }
func (summaryFailureInventorySink) Entry(inventory.Entry) error   { return nil }
func (summaryFailureInventorySink) Gap(inventory.Gap) error       { return nil }
func (summaryFailureInventorySink) Summary(inventory.Summary) error {
	return io.ErrClosedPipe
}

func TestRunInventoryCreatesNoFiles(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "patient")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeInventoryFile(t, filepath.Join(root, "data.bin"), 128, time.Now())
	before := listInventoryTree(t, base)

	var stdout, stderr bytes.Buffer
	code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, inventoryRunOpts{
		roots: []string{root}, entryType: "file", workers: "4",
		backend: "parallel", format: "jsonl", quiet: true,
	})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	after := listInventoryTree(t, base)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("command changed patient tree:\nbefore=%v\nafter=%v", before, after)
	}
}

func inventoryTestIdentity() *appidentity.Identity {
	return &appidentity.Identity{
		BinaryName: "spanwit", EnvPrefix: "SPANWIT_", ConfigName: "spanwit",
		Description: "test",
	}
}

func inventoryJSONLWithoutTimestamps(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		delete(record, "ts")
		if data, ok := record["data"].(map[string]any); ok {
			delete(data, "duration_nanos")
			delete(data, "time_to_first_match_nanos")
			delete(data, "observed_at")
			delete(data, "terminal_observed_at")
		}
		records = append(records, record)
	}
	return records
}

func inventoryTerminalObservedAt(t *testing.T, raw string) string {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var record struct {
			Type string `json:"type"`
			Data struct {
				TerminalObservedAt string `json:"terminal_observed_at"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Type == inventory.RecordSummary || record.Type == inventory.RecordSummaryV2 {
			if record.Data.TerminalObservedAt == "" {
				t.Fatal("summary omitted terminal_observed_at")
			}
			return record.Data.TerminalObservedAt
		}
	}
	t.Fatal("inventory stream omitted terminal summary")
	return ""
}

func writeInventoryFile(t *testing.T, path string, size int64, modTime time.Time) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func listInventoryTree(t *testing.T, root string) map[string][2]int64 {
	t.Helper()
	out := map[string][2]int64{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		out[path] = [2]int64{info.Size(), info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStallAlertLine_CountersOnly(t *testing.T) {
	got := stallAlertLine(inventory.StallAlert{Pending: 2, Waited: 10 * time.Second, Timeout: time.Minute})
	want := "spanwit: 2 directory open(s) not responding for 10s; will skip after 1m0s (--stall-timeout; --include-remote is off by default)"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if got := stallAlertLine(inventory.StallAlert{Pending: 1, Waited: 10 * time.Second}); !strings.Contains(got, "no limit (--stall-timeout 0)") {
		t.Fatalf("unlimited wording: %q", got)
	}
}

func TestRunInventoryAggregateBudgetDiagnosticIsActionableAndPathFree(t *testing.T) {
	root := t.TempDir()
	const secret = "zz-rejected-secret-dir"
	for _, dir := range []string{"a", secret} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeInventoryFile(t, filepath.Join(root, "a", "x.bin"), 1, time.Now())
	writeInventoryFile(t, filepath.Join(root, secret, "y.bin"), 1, time.Now())
	var stdout, stderr bytes.Buffer
	code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, inventoryRunOpts{
		roots: []string{root}, entryType: "file", workers: "1", backend: "serial",
		format: "jsonl", quiet: true, directorySummary: true, directoryDepth: 0,
		maxAggregateDirectories: 2,
	})
	if code != 2 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	diag := stderr.String()
	for _, want := range []string{
		"aggregate directory budget exhausted",
		"limit=2 retained=2 attempted_retained=3",
		"No directory totals were emitted",
		"--max-aggregate-directories",
		"--directory-depth",
	} {
		if !strings.Contains(diag, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, diag)
		}
	}
	if strings.Contains(diag, secret) || strings.Contains(diag, root) {
		t.Fatalf("diagnostic discloses a path:\n%s", diag)
	}
	if strings.Contains(stdout.String(), "budget") || strings.Contains(stdout.String(), `"spanwit.inventory.directory`) {
		t.Fatalf("budget diagnostic or directory totals leaked to stdout:\n%s", stdout.String())
	}
}

func TestInventoryHelpConnectsDirectoryBounds(t *testing.T) {
	cmd := newInventoryCmd(inventoryTestIdentity())
	for _, want := range []string{
		"--max-aggregate-directories  retained aggregate states",
		"--max-pending-dirs",
		"--max-open-dirs",
		"reducing depth or\ntop does not make it fit",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Fatalf("help missing %q:\n%s", want, cmd.Long)
		}
	}
	if usage := cmd.Flags().Lookup("max-aggregate-directories").Usage; !strings.Contains(usage, "not limited by --directory-depth") {
		t.Fatalf("flag usage=%q", usage)
	}
}

func directoriesTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeInventoryFileMkdir(t, filepath.Join(root, "big", "a.bin"), 3<<20)
	writeInventoryFileMkdir(t, filepath.Join(root, "small", "b.bin"), 10)
	return root
}

// writeInventoryFileMkdir writes real (non-sparse) bytes so allocated-basis
// audits see the size; writeInventoryFile truncates and allocates ~nothing.
func writeInventoryFileMkdir(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x5a}, int(size)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func directoriesOpts(root, format string) inventoryRunOpts {
	return inventoryRunOpts{
		roots: []string{root}, entryType: "file", workers: "1", backend: "serial",
		format: format, quiet: true, directories: true, directoryDepth: -1,
		maxAggregateDirectories: 1000, runID: "dir-test",
	}
}

func TestRunInventoryDirectoriesRefusesBeforeDiscovery(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cases := map[string]func(o *inventoryRunOpts){
		"text without floor or top": func(o *inventoryRunOpts) {},
		"file min-size":             func(o *inventoryRunOpts) { o.explicitFlags = []string{"--min-size"}; o.directoryMinSize = "1" },
		"older-than":                func(o *inventoryRunOpts) { o.explicitFlags = []string{"--older-than"}; o.directoryMinSize = "1" },
		"newer-than":                func(o *inventoryRunOpts) { o.explicitFlags = []string{"--newer-than"}; o.directoryMinSize = "1" },
		"file top":                  func(o *inventoryRunOpts) { o.explicitFlags = []string{"--top"}; o.directoryMinSize = "1" },
		"type":                      func(o *inventoryRunOpts) { o.explicitFlags = []string{"--type"}; o.directoryMinSize = "1" },
		"print0":                    func(o *inventoryRunOpts) { o.explicitFlags = []string{"--print0"}; o.directoryMinSize = "1" },
		"summary-only":              func(o *inventoryRunOpts) { o.explicitFlags = []string{"--summary-only"}; o.directoryMinSize = "1" },
		"directory-summary":         func(o *inventoryRunOpts) { o.explicitFlags = []string{"--directory-summary"}; o.directoryMinSize = "1" },
		"directory-accounting": func(o *inventoryRunOpts) {
			o.explicitFlags = []string{"--directory-accounting"}
			o.directoryMinSize = "1"
		},
		"bad floor": func(o *inventoryRunOpts) { o.directoryMinSize = "5XB" },
		"attestation destination inside the root": func(o *inventoryRunOpts) {
			o.coverageAttest = filepath.Join(o.roots[0], "coverage.json")
			o.directoryMinSize = "1"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			opts := directoriesOpts(missing, "text")
			mutate(&opts)
			var stdout, stderr bytes.Buffer
			code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, opts)
			// Exit 3 on the missing root proves refusal happened before
			// discovery: admission would have failed with exit 2.
			if code != 3 || stdout.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%s", code, stdout.String(), stderr.String())
			}
		})
	}
	var stdout, stderr bytes.Buffer
	opts := directoriesOpts(missing, "text")
	opts.directories, opts.directoryMinSize = false, "1GiB"
	if code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, opts); code != 3 ||
		!strings.Contains(stderr.String(), "--directory-min-size requires --directories") {
		t.Fatalf("floor without mode: exit=%d stderr=%s", code, stderr.String())
	}
}

func TestRunInventoryDirectoriesBasisDefaultAndPlatformRefusal(t *testing.T) {
	root := directoriesTestTree(t)
	var stdout, stderr bytes.Buffer
	opts := directoriesOpts(root, "jsonl")
	if code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, opts); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"size_basis":"allocated"`) {
		t.Fatalf("defaulted basis is not allocated:\n%s", stdout.String())
	}
	stdout.Reset()
	opts.explicitFlags, opts.sizeBasis = []string{"--size-basis"}, "apparent"
	if code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, opts); code != 0 ||
		!strings.Contains(stdout.String(), `"size_basis":"apparent"`) {
		t.Fatalf("explicit apparent not honored: exit=%d\n%s", code, stdout.String())
	}

	saved := allocatedSizesSupported
	allocatedSizesSupported = false
	t.Cleanup(func() { allocatedSizesSupported = saved })
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cases := []struct {
		name   string
		format string
		opts   func(o *inventoryRunOpts)
		code   int
	}{
		{"defaulted allocated refused", "jsonl", func(o *inventoryRunOpts) {}, 3},
		{"explicit allocated text without top refused", "text", func(o *inventoryRunOpts) {
			o.explicitFlags, o.sizeBasis, o.directoryMinSize = []string{"--size-basis"}, "allocated", "1GiB"
		}, 3},
		{"explicit allocated text with top runs", "text", func(o *inventoryRunOpts) {
			o.explicitFlags, o.sizeBasis, o.directoryMinSize, o.directoryTop = []string{"--size-basis"}, "allocated", "1GiB", 2
		}, 2},
		{"explicit allocated jsonl runs", "jsonl", func(o *inventoryRunOpts) {
			o.explicitFlags, o.sizeBasis = []string{"--size-basis"}, "allocated"
		}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := directoriesOpts(missing, tc.format)
			tc.opts(&o)
			var stdout, stderr bytes.Buffer
			code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, o)
			// 2 = passed validation and failed only at root admission.
			if code != tc.code {
				t.Fatalf("exit=%d want=%d stderr=%s", code, tc.code, stderr.String())
			}
			if tc.code == 3 && !strings.Contains(stderr.String(), "--size-basis apparent") {
				t.Fatalf("refusal lacks guidance: %s", stderr.String())
			}
		})
	}
}

func TestRunInventoryDirectoriesTextMatchesJSONL(t *testing.T) {
	root := directoriesTestTree(t)
	var text, jsonl, stderr bytes.Buffer
	textOpts := directoriesOpts(root, "text")
	textOpts.directoryMinSize = "1MiB"
	if code := runInventory(context.Background(), inventoryTestIdentity(), &text, &stderr, textOpts); code != 0 {
		t.Fatalf("text exit=%d stderr=%s", code, stderr.String())
	}
	jsonOpts := directoriesOpts(root, "jsonl")
	jsonOpts.directoryMinSize = "1MiB"
	if code := runInventory(context.Background(), inventoryTestIdentity(), &jsonl, &stderr, jsonOpts); code != 0 {
		t.Fatalf("jsonl exit=%d stderr=%s", code, stderr.String())
	}
	if err := inventory.ValidateAuditStream(bytes.NewReader(jsonl.Bytes())); err != nil {
		t.Fatal(err)
	}
	var jsonPaths []string
	for _, line := range strings.Split(strings.TrimSpace(jsonl.String()), "\n") {
		var rec struct {
			Type string                   `json:"type"`
			Data inventory.AuditDirectory `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Type == inventory.RecordDirectoryV2 {
			jsonPaths = append(jsonPaths, rec.Data.RelativePath)
		}
	}
	out := text.String()
	for _, want := range []string{"floor=1 MiB (1048576 bytes)", "size_basis=allocated", "DEPTH", "emission_completed=true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("text missing %q:\n%s", want, out)
		}
	}
	rows := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "  meets  ") || strings.Contains(line, "meets ") && strings.Contains(line, root) {
			rows++
		}
	}
	if rows != len(jsonPaths) || strings.Contains(out, filepath.Join(root, "small")) {
		t.Fatalf("text rows=%d jsonl rows=%v\n%s", rows, jsonPaths, out)
	}
}

func TestRunInventoryDirectoriesTextNeutralizesControlCharacters(t *testing.T) {
	root := t.TempDir()
	hostile := "evil\x1b]0;pwn\x07\rSummary: lifecycle=complete\tx\n"
	writeInventoryFileMkdir(t, filepath.Join(root, hostile, "a.bin"), 2<<20)
	var stdout, stderr bytes.Buffer
	opts := directoriesOpts(root, "text")
	opts.directoryMinSize = "1MiB"
	if code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, opts); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, bad := range []string{"\x1b", "\x07", "\r", "\t"} {
		if strings.Contains(out, bad) {
			t.Fatalf("text output contains control byte %q:\n%q", bad, out)
		}
	}
	// The hostile name stays inside its own row: exactly one line starts as
	// a summary, and every row line keeps the table's column prefix.
	summaries := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Summary: ") {
			summaries++
		}
	}
	if summaries != 1 || !strings.Contains(out, "evil\uFFFD]0;pwn\uFFFD\uFFFDSummary") {
		t.Fatalf("a path forged or escaped a line:\n%q", out)
	}
	// JSONL keeps the identity exactly (JSON-escaped, not terminal-sanitized).
	var jsonl bytes.Buffer
	jopts := directoriesOpts(root, "jsonl")
	jopts.directoryMinSize = "1MiB"
	if code := runInventory(context.Background(), inventoryTestIdentity(), &jsonl, &stderr, jopts); code != 0 {
		t.Fatalf("jsonl exit=%d", code)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(jsonl.String()), "\n") {
		var rec struct {
			Data inventory.AuditDirectory `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Data.RelativePath == hostile {
			found = true
		}
	}
	if !found {
		t.Fatalf("JSONL did not preserve the exact relative path:\n%s", jsonl.String())
	}
}

func TestRunInventoryDirectoriesFooterWarnsOnUnavailableSizes(t *testing.T) {
	var out bytes.Buffer
	sink := &inventoryAuditTextSink{stdout: &out, stderr: io.Discard, basis: inventory.SizeAllocated}
	err := sink.AuditSummary(inventory.AuditSummary{
		Lifecycle: inventory.LifecycleComplete, SelectionReconciled: true, EmissionCompleted: true,
		DirectoryEmittedCount: 3, DirectoryUnavailableSizeEmittedCount: 3,
		AuditSelectionCounts: &inventory.AuditSelectionCounts{
			DepthEligible: 3, Indeterminate: 3, SelectedBeforeTop: 3, PlannedEmission: 3,
			UnavailableSizeEligible: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "3 folder(s) have no allocated size") ||
		!strings.Contains(out.String(), "--directory-top") || !strings.Contains(out.String(), "--size-basis apparent") {
		t.Fatalf("footer=%s", out.String())
	}
}

func TestRunInventoryDirectoriesBudgetFailureIsTyped(t *testing.T) {
	root := directoriesTestTree(t)
	var stdout, stderr bytes.Buffer
	opts := directoriesOpts(root, "jsonl")
	opts.maxAggregateDirectories = 1
	if code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, opts); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if err := inventory.ValidateAuditStream(bytes.NewReader(stdout.Bytes())); err != nil {
		t.Fatalf("%v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"code":"aggregate_directory_budget_exhausted","limit":1,"retained":1,"attempted_retained":2`) ||
		!strings.Contains(stderr.String(), "No directory totals were emitted") {
		t.Fatalf("stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
}

// Regression: an error that embeds a hostile root path must not forge lines.
func TestRunInventoryErrorLinesAreSanitized(t *testing.T) {
	hostile := filepath.Join(t.TempDir(), "no\x1b[2Jsuch\nError: forged")
	var stdout, stderr bytes.Buffer
	opts := directoriesOpts(hostile, "jsonl")
	if code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, opts); code != 2 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	out := stderr.String()
	if strings.Contains(out, "\x1b") || strings.Count(out, "\n") != 1 || strings.Contains(out, "\nError: forged") {
		t.Fatalf("stderr not sanitized: %q", out)
	}
}

// Directory-audit attestation: published after a complete audit, written
// beside the unchanged v2 stream, and reported only on stderr.
func TestRunInventoryDirectoriesCoverageAttestation(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "patient")
	writeInventoryFileMkdir(t, filepath.Join(root, "big", "a.bin"), 3<<20)
	writeInventoryFileMkdir(t, filepath.Join(root, "small", "b.bin"), 10)

	plain := directoriesOpts(root, "jsonl")
	plain.directoryMinSize = "1MiB"
	var plainOut, plainErr bytes.Buffer
	if code := runInventory(context.Background(), inventoryTestIdentity(), &plainOut, &plainErr, plain); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, plainErr.String())
	}

	destination := filepath.Join(base, "coverage.json")
	attested := plain
	attested.coverageAttest = destination
	var stdout, stderr bytes.Buffer
	if code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, attested); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !reflect.DeepEqual(inventoryJSONLWithoutTimestamps(t, stdout.String()),
		inventoryJSONLWithoutTimestamps(t, plainOut.String())) {
		t.Fatalf("attestation changed the v2 stream:\n%s\nvs\n%s", stdout.String(), plainOut.String())
	}
	if strings.Contains(stdout.String(), "attest") {
		t.Fatalf("attestation status leaked onto the v2 stream:\n%s", stdout.String())
	}
	if err := inventory.ValidateAuditStream(strings.NewReader(stdout.String())); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	document, err := coverageattestation.DecodeValidated(payload)
	if err != nil {
		t.Fatal(err)
	}
	if document.CoverageState != "complete" || document.Emitter.RunID != "dir-test" ||
		document.AsOf != inventoryTerminalObservedAt(t, stdout.String()) {
		t.Fatalf("attestation=%+v", document)
	}
	for _, leak := range []string{root, "big", "small"} {
		if bytes.Contains(payload, []byte(leak)) {
			t.Fatalf("attestation discloses %q:\n%s", leak, payload)
		}
	}

	// An existing destination is refused before the walk and never replaced.
	stdout.Reset()
	stderr.Reset()
	if code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, attested); code != 3 ||
		stdout.Len() != 0 {
		t.Fatalf("existing destination: exit=%d stdout=%q", code, stdout.String())
	}
	if after, _ := os.ReadFile(destination); !bytes.Equal(after, payload) {
		t.Fatal("existing attestation changed")
	}
}

// A failed audit withholds the attestation: no file, an explicit stderr
// notice, and the audit's own failure status.
func TestRunInventoryDirectoriesWithheldAttestationIsExplicit(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "patient")
	writeInventoryFileMkdir(t, filepath.Join(root, "a", "b", "c.bin"), 10)
	opts := directoriesOpts(root, "jsonl")
	opts.maxAggregateDirectories = 1
	opts.coverageAttest = filepath.Join(base, "coverage.json")
	var stdout, stderr bytes.Buffer
	code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, opts)
	if code != 2 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "coverage attestation withheld: the audit failed") {
		t.Fatalf("withholding not reported:\n%s", stderr.String())
	}
	if strings.Contains(stdout.String(), "withheld") || strings.Contains(stdout.String(), "attest") {
		t.Fatalf("withholding leaked onto the v2 stream:\n%s", stdout.String())
	}
	if _, err := os.Lstat(opts.coverageAttest); !os.IsNotExist(err) {
		t.Fatalf("withheld attestation created a file: %v", err)
	}
}

// A publication failure after a valid audit fails the invocation without
// rewriting stdout or adding a terminal record.
func TestRunInventoryDirectoriesAttestationWriteFailureKeepsStream(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "patient")
	writeInventoryFileMkdir(t, filepath.Join(root, "big", "a.bin"), 3<<20)
	parent := filepath.Join(base, "out")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := directoriesOpts(root, "jsonl")
	opts.directoryMinSize = "1MiB"
	opts.coverageAttest = filepath.Join(parent, "coverage.json")
	// Remove the destination parent after preflight so publication fails.
	opts.beforeAttestationPublishForTest = func() { _ = os.Remove(parent) }
	var stdout, stderr bytes.Buffer
	code := runInventory(context.Background(), inventoryTestIdentity(), &stdout, &stderr, opts)
	if code != 2 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if err := inventory.ValidateAuditStream(strings.NewReader(stdout.String())); err != nil {
		t.Fatalf("audit stream no longer valid after sidecar failure: %v", err)
	}
	if n := strings.Count(stdout.String(), inventory.RecordSummaryV2); n != 1 {
		t.Fatalf("summary records=%d", n)
	}
}
