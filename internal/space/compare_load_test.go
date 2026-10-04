package space

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/capacity"
)

func TestLoadCompareInputJSONAcceptsValidatedV2AndRejectsTampering(t *testing.T) {
	calls := 0
	report, err := AnalyzeCapacityOnly(context.Background(), CapacityOptions{
		Path:      t.TempDir(),
		Collector: testCapacityCollector(t, &calls),
		Tool:      capacity.ToolIdentity{Name: "spanwit", Version: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	input, err := LoadCompareInputJSON(raw, "left.json")
	if err != nil {
		t.Fatal(err)
	}
	if input.Path != "left.json" || input.ReportVersion != 2 ||
		input.CaptureMode != CaptureModeCapacityOnly {
		t.Fatalf("input=%#v", input)
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["generated_at"] = "2026-07-30T17:00:01Z"
	tampered, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCompareInputJSON(tampered, "bad.json"); err == nil ||
		!strings.Contains(err.Error(), "capacity invariant") {
		t.Fatalf("tampered error=%v", err)
	}
}

func TestLoadCompareInputJSONAppliesResourceCeilingsBeforeUse(t *testing.T) {
	tooLarge := make([]byte, MaxCompareInputBytes+1)
	if _, err := LoadCompareInputJSON(tooLarge, "large.json"); err == nil ||
		!strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("large error=%v", err)
	}
	var builder strings.Builder
	builder.WriteByte('[')
	for index := 0; index <= MaxCompareArrayItems; index++ {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteByte('0')
	}
	builder.WriteByte(']')
	if _, err := LoadCompareInputJSON([]byte(builder.String()), "array.json"); err == nil ||
		!strings.Contains(err.Error(), "array exceeds") {
		t.Fatalf("array error=%v", err)
	}
}

func TestValidateCompareCapacityTopologyRejectsIdentityConfusion(t *testing.T) {
	value := int64(10)
	accounting := capacity.Accounting{
		Target: capacity.Target{VolumeID: "volume-1", ContainerID: "container-1"},
		Planes: capacity.Planes{
			Container: capacity.ContainerPlane{
				ID:       "container-2",
				Capacity: &capacity.ByteClaim{Status: capacity.StatusMeasured, Bytes: &value},
			},
			Volumes: capacity.VolumesPlane{
				Entries: []capacity.VolumeEntry{{
					ID:       "volume-2",
					IsTarget: true,
				}},
			},
		},
	}
	if err := validateCompareCapacityTopology(accounting); err == nil ||
		!strings.Contains(err.Error(), "container identity") {
		t.Fatalf("container error=%v", err)
	}
	accounting.Planes.Container = capacity.ContainerPlane{}
	accounting.Planes.Volumes.Entries = append(accounting.Planes.Volumes.Entries,
		capacity.VolumeEntry{ID: "volume-3", IsTarget: true})
	if err := validateCompareCapacityTopology(accounting); err == nil ||
		!strings.Contains(err.Error(), "multiple target") {
		t.Fatalf("volume error=%v", err)
	}
}

func TestLoadCompareInputJSONKeepsFilesystemAndAPFSVolumeIdentitiesSeparate(t *testing.T) {
	calls := 0
	report, err := AnalyzeCapacityOnly(context.Background(), CapacityOptions{
		Path:      t.TempDir(),
		Collector: testCapacityCollector(t, &calls),
		Tool:      capacity.ToolIdentity{Name: "spanwit", Version: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	report.Pressure.VolumeID = "fsid:16777229:26"
	report.CapacityAccounting.Target.Platform = "darwin"
	report.CapacityAccounting.Target.VolumeID = report.Pressure.VolumeID
	report.CapacityAccounting.Target.ContainerID = "disk3"
	report.CapacityAccounting.Capture.OS.GOOS = "darwin"
	containerCapacity := int64(1_200_000)
	containerFree := int64(500_000)
	targetUsed := int64(700_000)
	report.CapacityAccounting.Planes.Container = capacity.ContainerPlane{
		CollectionStatus: capacity.StatusMeasured,
		Source:           "diskutil.apfs.list.plist",
		Coverage:         capacity.Coverage{Gaps: []capacity.CoverageGap{}},
		ID:               "disk3",
		Capacity:         testCompareClaim(containerCapacity, "apfs_container_size"),
		Free:             testCompareClaim(containerFree, "apfs_container_free"),
	}
	report.CapacityAccounting.Planes.Volumes = capacity.VolumesPlane{
		CollectionStatus: capacity.StatusMeasured,
		Source:           "diskutil.apfs.list.plist",
		Coverage:         capacity.Coverage{Gaps: []capacity.CoverageGap{}},
		Entries: []capacity.VolumeEntry{{
			ID:            "disk3s5",
			IsTarget:      true,
			CapacityInUse: testCompareClaim(targetUsed, "CapacityInUse"),
		}},
	}

	leftRaw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	right := report
	right.GeneratedAt = "2026-07-30T17:01:00Z"
	right.CapacityAccounting.Capture.CapturedAt = right.GeneratedAt
	*right.CapacityAccounting.Planes.Filesystem.Available.Bytes += 100
	right.Pressure.AvailBytes += 100
	*right.CapacityAccounting.Planes.Container.Free.Bytes += 100
	*right.CapacityAccounting.Planes.Volumes.Entries[0].CapacityInUse.Bytes -= 100
	rightRaw, err := json.Marshal(right)
	if err != nil {
		t.Fatal(err)
	}
	leftInput, err := LoadCompareInputJSON(leftRaw, "left.json")
	if err != nil {
		t.Fatal(err)
	}
	rightInput, err := LoadCompareInputJSON(rightRaw, "right.json")
	if err != nil {
		t.Fatal(err)
	}
	compared := capacity.Compare(time.Now(), leftInput, rightInput)
	for _, planeField := range [][2]string{
		{"filesystem", "available"},
		{"container", "free"},
		{"volumes", "target.capacity_in_use"},
	} {
		if delta := findCompareDelta(t, compared, planeField[0], planeField[1]); delta.DeltaStatus != "exact" {
			t.Fatalf("%s.%s=%#v", planeField[0], planeField[1], delta)
		}
	}
}

func testCompareClaim(value int64, basis string) *capacity.ByteClaim {
	return &capacity.ByteClaim{
		Status: capacity.StatusMeasured,
		Bytes:  &value,
		Human:  capacity.HumanBytes(value),
		Basis:  basis,
		Bound:  "exact",
	}
}

func findCompareDelta(
	t *testing.T,
	report capacity.CompareReport,
	plane string,
	field string,
) capacity.PlaneDelta {
	t.Helper()
	for _, delta := range report.PlaneDeltas {
		if delta.Plane == plane && delta.Field == field {
			return delta
		}
	}
	t.Fatalf("missing %s.%s", plane, field)
	return capacity.PlaneDelta{}
}
