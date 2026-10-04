package inventory_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/inventory"
)

func TestAggregationProfileRunValidatesStrictSchema(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "file.bin"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	if _, err := inventory.Run(context.Background(), inventory.Options{
		Roots: []string{root}, Backend: inventory.BackendSerial, Workers: 1,
		EmissionMode: inventory.EmissionDirectorySummary, DirectoryDepth: -1,
		MaxAggregateDirectories: 10,
	}, inventory.NewJSONLSink(&stream)); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(&stream)
	records := 0
	for scanner.Scan() {
		records++
		if err := contract.ValidateJSON(
			spanwitschema.SpanwitFilesystemInventoryAggregationV0,
			append([]byte(nil), scanner.Bytes()...),
		); err != nil {
			t.Fatalf("record %d: %v\n%s", records, err, scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if records != 4 {
		t.Fatalf("records=%d want=4 (header, two directories, summary)", records)
	}
}

func TestAggregationProfileAccountingRunValidatesStrictSchema(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "file.bin"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	if _, err := inventory.Run(context.Background(), inventory.Options{
		Roots: []string{root}, Backend: inventory.BackendSerial, Workers: 1,
		EmissionMode: inventory.EmissionDirectorySummary, DirectoryDepth: -1,
		MaxAggregateDirectories: 10, DirectoryAccounting: true,
	}, inventory.NewJSONLSink(&stream)); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(&stream)
	records := 0
	sawAccounting := false
	for scanner.Scan() {
		records++
		if err := contract.ValidateJSON(
			spanwitschema.SpanwitFilesystemInventoryAggregationV1,
			append([]byte(nil), scanner.Bytes()...),
		); err != nil {
			t.Fatalf("record %d: %v\n%s", records, err, scanner.Text())
		}
		if !strings.Contains(scanner.Text(), `"accounting"`) {
			continue
		}
		var record struct {
			Type string `json:"type"`
			Data struct {
				Accounting struct {
					Apparent struct {
						Status string `json:"status"`
						Bound  string `json:"bound"`
					} `json:"apparent"`
					ExpectedReclaim struct {
						Status string `json:"status"`
					} `json:"expected_reclaim"`
				} `json:"accounting"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.Type != "spanwit.inventory.directory.v1" {
			t.Fatalf("accounting on unexpected record %s", record.Type)
		}
		if record.Data.Accounting.Apparent.Status != "measured" || record.Data.Accounting.Apparent.Bound != "exact" {
			t.Fatalf("apparent claim=%+v", record.Data.Accounting.Apparent)
		}
		if record.Data.Accounting.ExpectedReclaim.Status != "unsupported" {
			t.Fatalf("expected_reclaim must be unsupported: %+v", record.Data.Accounting.ExpectedReclaim)
		}
		sawAccounting = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawAccounting {
		t.Fatal("no directory record carried accounting claims")
	}
}

func TestAggregationProfileAccountingUnsupportedClaimShape(t *testing.T) {
	valid := `{"type":"spanwit.inventory.directory.v1","run_id":"r","seq":1,"ts":"2026-09-03T12:00:00Z","data":{"root_id":"root-1","relative_path":".","depth":0,"apparent_bytes_sum":10,"allocated_unmeasured_count":1,"file_count":1,"descendant_directory_count":0,"lifecycle":"complete","affecting_gap_count":0,"accounting":{"apparent":{"status":"measured","basis":"path_entry_logical_size_sum","bound":"exact","bytes":10},"allocated":{"status":"unsupported","detail":"the allocated sum is unavailable because allocation was not observed for every covered file"},"unique_physical":{"status":"unsupported","detail":"x"},"shared_cloned":{"status":"unsupported","detail":"x"},"expected_reclaim":{"status":"unsupported","detail":"x"}}}}`
	invalid := `{"type":"spanwit.inventory.directory.v1","run_id":"r","seq":1,"ts":"2026-09-03T12:00:00Z","data":{"root_id":"root-1","relative_path":".","depth":0,"apparent_bytes_sum":10,"allocated_unmeasured_count":1,"file_count":1,"descendant_directory_count":0,"lifecycle":"complete","affecting_gap_count":0,"accounting":{"apparent":{"status":"measured","basis":"path_entry_logical_size_sum","bound":"exact","bytes":10},"allocated":{"status":"unsupported","detail":"x","bytes":5},"unique_physical":{"status":"unsupported","detail":"x"},"shared_cloned":{"status":"unsupported","detail":"x"},"expected_reclaim":{"status":"unsupported","detail":"x"}}}}`
	mixed := `{"type":"spanwit.inventory.directory.v1","run_id":"r","seq":1,"ts":"2026-09-03T12:00:00Z","data":{"root_id":"root-1","relative_path":".","depth":0,"apparent_bytes_sum":110,"allocated_unmeasured_count":1,"file_count":2,"descendant_directory_count":0,"lifecycle":"complete","affecting_gap_count":0,"accounting":{"apparent":{"status":"measured","basis":"path_entry_logical_size_sum","bound":"exact","bytes":110},"allocated":{"status":"partial","basis":"path_entry_allocated_blocks_sum","bound":"lower","bytes":100,"unmeasured_count":1},"unique_physical":{"status":"unsupported","detail":"x"},"shared_cloned":{"status":"unsupported","detail":"x"},"expected_reclaim":{"status":"unsupported","detail":"x"}}}}`
	if err := contract.ValidateJSON(spanwitschema.SpanwitFilesystemInventoryAggregationV1, []byte(valid)); err != nil {
		t.Fatalf("zero-measured allocated record must validate: %v\n%s", err, valid)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitFilesystemInventoryAggregationV1, []byte(invalid)); err == nil {
		t.Fatalf("unsupported claim with bytes must be rejected:\n%s", invalid)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitFilesystemInventoryAggregationV1, []byte(mixed)); err != nil {
		t.Fatalf("mixed partial/lower allocated record must validate: %v\n%s", err, mixed)
	}
}

func TestAggregationProfileAccountingRejectsInvalidStatusBoundPairs(t *testing.T) {
	template := `{"type":"spanwit.inventory.directory.v1","run_id":"r","seq":1,"ts":"2026-09-03T12:00:00Z","data":{"root_id":"root-1","relative_path":".","depth":0,"apparent_bytes_sum":10,"allocated_unmeasured_count":0,"file_count":1,"descendant_directory_count":0,"lifecycle":"complete","affecting_gap_count":0,"accounting":{"apparent":%s,"allocated":{"status":"measured","basis":"path_entry_allocated_blocks_sum","bound":"exact","bytes":5},"unique_physical":{"status":"unsupported","detail":"x"},"shared_cloned":{"status":"unsupported","detail":"x"},"expected_reclaim":{"status":"unsupported","detail":"x"}}}}`
	cases := []string{
		`{"status":"partial","basis":"path_entry_logical_size_sum","bound":"exact","bytes":10}`,
		`{"status":"measured","basis":"path_entry_logical_size_sum","bound":"lower","bytes":10}`,
		`{"status":"measured","basis":"path_entry_logical_size_sum","bound":"exact","bytes":10,"unmeasured_count":1}`,
	}
	for i, apparent := range cases {
		record := strings.Replace(template, `"apparent":{"status":"measured","basis":"path_entry_logical_size_sum","bound":"exact","bytes":10}`, `"apparent":`+apparent, 1)
		if err := contract.ValidateJSON(spanwitschema.SpanwitFilesystemInventoryAggregationV1, []byte(record)); err == nil {
			t.Fatalf("case %d must be rejected:\n%s", i, record)
		}
	}
}

func TestAggregationProfileFailedSummaryValidatesStrictSchema(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	if _, err := inventory.Run(context.Background(), inventory.Options{
		Roots: []string{root}, Backend: inventory.BackendSerial, Workers: 1,
		EmissionMode: inventory.EmissionDirectorySummary, DirectoryDepth: -1,
		MaxAggregateDirectories: 1,
	}, inventory.NewJSONLSink(&stream)); err == nil {
		t.Fatal("expected aggregate budget failure")
	}
	scanner := bufio.NewScanner(&stream)
	records := 0
	for scanner.Scan() {
		records++
		if err := contract.ValidateJSON(
			spanwitschema.SpanwitFilesystemInventoryAggregationV0,
			append([]byte(nil), scanner.Bytes()...),
		); err != nil {
			t.Fatalf("record %d: %v\n%s", records, err, scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if records != 2 {
		t.Fatalf("records=%d want=2 (header and failed summary)", records)
	}
}

func TestProfileGoldensValidateStrictSchema(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "jsonl", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no inventory profile goldens found")
	}
	for _, name := range files {
		name := name
		t.Run(filepath.Base(name), func(t *testing.T) {
			file, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			scanner := bufio.NewScanner(file)
			line := 0
			for scanner.Scan() {
				line++
				if err := contract.ValidateJSON(
					spanwitschema.SpanwitFilesystemInventoryV0,
					append([]byte(nil), scanner.Bytes()...),
				); err != nil {
					t.Fatalf("record %d: %v\n%s", line, err, scanner.Text())
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyHeaderWithoutMutationContractStillValidates(t *testing.T) {
	// The read-only assertion keeps mutation_contract optional under unchanged header.v1 / v0
	// identity so historical streams remain accepted by the replacement validator.
	path := filepath.Join("testdata", "jsonl-compat", "legacy-header-without-mutation-contract.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitFilesystemInventoryV0, raw); err != nil {
		t.Fatalf("header without mutation_contract must remain valid: %v\n%s", err, raw)
	}
}

func TestProfileSchemaRejectsUnknownAndIncompleteRecords(t *testing.T) {
	valid := map[string]any{
		"type":   "spanwit.inventory.gap.v1",
		"run_id": "run-1",
		"seq":    float64(1),
		"ts":     "2026-08-02T20:00:00Z",
		"data": map[string]any{
			"root_id": "root-1", "kind": "permission",
			"affects_completeness": true,
		},
	}
	marshal := func(value map[string]any) []byte {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if err := contract.ValidateJSON(
		spanwitschema.SpanwitFilesystemInventoryV0, marshal(valid)); err != nil {
		t.Fatal(err)
	}

	valid["unexpected"] = true
	if err := contract.ValidateJSON(
		spanwitschema.SpanwitFilesystemInventoryV0, marshal(valid)); err == nil {
		t.Fatal("profile schema accepted an unknown envelope field")
	}
	delete(valid, "unexpected")
	delete(valid["data"].(map[string]any), "root_id")
	if err := contract.ValidateJSON(
		spanwitschema.SpanwitFilesystemInventoryV0, marshal(valid)); err == nil {
		t.Fatal("profile schema accepted a rootless gap")
	}
}
