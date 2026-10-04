package space

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/contract"
)

func testCapacityCollector(t *testing.T, calls *int) capacity.Collector {
	t.Helper()
	return capacity.Collector{
		Platform:     "linux",
		Architecture: "amd64",
		Now: func() time.Time {
			return time.Date(2026, 7, 30, 17, 0, 0, 0, time.UTC)
		},
		FilesystemSampler: func(_ context.Context, path string) (capacity.FilesystemSample, error) {
			*calls++
			return capacity.FilesystemSample{
				Path: path, Mount: "/", VolumeID: "fs:1", FSType: "ext4",
				Total: 1_000_000, Used: 600_000, Available: 400_000,
			}, nil
		},
	}
}

func TestAnalyzeCapacityOnlyOneObservationFeedsPressureAndPlane(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	report, err := AnalyzeCapacityOnly(context.Background(), CapacityOptions{
		Path:      home,
		Collector: testCapacityCollector(t, &calls),
		Tool:      capacity.ToolIdentity{Name: "spanwit", Version: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("filesystem samples=%d want 1", calls)
	}
	if report.GeneratedAt != report.CapacityAccounting.Capture.CapturedAt {
		t.Fatal("capture clock diverged")
	}
	if err := ValidateCapacityInvariants(report); err != nil {
		t.Fatal(err)
	}
	if report.Pressure.AvailBytes != *report.CapacityAccounting.Planes.Filesystem.Available.Bytes {
		t.Fatal("pressure and capacity plane did not share observation")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		`"verified_reclaimable"`, `"hotspots"`, `"unverified"`, `"unknown"`,
		`"prune_handoff"`, `"min_size"`, `"top"`, `"max_depth"`,
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("capacity-only report contains deep inventory key %s", forbidden)
		}
	}
}

func TestAnalyzeCapacityOnly_MutationContractOpenAndReadOnly(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"open", "read_only"} {
		calls := 0
		report, err := AnalyzeCapacityOnly(context.Background(), CapacityOptions{
			Path:             home,
			Collector:        testCapacityCollector(t, &calls),
			Tool:             capacity.ToolIdentity{Name: "spanwit", Version: "test"},
			MutationContract: want,
		})
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		if report.MutationContract != want {
			t.Fatalf("%s: got %q", want, report.MutationContract)
		}
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"mutation_contract":"`+want+`"`) {
			t.Fatalf("%s: serialized report missing contract: %s", want, raw)
		}
		if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV2, raw); err != nil {
			t.Fatalf("%s schema: %v", want, err)
		}
	}
	if _, err := AnalyzeCapacityOnly(context.Background(), CapacityOptions{
		Path: home, Collector: testCapacityCollector(t, new(int)),
		Tool:             capacity.ToolIdentity{Name: "spanwit", Version: "test"},
		MutationContract: "write",
	}); err == nil {
		t.Fatal("invalid mutation_contract must fail")
	}
}

func TestAnalyzeCapacityOnlyCrossVolumeKeepsPrimaryTarget(t *testing.T) {
	calls := 0
	collector := testCapacityCollector(t, &calls)
	collector.FilesystemSampler = func(_ context.Context, path string) (capacity.FilesystemSample, error) {
		calls++
		sample := capacity.FilesystemSample{
			Path: path, Mount: "/primary", VolumeID: "fs:primary", FSType: "apfs",
			Total: 1000, Used: 600, Available: 400,
		}
		if strings.Contains(path, "external") {
			sample.Mount = "/external"
			sample.VolumeID = "fs:external"
			sample.Available = 800
		}
		return sample, nil
	}
	root := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	report, err := AnalyzeCapacityOnly(context.Background(), CapacityOptions{
		Path: root, Collector: collector, Tool: capacity.ToolIdentity{Name: "spanwit", Version: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.AnalysisPressure == nil || report.AnalysisPressure.VolumeID != "fs:external" {
		t.Fatalf("analysis pressure=%#v", report.AnalysisPressure)
	}
	if report.CapacityAccounting.Target.Path != report.Pressure.Path ||
		report.CapacityAccounting.Target.VolumeID != "fs:primary" {
		t.Fatalf("capacity target must follow primary observation: %#v pressure=%#v",
			report.CapacityAccounting.Target, report.Pressure)
	}
	if report.Root == report.CapacityAccounting.Target.Path {
		t.Fatal("test did not exercise distinct analysis root and capacity target")
	}
}

func TestValidateCapacityInvariantsRejectsMismatches(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	report, err := AnalyzeCapacityOnly(context.Background(), CapacityOptions{
		Path: home, Collector: testCapacityCollector(t, &calls),
		Tool: capacity.ToolIdentity{Name: "spanwit", Version: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*CapacityOnlyReport)
	}{
		{"timestamp", func(r *CapacityOnlyReport) { r.GeneratedAt = "2026-07-30T17:00:01Z" }},
		{"target path", func(r *CapacityOnlyReport) { r.CapacityAccounting.Target.Path = "/other" }},
		{"target mount", func(r *CapacityOnlyReport) { r.CapacityAccounting.Target.Mount = "/other" }},
		{"target volume", func(r *CapacityOnlyReport) { r.CapacityAccounting.Target.VolumeID = "fs:2" }},
		{"available bytes", func(r *CapacityOnlyReport) {
			value := int64(1)
			r.CapacityAccounting.Planes.Filesystem.Available.Bytes = &value
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bad := report
			tt.mutate(&bad)
			if err := ValidateCapacityInvariants(bad); err == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
}
