package inventory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

// auditFixture is a static tree with known apparent sizes. The oracle below
// derives expected directory totals from this table alone, independently of
// the walker and aggregator.
var auditFixture = map[string]int64{
	"big/a.bin":           6000,
	"big/sub/b.bin":       4000,
	"mid/c.bin":           3000,
	"mid/deep/x/d.bin":    2000,
	"small/e.bin":         10,
	"tie1/f.bin":          3000,
	"tie2/g.bin":          3000,
	"top.bin":             1,
	"empty-dir/.keep.bin": 0,
}

func writeAuditFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, size := range auditFixture {
		writeSizedAt(t, filepath.Join(root, filepath.FromSlash(rel)), size, time.Now())
	}
	return root
}

// oracleTotals returns apparent totals per directory (root-relative, "." for
// the root) computed purely from the fixture table.
func oracleTotals() map[string]int64 {
	out := map[string]int64{".": 0}
	for rel, size := range auditFixture {
		out["."] += size
		dir := filepath.ToSlash(filepath.Dir(rel))
		for dir != "." {
			out[dir] += size
			dir = filepath.ToSlash(filepath.Dir(dir))
		}
	}
	return out
}

type auditMemorySink struct {
	memorySink
	header  []AuditHeader
	rows    []AuditDirectory
	summary []AuditSummary
}

func (s *auditMemorySink) AuditHeader(h AuditHeader) error {
	s.header = append(s.header, h)
	return nil
}
func (s *auditMemorySink) AuditDirectory(d AuditDirectory) error {
	s.rows = append(s.rows, d)
	return nil
}
func (s *auditMemorySink) AuditSummary(v AuditSummary) error {
	s.summary = append(s.summary, v)
	return nil
}

func auditOptions(root string) Options {
	return Options{
		Roots: []string{root}, Backend: BackendSerial, Workers: 1,
		EmissionMode: EmissionDirectoryAudit, DirectoryDepth: -1,
		MaxAggregateDirectories: 1000, SizeBasis: SizeApparent, RunID: "audit-test",
	}
}

func int64p(v int64) *int64 { return &v }

// runAuditJSONL runs one audit to a JSONL buffer and requires every record to
// validate against the published schema and the whole stream against the
// semantic validator.
func runAuditJSONL(t *testing.T, opts Options) (*bytes.Buffer, error) {
	t.Helper()
	var stream bytes.Buffer
	_, runErr := Run(context.Background(), opts, NewJSONLSink(&stream))
	requireValidAuditStream(t, stream.Bytes())
	return &stream, runErr
}

func requireValidAuditStream(t *testing.T, raw []byte) {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		if err := contract.ValidateJSON(spanwitschema.SpanwitFilesystemInventoryAggregationV2,
			append([]byte(nil), scanner.Bytes()...)); err != nil {
			t.Fatalf("schema: %v\n%s", err, scanner.Text())
		}
	}
	if err := ValidateAuditStream(bytes.NewReader(raw)); err != nil {
		t.Fatalf("stream semantics: %v\n%s", err, raw)
	}
}

func decodeAudit(t *testing.T, raw []byte) (AuditHeader, []AuditDirectory, AuditSummary) {
	t.Helper()
	var header AuditHeader
	var rows []AuditDirectory
	var summary AuditSummary
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var env struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatal(err)
		}
		var err error
		switch env.Type {
		case RecordHeaderV2:
			err = json.Unmarshal(env.Data, &header)
		case RecordDirectoryV2:
			var row AuditDirectory
			err = json.Unmarshal(env.Data, &row)
			rows = append(rows, row)
		case RecordSummaryV2:
			err = json.Unmarshal(env.Data, &summary)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return header, rows, summary
}

func TestDirectoryAuditMatchesIndependentOracleAcrossWorkers(t *testing.T) {
	root := writeAuditFixture(t)
	oracle := oracleTotals()
	const floor = 3000
	var want []string
	for rel, total := range oracle {
		if total >= floor {
			want = append(want, fmt.Sprintf("%s=%d", rel, total))
		}
	}
	sort.Strings(want)

	var baseline []AuditDirectory
	for _, workers := range []int{1, 4, 8} {
		opts := auditOptions(root)
		opts.Workers, opts.Backend = workers, BackendAuto
		opts.DirectoryFloor = int64p(floor)
		raw, err := runAuditJSONL(t, opts)
		if err != nil {
			t.Fatalf("workers=%d: %v", workers, err)
		}
		_, rows, summary := decodeAudit(t, raw.Bytes())
		var got []string
		for _, row := range rows {
			if row.FloorDecision != FloorMeets {
				t.Fatalf("workers=%d: exact row %q decision=%q", workers, row.RelativePath, row.FloorDecision)
			}
			got = append(got, fmt.Sprintf("%s=%d", row.RelativePath, *row.Accounting.Apparent.Bytes))
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("workers=%d rows=%v want=%v", workers, got, want)
		}
		if summary.Lifecycle != LifecycleComplete || !summary.SelectionReconciled || !summary.EmissionCompleted {
			t.Fatalf("workers=%d summary=%+v", workers, summary)
		}
		if int(summary.DepthEligible) != len(oracle) || summary.Excluded != int64(len(oracle)-len(want)) {
			t.Fatalf("workers=%d counts=%+v oracle=%d", workers, *summary.AuditSelectionCounts, len(oracle))
		}
		if baseline == nil {
			baseline = rows
		} else if !reflect.DeepEqual(baseline, rows) {
			t.Fatalf("workers=%d rows differ from serial run", workers)
		}
	}
}

func TestDirectoryAuditFloorSemantics(t *testing.T) {
	root := writeAuditFixture(t)
	cases := []struct {
		name  string
		floor *int64
		check func(t *testing.T, rows []AuditDirectory, s AuditSummary)
	}{
		{"inclusive floor keeps an exact match", int64p(3000), func(t *testing.T, rows []AuditDirectory, s AuditSummary) {
			found := false
			for _, row := range rows {
				if row.RelativePath == "mid/deep" {
					t.Fatalf("2000-byte directory passed a 3000 floor")
				}
				if row.RelativePath == "tie1" {
					found = true
				}
			}
			if !found {
				t.Fatal("directory exactly at the floor was not kept")
			}
		}},
		{"omitted floor is not_applied everywhere", nil, func(t *testing.T, rows []AuditDirectory, s AuditSummary) {
			for _, row := range rows {
				if row.FloorDecision != FloorNotApplied {
					t.Fatalf("row %q decision=%q", row.RelativePath, row.FloorDecision)
				}
			}
			if s.NotApplied != s.DepthEligible || s.Meets+s.Indeterminate+s.Excluded != 0 {
				t.Fatalf("counts=%+v", *s.AuditSelectionCounts)
			}
		}},
		{"explicit zero floor is applied", int64p(0), func(t *testing.T, rows []AuditDirectory, s AuditSummary) {
			if s.NotApplied != 0 || s.Meets != s.DepthEligible {
				t.Fatalf("counts=%+v", *s.AuditSelectionCounts)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := auditOptions(root)
			opts.DirectoryFloor = tc.floor
			raw, err := runAuditJSONL(t, opts)
			if err != nil {
				t.Fatal(err)
			}
			header, rows, summary := decodeAudit(t, raw.Bytes())
			if header.DirectorySelection.FloorApplied != (tc.floor != nil) {
				t.Fatalf("header selection=%+v", header.DirectorySelection)
			}
			tc.check(t, rows, summary)
		})
	}
}

func TestDirectoryAuditDepthBeforeTopAndDeterministicTies(t *testing.T) {
	root := writeAuditFixture(t)
	opts := auditOptions(root)
	opts.DirectoryDepth, opts.DirectoryTop = 1, 4
	raw, err := runAuditJSONL(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, rows, summary := decodeAudit(t, raw.Bytes())
	var got []string
	for _, row := range rows {
		got = append(got, row.RelativePath)
	}
	// Depth <= 1 first, then rank: root, big (10000), mid (5000), then the
	// 3000-byte tie broken by relative path.
	want := []string{".", "big", "mid", "tie1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows=%v want=%v", got, want)
	}
	if !summary.SelectionTruncated || summary.PlannedEmission != 4 || summary.SelectedBeforeTop <= 4 {
		t.Fatalf("counts=%+v", *summary.AuditSelectionCounts)
	}
}

func TestDirectoryAuditUnavailableAllocationIsNeverZero(t *testing.T) {
	root := writeAuditFixture(t)
	original := fileMetadataOf
	fileMetadataOf = func(info fs.FileInfo) fileMetadata {
		meta := original(info)
		meta.allocated = nil
		return meta
	}
	t.Cleanup(func() { fileMetadataOf = original })

	for _, floor := range []*int64{int64p(1), nil} {
		opts := auditOptions(root)
		opts.SizeBasis, opts.DirectoryFloor, opts.DirectoryTop = SizeAllocated, floor, 3
		raw, err := runAuditJSONL(t, opts)
		if err != nil {
			t.Fatal(err)
		}
		_, rows, summary := decodeAudit(t, raw.Bytes())
		wantDecision := FloorIndeterminate
		if floor == nil {
			wantDecision = FloorNotApplied
		}
		for _, row := range rows {
			if row.Accounting.Allocated.Status != ClaimStatusUnsupported || row.Accounting.Allocated.Bytes != nil {
				t.Fatalf("allocated claim=%+v", row.Accounting.Allocated)
			}
			if row.FloorDecision != wantDecision {
				t.Fatalf("floor=%v decision=%q", floor, row.FloorDecision)
			}
		}
		c := summary.AuditSelectionCounts
		if c.UnavailableSizeEligible != c.DepthEligible || summary.DirectoryUnavailableSizeEmittedCount != 3 ||
			!c.SelectionTruncated || c.Excluded != 0 {
			t.Fatalf("floor=%v counts=%+v summary=%+v", floor, *c, summary)
		}
	}
}

func TestDirectoryAuditPartialLowerBoundsStayHonest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	root := t.TempDir()
	writeSizedAt(t, filepath.Join(root, "hi", "seen.bin"), 5000, time.Now())
	writeSizedAt(t, filepath.Join(root, "hi", "locked", "hidden.bin"), 1, time.Now())
	writeSizedAt(t, filepath.Join(root, "lo", "seen.bin"), 100, time.Now())
	writeSizedAt(t, filepath.Join(root, "lo", "locked", "hidden.bin"), 1, time.Now())
	for _, dir := range []string{"hi/locked", "lo/locked"} {
		path := filepath.Join(root, filepath.FromSlash(dir))
		if err := os.Chmod(path, 0); err != nil {
			t.Skipf("chmod unavailable: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
	}
	opts := auditOptions(root)
	opts.DirectoryFloor = int64p(1000)
	opts.DirectoryDepth = 1
	raw, err := runAuditJSONL(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, rows, summary := decodeAudit(t, raw.Bytes())
	decisions := map[string]string{}
	for _, row := range rows {
		decisions[row.RelativePath] = row.FloorDecision
		if row.Lifecycle == LifecyclePartial && row.Accounting.Apparent.Bound != BoundLower {
			t.Fatalf("partial row %q claims %+v", row.RelativePath, row.Accounting.Apparent)
		}
	}
	if decisions["hi"] != FloorMeets || decisions["lo"] != FloorIndeterminate {
		t.Fatalf("decisions=%v", decisions)
	}
	if summary.Lifecycle != LifecyclePartial || summary.GapCount == 0 || !summary.EmissionCompleted {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestDirectoryAuditEmptyMatchIsExplicit(t *testing.T) {
	root := writeAuditFixture(t)
	opts := auditOptions(root)
	opts.DirectoryFloor = int64p(1 << 40)
	raw, err := runAuditJSONL(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, rows, summary := decodeAudit(t, raw.Bytes())
	if len(rows) != 0 || !summary.SelectionReconciled || !summary.EmissionCompleted ||
		summary.SelectedBeforeTop != 0 || summary.Lifecycle != LifecycleComplete {
		t.Fatalf("rows=%d summary=%+v", len(rows), summary)
	}
}

func TestDirectoryAuditBudgetFailureMapsTypedFailure(t *testing.T) {
	root := writeAuditFixture(t)
	opts := auditOptions(root)
	opts.MaxAggregateDirectories = 2
	raw, err := runAuditJSONL(t, opts)
	requireBudgetError(t, err, 2, 2, 3)
	_, rows, summary := decodeAudit(t, raw.Bytes())
	f := summary.Failure
	if len(rows) != 0 || summary.Lifecycle != LifecycleFailed || summary.SelectionReconciled ||
		summary.AuditSelectionCounts != nil || f == nil || f.Code != FailureAggregateBudget ||
		*f.Limit != 2 || *f.Retained != 2 || *f.AttemptedRetained != 3 {
		t.Fatalf("summary=%+v failure=%+v", summary, f)
	}
}

func TestDirectoryAuditCancellationBoundaries(t *testing.T) {
	root := writeAuditFixture(t)
	cases := []struct {
		name  string
		hooks func(cancel context.CancelFunc) *auditHooks
		check func(t *testing.T, rows []AuditDirectory, s AuditSummary)
	}{
		{"last root complete, before selection", func(cancel context.CancelFunc) *auditHooks {
			return &auditHooks{beforeSelect: cancel}
		}, func(t *testing.T, rows []AuditDirectory, s AuditSummary) {
			if !s.Canceled || s.Lifecycle != LifecyclePartial || s.SelectionReconciled ||
				s.EmissionCompleted || s.AuditSelectionCounts != nil || len(rows) != 0 {
				t.Fatalf("summary=%+v rows=%d", s, len(rows))
			}
			for _, r := range s.Roots {
				if r.Lifecycle != LifecycleComplete || r.Canceled || r.GapCount != 0 {
					t.Fatalf("completed traversal rewritten: %+v", r)
				}
			}
		}},
		{"during emitted prefix", func(cancel context.CancelFunc) *auditHooks {
			return &auditHooks{beforeRow: func(i int) {
				if i == 2 {
					cancel()
				}
			}}
		}, func(t *testing.T, rows []AuditDirectory, s AuditSummary) {
			if !s.Canceled || s.Lifecycle != LifecyclePartial || !s.SelectionReconciled ||
				s.EmissionCompleted || len(rows) != 2 || s.DirectoryEmittedCount != 2 ||
				s.PlannedEmission <= 2 || s.SelectionTruncated {
				t.Fatalf("summary=%+v rows=%d", s, len(rows))
			}
		}},
		{"after emission commit", func(cancel context.CancelFunc) *auditHooks {
			return &auditHooks{afterCommit: cancel}
		}, func(t *testing.T, rows []AuditDirectory, s AuditSummary) {
			if s.Canceled || s.Lifecycle != LifecycleComplete || !s.EmissionCompleted ||
				int64(len(rows)) != s.PlannedEmission {
				t.Fatalf("summary=%+v rows=%d", s, len(rows))
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts := auditOptions(root)
			opts.auditHooks = tc.hooks(cancel)
			var stream bytes.Buffer
			summary, _ := Run(ctx, opts, NewJSONLSink(&stream))
			requireValidAuditStream(t, stream.Bytes())
			_, rows, audit := decodeAudit(t, stream.Bytes())
			if summary.Lifecycle != audit.Lifecycle {
				t.Fatalf("returned lifecycle %q, stream %q", summary.Lifecycle, audit.Lifecycle)
			}
			tc.check(t, rows, audit)
		})
	}
}

func TestDirectoryAuditRejectsFilePredicatesAndFloorOutsideMode(t *testing.T) {
	root := t.TempDir()
	bad := []Options{
		{Roots: []string{root}, EmissionMode: EmissionDirectoryAudit, MaxAggregateDirectories: 1, MinSize: 1},
		{Roots: []string{root}, EmissionMode: EmissionDirectoryAudit, MaxAggregateDirectories: 1, Top: 1},
		{Roots: []string{root}, EmissionMode: EmissionDirectoryAudit, MaxAggregateDirectories: 1, DirectoryAccounting: true},
		{Roots: []string{root}, EmissionMode: EmissionDirectorySummary, MaxAggregateDirectories: 1, DirectoryFloor: int64p(1)},
		{Roots: []string{root}, EmissionMode: EmissionDirectoryAudit, MaxAggregateDirectories: 1, DirectoryFloor: int64p(-1)},
	}
	for i, opts := range bad {
		if _, err := Run(context.Background(), opts, &auditMemorySink{}); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

// Schema negative controls mutate a valid record and require rejection.
func TestDirectoryAuditSchemaRejectsMalformedRecords(t *testing.T) {
	root := writeAuditFixture(t)
	opts := auditOptions(root)
	opts.DirectoryFloor = int64p(3000)
	raw, err := runAuditJSONL(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw.Bytes()), []byte("\n"))
	header, directory, summary := lines[0], lines[1], lines[len(lines)-1]
	mutate := func(line []byte, edit func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	data := func(m map[string]any) map[string]any { return m["data"].(map[string]any) }
	cases := map[string][]byte{
		"missing floor decision": mutate(directory, func(m map[string]any) { delete(data(m), "floor_decision") }),
		"excluded decision":      mutate(directory, func(m map[string]any) { data(m)["floor_decision"] = "excluded" }),
		"swapped apparent basis": mutate(directory, func(m map[string]any) {
			data(m)["accounting"].(map[string]any)["apparent"].(map[string]any)["basis"] = BasisAllocatedPathEntrySum
		}),
		"unsupported with bytes": mutate(directory, func(m map[string]any) {
			data(m)["accounting"].(map[string]any)["unique_physical"].(map[string]any)["bytes"] = 1
		}),
		"old record version with new field": mutate(directory, func(m map[string]any) { m["type"] = RecordDirectory }),
		"floor bytes without floor": mutate(header, func(m map[string]any) {
			data(m)["directory_selection"].(map[string]any)["floor_applied"] = false
		}),
		"applied floor missing bytes": mutate(header, func(m map[string]any) {
			delete(data(m)["directory_selection"].(map[string]any), "floor_bytes")
		}),
		"reconciled missing partition": mutate(summary, func(m map[string]any) { delete(data(m), "directory_meets_count") }),
		"unreconciled with partition":  mutate(summary, func(m map[string]any) { data(m)["selection_reconciled"] = false }),
		"complete with failure": mutate(summary, func(m map[string]any) {
			data(m)["failure"] = map[string]any{"code": "traversal_failed"}
		}),
		"budget members on other failure": mutate(summary, func(m map[string]any) {
			d := data(m)
			d["lifecycle"], d["emission_completed"] = "failed", false
			d["failure"] = map[string]any{"code": "traversal_failed", "limit": 1}
		}),
		"budget failure missing member": mutate(summary, func(m map[string]any) {
			d := data(m)
			d["lifecycle"], d["emission_completed"] = "failed", false
			d["failure"] = map[string]any{"code": FailureAggregateBudget, "limit": 1, "retained": 1}
		}),
		"canceled but complete": mutate(summary, func(m map[string]any) { data(m)["canceled"] = true }),
		"negative count":        mutate(summary, func(m map[string]any) { data(m)["directory_emitted_count"] = -1 }),
		"float count":           mutate(summary, func(m map[string]any) { data(m)["directory_emitted_count"] = 1.5 }),
	}
	// An out-of-range integer cannot survive a float64 round trip, so write it
	// textually.
	cases["integer above int64"] = bytes.Replace(summary,
		[]byte(`"directory_emitted_count":`), []byte(`"directory_emitted_count":9223372036854775808,"x":`), 1)
	for name, line := range cases {
		if err := contract.ValidateJSON(spanwitschema.SpanwitFilesystemInventoryAggregationV2, line); err == nil {
			t.Errorf("%s: schema accepted %s", name, line)
		}
	}
}

// Semantic negative controls: every line is schema-valid, the stream is not.
func TestDirectoryAuditStreamValidatorRejectsCrossRecordFaults(t *testing.T) {
	root := writeAuditFixture(t)
	opts := auditOptions(root)
	opts.DirectoryFloor = int64p(3000)
	raw, err := runAuditJSONL(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw.Bytes()), []byte("\n"))
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, []byte("\n")) }
	reseq := func(parts [][]byte) []byte {
		out := make([][]byte, len(parts))
		for i, line := range parts {
			var m map[string]any
			if err := json.Unmarshal(line, &m); err != nil {
				t.Fatal(err)
			}
			m["seq"] = i
			out[i], _ = json.Marshal(m)
		}
		return join(out...)
	}
	edit := func(line []byte, fn func(m map[string]any)) []byte {
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		fn(m)
		out, _ := json.Marshal(m)
		return out
	}
	data := func(m map[string]any) map[string]any { return m["data"].(map[string]any) }
	last := len(lines) - 1
	withSummary := func(fn func(m map[string]any)) []byte {
		parts := append([][]byte{}, lines[:last]...)
		return join(append(parts, edit(lines[last], fn))...)
	}
	cases := map[string][]byte{
		"truncated final record":  join(lines[:last]...),
		"record after summary":    join(append(append([][]byte{}, lines...), lines[1])...),
		"sequence gap":            join(append([][]byte{lines[0]}, lines[2:]...)...),
		"duplicate header":        reseq(append([][]byte{lines[0], lines[0]}, lines[1:]...)),
		"duplicate directory":     reseq(append(append([][]byte{}, lines[:2]...), lines[1:]...)),
		"mixed run ids":           join(append(append([][]byte{}, lines[:1]...), append([][]byte{edit(lines[1], func(m map[string]any) { m["run_id"] = "other" })}, lines[2:]...)...)...),
		"unknown root reference":  join(append(append([][]byte{}, lines[:1]...), append([][]byte{edit(lines[1], func(m map[string]any) { data(m)["root_id"] = "root-9" })}, lines[2:]...)...)...),
		"emitted count mismatch":  withSummary(func(m map[string]any) { data(m)["directory_emitted_count"] = json.Number("99") }),
		"partition violates E":    withSummary(func(m map[string]any) { data(m)["directory_meets_count"] = json.Number("0") }),
		"truncated without K < S": withSummary(func(m map[string]any) { data(m)["directory_selection_truncated"] = true }),
	}
	for name, stream := range cases {
		if err := ValidateAuditStream(bytes.NewReader(stream)); err == nil {
			t.Errorf("%s: stream validator accepted", name)
		}
	}
}

func TestDirectoryAuditStreamValidatorIsLosslessAbove2Pow53(t *testing.T) {
	// 2^53 + 1 is not representable as float64; a float decode would round it
	// and break retained = limit.
	const big = "9007199254740993"
	stream := strings.Join([]string{
		`{"type":"spanwit.inventory.header.v2","run_id":"r","seq":0,"ts":"2026-09-30T00:00:00Z","data":{"profile":"spanwit.filesystem-inventory-aggregation/v2","roots":[{"id":"root-1","local_path":"/x"}],"backend":"serial","workers":1,"max_open_dirs":1,"max_pending_dirs":1,"one_filesystem":false,"emission_mode":"directory_audit","max_aggregate_directories":` + big + `,"subject":{"accounting_scope":"full_subject","entry_type":"file","symlink_posture":"not_followed"},"directory_selection":{"floor_applied":false,"size_basis":"apparent","depth":-1,"top":0},"exclusions":[],"ordering":"directory_audit_rank_v1","path_protection":"source_structure:block_export","mutation_contract":"open"}}`,
		`{"type":"spanwit.inventory.summary.v2","run_id":"r","seq":1,"ts":"2026-09-30T00:00:00Z","data":{"lifecycle":"failed","canceled":false,"selection_reconciled":false,"emission_completed":false,"directory_emitted_count":0,"directory_indeterminate_emitted_count":0,"directory_unavailable_size_emitted_count":0,"failure":{"code":"aggregate_directory_budget_exhausted","limit":` + big + `,"retained":` + big + `,"attempted_retained":9007199254740994},"backend":"serial","visited_entries":0,"visited_directories":0,"gap_count":0,"boundary_skip_count":0,"vanished_count":0,"queue_limit_skip_count":0,"exclusion_count":0,"peak_depth":0,"peak_pending_directories":0,"duration_nanos":0,"terminal_observed_at":"2026-09-30T00:00:00Z","roots":[{"root_id":"root-1","lifecycle":"failed","visited_entries":0,"visited_directories":0,"gap_count":0,"boundary_skip_count":0,"exclusion_count":0,"vanished_count":0,"queue_limit_skip_count":0,"canceled":false,"failed":true}]}}`,
	}, "\n")
	requireValidAuditStream(t, []byte(stream))
	broken := strings.Replace(stream, `"attempted_retained":9007199254740994`, `"attempted_retained":9007199254740995`, 1)
	if err := ValidateAuditStream(strings.NewReader(broken)); err == nil {
		t.Fatal("validator accepted attempted_retained != retained + 1 above 2^53")
	}
}

// The committed synthetic example streams are the documented v2 examples; each
// must pass both the schema and the stream validator.
func TestDirectoryAuditExampleStreamsValidate(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "audit-v2", "*.jsonl"))
	if err != nil || len(paths) < 3 {
		t.Fatalf("examples=%v err=%v", paths, err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(path), func(t *testing.T) { requireValidAuditStream(t, raw) })
	}
}

// Regression: worker shutdown after a fatal budget error is internal, so no
// root may be reported canceled.
func TestDirectoryAuditBudgetFailureDoesNotMarkRootsCanceled(t *testing.T) {
	root := writeAuditFixture(t)
	opts := auditOptions(root)
	opts.MaxAggregateDirectories = 2
	raw, _ := runAuditJSONL(t, opts)
	_, _, summary := decodeAudit(t, raw.Bytes())
	for _, r := range summary.Roots {
		if r.Canceled {
			t.Fatalf("internal cancellation leaked into root evidence: %+v", r)
		}
	}
	if summary.Canceled {
		t.Fatalf("summary canceled on a fatal failure: %+v", summary)
	}
}

// Regression: false-success and misordered streams pass the per-line schema
// only if the stream validator also rejects them.
func TestDirectoryAuditValidatorRejectsLifecycleAndOrderFaults(t *testing.T) {
	root := writeAuditFixture(t)
	opts := auditOptions(root)
	opts.DirectoryFloor = int64p(3000)
	raw, err := runAuditJSONL(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw.Bytes()), []byte("\n"))
	last := len(lines) - 1
	edit := func(line []byte, fn func(d map[string]any)) []byte {
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		fn(m["data"].(map[string]any))
		out, _ := json.Marshal(m)
		return out
	}
	withSummary := func(fn func(d map[string]any)) []byte {
		parts := append([][]byte{}, lines[:last]...)
		return bytes.Join(append(parts, edit(lines[last], fn)), []byte("\n"))
	}
	// Find two adjacent rows with equal rank (the 3000-byte tie) and swap them.
	var tieA, tieB int
	for i := 1; i < last-1; i++ {
		if bytes.Contains(lines[i], []byte(`"relative_path":"tie1"`)) &&
			bytes.Contains(lines[i+1], []byte(`"relative_path":"tie2"`)) {
			tieA, tieB = i, i+1
		}
	}
	if tieA == 0 {
		t.Fatalf("fixture tie rows not adjacent:\n%s", raw.String())
	}
	swapped := append([][]byte{}, lines...)
	swapped[tieA], swapped[tieB] = edit(lines[tieB], func(d map[string]any) {}), edit(lines[tieA], func(d map[string]any) {})
	for _, i := range []int{tieA, tieB} {
		var m map[string]any
		_ = json.Unmarshal(swapped[i], &m)
		m["seq"] = i
		swapped[i], _ = json.Marshal(m)
	}
	cases := map[string][]byte{
		"complete with gaps":             withSummary(func(d map[string]any) { d["gap_count"] = json.Number("3") }),
		"partial without gaps or cancel": withSummary(func(d map[string]any) { d["lifecycle"] = "partial" }),
		"tied rows out of path order":    bytes.Join(swapped, []byte("\n")),
		"duplicate summary root": withSummary(func(d map[string]any) {
			roots := d["roots"].([]any)
			d["roots"] = append(roots, roots[0])
		}),
	}
	for name, stream := range cases {
		if err := ValidateAuditStream(bytes.NewReader(stream)); err == nil {
			t.Errorf("%s: stream validator accepted", name)
		}
	}
	// The schema itself now rejects a complete result that is not reconciled.
	notReconciled := edit(lines[last], func(d map[string]any) {
		d["selection_reconciled"] = false
		for _, k := range []string{"directory_depth_eligible_count", "directory_meets_count",
			"directory_indeterminate_count", "directory_excluded_count", "directory_not_applied_count",
			"directory_selected_before_top_count", "directory_planned_emission_count",
			"directory_unavailable_size_eligible_count", "directory_selection_truncated"} {
			delete(d, k)
		}
		d["emission_completed"] = false
	})
	if err := contract.ValidateJSON(spanwitschema.SpanwitFilesystemInventoryAggregationV2, notReconciled); err == nil {
		t.Error("schema accepted lifecycle complete with an unreconciled selection")
	}
}

// Regression: gap records must reconcile with summary gap counts and
// per-root evidence; a complete audit cannot hide an affecting gap or an
// incomplete root.
func TestDirectoryAuditValidatorReconcilesGapsAndRootEvidence(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "audit-v2", "complete.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	last := len(lines) - 1
	edit := func(line []byte, fn func(m map[string]any)) []byte {
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		fn(m)
		out, _ := json.Marshal(m)
		return out
	}
	data := func(m map[string]any) map[string]any { return m["data"].(map[string]any) }
	root0 := func(m map[string]any) map[string]any {
		return data(m)["roots"].([]any)[0].(map[string]any)
	}
	gap := []byte(`{"type":"spanwit.inventory.gap.v1","run_id":"example","seq":` + fmt.Sprint(last) +
		`,"ts":"2026-09-30T12:00:00Z","data":{"root_id":"root-1","kind":"permission","affects_completeness":true}}`)
	withGap := func(fn func(m map[string]any)) []byte {
		parts := append([][]byte{}, lines[:last]...)
		parts = append(parts, gap, edit(lines[last], func(m map[string]any) {
			m["seq"] = json.Number(fmt.Sprint(last + 1))
			fn(m)
		}))
		return bytes.Join(parts, []byte("\n"))
	}
	withSummary := func(fn func(m map[string]any)) []byte {
		parts := append([][]byte{}, lines[:last]...)
		return bytes.Join(append(parts, edit(lines[last], fn)), []byte("\n"))
	}
	cases := map[string][]byte{
		"hidden affecting gap under complete": withGap(func(m map[string]any) {}),
		"global gap count disagrees with records": withGap(func(m map[string]any) {
			d := data(m)
			d["lifecycle"] = "partial"
			root0(m)["gap_count"], root0(m)["lifecycle"] = json.Number("1"), "partial"
		}),
		"root gap count disagrees with records": withGap(func(m map[string]any) {
			d := data(m)
			d["lifecycle"], d["gap_count"] = "partial", json.Number("1")
		}),
		"complete audit hides partial root": withSummary(func(m map[string]any) {
			root0(m)["lifecycle"] = "partial"
		}),
		"complete audit hides canceled root": withSummary(func(m map[string]any) {
			root0(m)["canceled"] = true
		}),
		"complete audit hides failed root": withSummary(func(m map[string]any) {
			root0(m)["lifecycle"], root0(m)["failed"] = "failed", true
		}),
	}
	for name, stream := range cases {
		if err := ValidateAuditStream(bytes.NewReader(stream)); err == nil {
			t.Errorf("%s: stream validator accepted", name)
		}
	}
	// A consistent partial result with the gap reconciled everywhere is valid.
	consistent := withGap(func(m map[string]any) {
		d := data(m)
		d["lifecycle"], d["gap_count"] = "partial", json.Number("1")
		root0(m)["gap_count"], root0(m)["lifecycle"] = json.Number("1"), "partial"
	})
	if err := ValidateAuditStream(bytes.NewReader(consistent)); err != nil {
		t.Fatalf("consistent partial stream rejected: %v", err)
	}
}

// Regression: emitted floor categories reconcile with M/I/U,
// and the budget failure limit must match the declared header budget.
func TestDirectoryAuditValidatorReconcilesCategoriesAndBudgetLimit(t *testing.T) {
	read := func(name string) [][]byte {
		raw, err := os.ReadFile(filepath.Join("testdata", "audit-v2", name))
		if err != nil {
			t.Fatal(err)
		}
		return bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	}
	edit := func(line []byte, fn func(d map[string]any)) []byte {
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		fn(m["data"].(map[string]any))
		out, _ := json.Marshal(m)
		return out
	}
	complete := read("complete.jsonl")
	last := len(complete) - 1
	swapped := append(append([][]byte{}, complete[:last]...), edit(complete[last], func(d map[string]any) {
		d["directory_meets_count"], d["directory_indeterminate_count"] = json.Number("0"), json.Number("3")
	}))
	budget := read("budget-failure.jsonl")
	limitMismatch := append([][]byte{edit(budget[0], func(d map[string]any) {
		d["max_aggregate_directories"] = json.Number("249999")
	})}, budget[1:]...)
	for name, lines := range map[string][][]byte{
		"meets/indeterminate swap under full emission": swapped,
		"budget limit differs from header":             limitMismatch,
	} {
		if err := ValidateAuditStream(bytes.NewReader(bytes.Join(lines, []byte("\n")))); err == nil {
			t.Errorf("%s: stream validator accepted", name)
		}
	}

	// Positive controls: a real top-K truncation with mixed categories and a
	// real prefix cancellation must remain valid (category counts are bounded,
	// not required to be equal, when D < S).
	root := writeAuditFixture(t)
	original := fileMetadataOf
	fileMetadataOf = func(info fs.FileInfo) fileMetadata {
		meta := original(info)
		if strings.HasPrefix(info.Name(), "d") || strings.HasPrefix(info.Name(), "e") {
			meta.allocated = nil
		}
		return meta
	}
	t.Cleanup(func() { fileMetadataOf = original })
	opts := auditOptions(root)
	// Fixture files are sparse (allocated ~0), so a zero floor gives measured
	// rows "meets" and unmeasured rows "indeterminate".
	opts.SizeBasis, opts.DirectoryFloor, opts.DirectoryTop = SizeAllocated, int64p(0), 3
	raw, err := runAuditJSONL(t, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, _, summary := decodeAudit(t, raw.Bytes())
	if !summary.SelectionTruncated || summary.Indeterminate == 0 || summary.Meets == 0 {
		t.Fatalf("positive control lacks mixed truncated categories: %+v", *summary.AuditSelectionCounts)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prefix := auditOptions(root)
	prefix.SizeBasis, prefix.DirectoryFloor = SizeAllocated, int64p(0)
	prefix.auditHooks = &auditHooks{beforeRow: func(i int) {
		if i == 2 {
			cancel()
		}
	}}
	var stream bytes.Buffer
	if _, err := Run(ctx, prefix, NewJSONLSink(&stream)); err != nil {
		t.Fatal(err)
	}
	requireValidAuditStream(t, stream.Bytes())
}

// Regression: root causes, traversal counters, the
// unavailable-size cross-count, row evidence and canonical relative paths.
func TestDirectoryAuditValidatorRejectsInconsistentEvidenceAndPaths(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "audit-v2", "complete.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	last := len(lines) - 1
	edit := func(index int, fn func(d map[string]any)) []byte {
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(lines[index]))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		fn(m["data"].(map[string]any))
		out, _ := json.Marshal(m)
		return out
	}
	with := func(edits map[int]func(d map[string]any)) []byte {
		parts := make([][]byte, len(lines))
		for i := range lines {
			parts[i] = lines[i]
			if fn, ok := edits[i]; ok {
				parts[i] = edit(i, fn)
			}
		}
		return bytes.Join(parts, []byte("\n"))
	}
	root0 := func(d map[string]any) map[string]any { return d["roots"].([]any)[0].(map[string]any) }
	claim := func(d map[string]any, plane string) map[string]any {
		return d["accounting"].(map[string]any)[plane].(map[string]any)
	}
	path := func(p string) map[int]func(d map[string]any) {
		return map[int]func(d map[string]any){2: func(d map[string]any) { d["relative_path"] = p }}
	}
	canceledAudit := func(rootCanceled bool) func(d map[string]any) {
		return func(d map[string]any) {
			d["lifecycle"], d["canceled"], d["emission_completed"] = "partial", true, false
			root0(d)["lifecycle"], root0(d)["canceled"] = "partial", rootCanceled
		}
	}
	lowerRow := func(allocatedExact bool) func(d map[string]any) {
		return func(d map[string]any) {
			d["lifecycle"], d["affecting_gap_count"] = "partial", json.Number("1")
			claim(d, "apparent")["status"], claim(d, "apparent")["bound"] = "partial", "lower"
			if !allocatedExact {
				claim(d, "allocated")["status"], claim(d, "allocated")["bound"] = "partial", "lower"
			}
		}
	}
	unmeasured := func(row, typed int) func(d map[string]any) {
		return func(d map[string]any) {
			d["allocated_unmeasured_count"] = json.Number(fmt.Sprint(row))
			a := claim(d, "allocated")
			a["status"], a["bound"], a["unmeasured_count"] = "partial", "lower", json.Number(fmt.Sprint(typed))
		}
	}
	budgetRaw, err := os.ReadFile(filepath.Join("testdata", "audit-v2", "budget-failure.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	withBudget := func(fn func(d map[string]any)) []byte {
		budget := bytes.Split(bytes.TrimSpace(budgetRaw), []byte("\n"))
		end := len(budget) - 1
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(budget[end]))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		fn(m["data"].(map[string]any))
		budget[end], _ = json.Marshal(m)
		return bytes.Join(budget, []byte("\n"))
	}
	cases := []struct {
		name   string
		stream []byte
		want   string
	}{
		{"partial root without its own cause", with(map[int]func(d map[string]any){last: canceledAudit(false)}),
			"partial without affecting gaps or cancellation"},
		{"global boundary counter without root evidence", with(map[int]func(d map[string]any){last: func(d map[string]any) {
			d["boundary_skip_count"] = json.Number("1")
		}}), "traversal counters"},
		{"root exclusion counter without global", with(map[int]func(d map[string]any){last: func(d map[string]any) {
			root0(d)["exclusion_count"] = json.Number("1")
		}}), "traversal counters"},
		{"negative root counter", with(map[int]func(d map[string]any){last: func(d map[string]any) {
			d["vanished_count"], root0(d)["vanished_count"] = json.Number("-1"), json.Number("-1")
		}}), "negative or overflow"},
		{"unavailable-size eligible beyond I + U", with(map[int]func(d map[string]any){last: func(d map[string]any) {
			d["directory_unavailable_size_eligible_count"] = json.Number("1")
		}}), "unavailable-size counts"},
		{"partial row without affecting gaps", with(map[int]func(d map[string]any){3: func(d map[string]any) {
			d["lifecycle"] = "partial"
		}}), "partial without affecting gaps"},
		{"complete row with affecting gaps", with(map[int]func(d map[string]any){3: func(d map[string]any) {
			d["affecting_gap_count"] = json.Number("1")
		}}), "complete but carries gaps"},
		{"complete row with lower apparent bound", with(map[int]func(d map[string]any){3: func(d map[string]any) {
			claim(d, "apparent")["status"], claim(d, "apparent")["bound"] = "partial", "lower"
		}}), "complete but carries gaps"},
		{"partial row with exact allocated claim", with(map[int]func(d map[string]any){3: lowerRow(true)}),
			"exact allocated claim"},
		{"complete row lower allocated with nothing unmeasured", with(map[int]func(d map[string]any){3: func(d map[string]any) {
			claim(d, "allocated")["status"], claim(d, "allocated")["bound"] = "partial", "lower"
		}}), "omits its unmeasured count"},
		{"unmeasured count above file count", with(map[int]func(d map[string]any){2: unmeasured(2000, 2000)}),
			"outside 0..file_count"},
		{"claim unmeasured count disagrees with row", with(map[int]func(d map[string]any){2: unmeasured(1, 2)}),
			"does not reconcile"},
		{"every file unmeasured reported as partial", with(map[int]func(d map[string]any){2: unmeasured(2, 2)}),
			"does not reconcile"},
		{"unsupported allocated with measured files", with(map[int]func(d map[string]any){2: func(d map[string]any) {
			d["allocated_unmeasured_count"] = json.Number("1")
			d["accounting"].(map[string]any)["allocated"] = map[string]any{"status": "unsupported", "detail": "none"}
		}}), "every covered file unmeasured"},
		{"failed root also canceled", withBudget(func(d map[string]any) { root0(d)["canceled"] = true }),
			"root failed or the audit is not canceled"},
		{"canceled root under an uncanceled audit", with(map[int]func(d map[string]any){last: func(d map[string]any) {
			d["lifecycle"] = "partial"
			root0(d)["lifecycle"], root0(d)["canceled"] = "partial", true
		}}), "audit is not canceled"},
		{"partial row under a gap-free summary", with(map[int]func(d map[string]any){3: lowerRow(false)}),
			"claim 1 affecting gaps"},
		{"absolute relative path", with(path("/vm")), "not canonical"},
		{"escaping relative path", with(path("../vm")), "not canonical"},
		{"repeated slash", with(path("vm//x")), "not canonical"},
		{"dot segment", with(path("./vm")), "not canonical"},
		{"trailing dot-dot", with(path("vm/..")), "not canonical"},
		{"empty relative path", with(path("")), "not canonical"},
		{"NUL in relative path", with(path("v\x00m")), "not canonical"},
	}
	for _, tc := range cases {
		err := ValidateAuditStream(bytes.NewReader(tc.stream))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", tc.name, err, tc.want)
		}
	}

	// Positive controls: a canceled root is its own cause; allocation
	// uncertainty on a fully traversed row is legitimate; a literal backslash
	// is an ordinary POSIX filename byte.
	for name, stream := range map[string][]byte{
		"canceled root": with(map[int]func(d map[string]any){last: canceledAudit(true)}),
		// The last row moves to the definite-lower group, so ordering holds.
		"complete row with unmeasured allocation": with(map[int]func(d map[string]any){3: func(d map[string]any) {
			d["file_count"] = json.Number("2")
			unmeasured(1, 1)(d)
		}}),
		"literal backslash": with(path(`vm\images`)),
	} {
		t.Run(name, func(t *testing.T) { requireValidAuditStream(t, stream) })
	}
}

// A real directory whose name contains a backslash keeps it verbatim in the
// slash-delimited identity and validates.
func TestDirectoryAuditKeepsLiteralBackslashInRelativePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a path separator on windows")
	}
	root := t.TempDir()
	writeSizedAt(t, filepath.Join(root, `a\b`, "c.bin"), 10, time.Now())
	raw, err := runAuditJSONL(t, auditOptions(root))
	if err != nil {
		t.Fatal(err)
	}
	_, rows, _ := decodeAudit(t, raw.Bytes())
	for _, row := range rows {
		if row.RelativePath == `a\b` && row.Depth == 1 {
			return
		}
	}
	t.Fatalf("no row for literal backslash directory: %+v", rows)
}
