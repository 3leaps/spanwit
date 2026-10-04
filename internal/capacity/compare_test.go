package capacity

import (
	"encoding/json"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

func TestCompareExactClaimsAndLowerBounds(t *testing.T) {
	left := compareTestInput(400, 100)
	right := compareTestInput(550, 40)
	right.CaptureMode = "full"
	right.Accounting.Capture.BootID = "boot-2"

	report := Compare(time.Date(2026, 7, 31, 14, 0, 0, 0, time.UTC), left, right)
	available := findDelta(t, report, "filesystem", "available")
	if available.DeltaStatus != "exact" || available.DeltaBytes == nil ||
		*available.DeltaBytes != 150 {
		t.Fatalf("available=%#v", available)
	}
	held := findDelta(t, report, "held_open", "logical_bytes")
	if held.DeltaStatus != "indeterminate" || held.DeltaBytes != nil ||
		held.LeftBytes == nil || held.RightBytes == nil {
		t.Fatalf("held=%#v", held)
	}
	if report.Compatibility.Status != "compatible" {
		t.Fatalf("compatibility=%#v", report.Compatibility)
	}
	for _, name := range []string{"capture_mode", "boot_id"} {
		check := findCheck(t, report, name)
		if check.OK || !check.Informational {
			t.Fatalf("check=%#v", check)
		}
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceCompareV1, payload); err != nil {
		t.Fatalf("schema: %v\n%s", err, payload)
	}
}

func TestCompareIdentityAndHeldAsymmetryStayPlaneLocal(t *testing.T) {
	left := compareTestInput(400, 100)
	right := compareTestInput(500, 50)
	right.Accounting.Target.VolumeID = "volume-2"
	right.Accounting.Planes.HeldOpen = HeldOpenPlane{
		CollectionStatus: StatusNotRequested,
		Coverage:         emptyCoverage(),
	}

	report := Compare(time.Now(), left, right)
	if report.Compatibility.Status != "partial" {
		t.Fatalf("compatibility=%#v", report.Compatibility)
	}
	if got := findDelta(t, report, "filesystem", "available"); got.DeltaStatus != "incomparable" {
		t.Fatalf("filesystem=%#v", got)
	}
	if got := findDelta(t, report, "container", "free"); got.DeltaStatus != "exact" {
		t.Fatalf("container=%#v", got)
	}
	if got := findDelta(t, report, "held_open", "logical_bytes"); got.DeltaStatus != "indeterminate" {
		t.Fatalf("held=%#v", got)
	}
	if !containsString(report.Coverage.AsymmetricPlanes, "held_open") {
		t.Fatalf("coverage=%#v", report.Coverage)
	}
}

func TestCompareBootIdentityIsInformationalAcrossSessionBoundaries(t *testing.T) {
	left := compareTestInput(400, 100)
	right := compareTestInput(500, 50)
	left.Accounting.Capture.SessionBoundary = "before-quiesced"
	right.Accounting.Capture.SessionBoundary = "after-reboot"

	sameBoot := Compare(time.Now(), left, right)
	check := findCheck(t, sameBoot, "boot_id")
	if !check.OK || !check.Informational {
		t.Fatalf("same-boot check=%#v", check)
	}

	right.Accounting.Capture.BootID = "boot-2"
	crossBoot := Compare(time.Now(), left, right)
	check = findCheck(t, crossBoot, "boot_id")
	if check.OK || !check.Informational {
		t.Fatalf("cross-boot check=%#v", check)
	}
	if crossBoot.Compatibility.Status != "compatible" {
		t.Fatalf("cross-boot compatibility=%#v", crossBoot.Compatibility)
	}
}

func compareTestInput(available, held int64) CompareInput {
	total := int64(1000)
	used := total - available
	containerFree := available + 20
	containerCapacity := int64(1200)
	heldClaim := heldOpenClaim(held, StatusMeasured)
	zero := 0
	one := 1
	return CompareInput{
		Path:          "capture.json",
		ReportSchema:  "https://schemas.3leaps.dev/spanwit/space-report/v2.json",
		ReportVersion: 2,
		CaptureMode:   "capacity_only",
		CapturedAt:    "2026-07-31T13:00:00Z",
		Accounting: Accounting{
			Capture: Capture{
				CapturedAt: "2026-07-31T13:00:00Z",
				BootID:     "boot-1",
			},
			Target: Target{
				Platform:    "darwin",
				VolumeID:    "volume-1",
				ContainerID: "container-1",
			},
			Planes: Planes{
				Filesystem: FilesystemPlane{
					CollectionStatus: StatusMeasured,
					Source:           "statfs",
					Coverage:         emptyCoverage(),
					Total:            measuredClaim(total, "statfs_blocks", "exact"),
					Used:             measuredClaim(used, "statfs_used", "exact"),
					Available:        measuredClaim(available, "statfs_bavail", "exact"),
				},
				Container: ContainerPlane{
					CollectionStatus: StatusMeasured,
					Source:           "diskutil.apfs.list.plist",
					Coverage:         emptyCoverage(),
					Capacity:         claimPointer(measuredClaim(containerCapacity, "apfs_container_size", "exact")),
					Free:             claimPointer(measuredClaim(containerFree, "apfs_container_free", "exact")),
				},
				Volumes: VolumesPlane{
					CollectionStatus: StatusMeasured,
					Source:           "diskutil.apfs.list.plist",
					Coverage:         emptyCoverage(),
					Entries: []VolumeEntry{{
						ID:            "volume-1",
						IsTarget:      true,
						CapacityInUse: claimPointer(measuredClaim(used, "CapacityInUse", "exact")),
					}},
				},
				Snapshots: SnapshotsPlane{
					CollectionStatus: StatusPartial,
					Source:           "diskutil.apfs.listSnapshots.plist",
					Coverage:         emptyCoverage(),
					Bytes:            claimPointer(unsupportedClaim("not exposed")),
				},
				SystemManaged: SystemManagedPlane{
					CollectionStatus: StatusMeasured,
					Source:           "filesystem.allocated_blocks",
					Coverage:         emptyCoverage(),
					Policy:           ManagedPolicy,
				},
				HeldOpen: HeldOpenPlane{
					CollectionStatus:  StatusMeasured,
					Source:            heldOpenSource,
					Coverage:          emptyCoverage(),
					Disclosure:        "none",
					Privilege:         "unprivileged",
					ProcessesExamined: &one,
					ProcessesDenied:   &zero,
					UniqueObjects:     &one,
					HolderProcesses:   &one,
					LogicalBytes:      &heldClaim,
				},
			},
		},
	}
}

func claimPointer(claim ByteClaim) *ByteClaim {
	return &claim
}

func findDelta(t *testing.T, report CompareReport, plane, field string) PlaneDelta {
	t.Helper()
	for _, delta := range report.PlaneDeltas {
		if delta.Plane == plane && delta.Field == field {
			return delta
		}
	}
	t.Fatalf("missing %s.%s", plane, field)
	return PlaneDelta{}
}

func findCheck(t *testing.T, report CompareReport, name string) CompatibilityCheck {
	t.Helper()
	for _, check := range report.Compatibility.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing check %s", name)
	return CompatibilityCheck{}
}
