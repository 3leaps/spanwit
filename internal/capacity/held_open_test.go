package capacity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestParseHeldOpenLsofDeduplicatesObjectsAndRedactsPaths(t *testing.T) {
	raw := []byte(
		lsofSet("p10", "cCodex", "u501") +
			lsofSet("f7", "D0x1", "i99", "k0", "s100", "n/Users/example/project/deleted.bin") +
			lsofSet("f8", "D0x1", "i100", "k0", "s50", "n/private/tmp/line\nbreak") +
			lsofSet("p20", "cnode", "u502") +
			lsofSet("f9", "D0x1", "i99", "k0", "s100", "n/Users/example/project/deleted.bin"),
	)
	parsed := parseHeldOpenLsof(raw)
	plane := buildHeldOpenPlane(parsed, "none", "/Users/example/project", "unprivileged")
	if plane.CollectionStatus != StatusMeasured {
		t.Fatalf("status=%s gaps=%#v", plane.CollectionStatus, plane.Coverage.Gaps)
	}
	if got := optionalHeldInt(plane.UniqueObjects); got != 2 {
		t.Fatalf("unique_objects=%d", got)
	}
	if got := optionalHeldInt(plane.Relationships); got != 3 {
		t.Fatalf("relationships=%d", got)
	}
	if plane.LogicalBytes == nil || plane.LogicalBytes.Bytes == nil ||
		*plane.LogicalBytes.Bytes != 150 {
		t.Fatalf("logical=%#v", plane.LogicalBytes)
	}
	if len(plane.Entries) != 2 || plane.Entries[0].RootClass != "target_tree" ||
		len(plane.Entries[0].Holders) != 2 {
		t.Fatalf("entries=%#v", plane.Entries)
	}
	rawJSON, err := json.Marshal(plane)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"deleted.bin", "line\\nbreak", `"path":`, "command_summary",
	} {
		if strings.Contains(string(rawJSON), forbidden) {
			t.Fatalf("default disclosure leaked %q: %s", forbidden, rawJSON)
		}
	}

	groupBytes := int64(0)
	groupObjects := 0
	for _, group := range plane.Groups {
		groupObjects += group.UniqueObjects
		if group.LogicalBytes == nil || group.LogicalBytes.Bytes == nil {
			t.Fatalf("group missing bytes: %#v", group)
		}
		groupBytes += *group.LogicalBytes.Bytes
	}
	if groupObjects != 2 || groupBytes != 150 {
		t.Fatalf("groups double counted: objects=%d bytes=%d groups=%#v",
			groupObjects, groupBytes, plane.Groups)
	}
}

func TestHeldOpenFullDisclosureAndCapsAreExplicit(t *testing.T) {
	files := make([]heldOpenFile, 0, heldOpenEntryLimit+heldOpenHolderLimit+2)
	for index := 0; index <= heldOpenEntryLimit; index++ {
		files = append(files, heldOpenFile{
			device: "0x1",
			inode:  fmt.Sprintf("%d", index),
			size:   int64(index + 1),
			path:   fmt.Sprintf("/tmp/object-%d", index),
			holder: heldOpenProcess{pid: index + 1, executable: "tool"},
		})
	}
	for index := 0; index <= heldOpenHolderLimit; index++ {
		files = append(files, heldOpenFile{
			device: "0x2",
			inode:  "shared",
			size:   1000,
			path:   "/tmp/shared",
			holder: heldOpenProcess{pid: 1000 + index, executable: "tool"},
		})
	}
	plane := buildHeldOpenPlane(heldOpenParseResult{
		files:             files,
		processesExamined: len(files),
	}, "full", "/work", "unprivileged")
	if plane.CollectionStatus != StatusPartial || len(plane.Entries) != heldOpenEntryLimit {
		t.Fatalf("status=%s entries=%d", plane.CollectionStatus, len(plane.Entries))
	}
	if plane.Entries[0].Path == "" {
		t.Fatal("full disclosure omitted path")
	}
	if len(plane.Entries[0].Holders) != heldOpenHolderLimit {
		t.Fatalf("holders=%d", len(plane.Entries[0].Holders))
	}
	if len(plane.Coverage.Gaps) != 1 ||
		plane.Coverage.Gaps[0].OmittedObjects == nil ||
		*plane.Coverage.Gaps[0].OmittedObjects != 2 ||
		plane.Coverage.Gaps[0].OmittedHolders == nil ||
		*plane.Coverage.Gaps[0].OmittedHolders != 1 {
		t.Fatalf("gaps=%#v", plane.Coverage.Gaps)
	}
}

func TestCollectHeldOpenFailureIsCuratedAndPathFree(t *testing.T) {
	plane := collectHeldOpen(
		context.Background(),
		RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
			return nil, &RunError{Code: "permission_denied"}
		}),
		"none",
		"/sensitive/project",
	)
	if plane.CollectionStatus != StatusUnavailable ||
		len(plane.Coverage.Gaps) != 1 ||
		plane.Coverage.Gaps[0].Code != "permission_denied" {
		t.Fatalf("plane=%#v", plane)
	}
	raw, err := json.Marshal(plane)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sensitive") || strings.Contains(string(raw), `"entries"`) {
		t.Fatalf("failure leaked path or entries: %s", raw)
	}
}

func TestCollectHeldOpenPermissionWarningLeavesDeniedProcessCountUnclaimed(t *testing.T) {
	raw := []byte(
		lsofSet("p10", "cCodex", "u501") +
			lsofSet("f7", "D0x1", "i99", "k0", "s100", "n/deleted.bin"),
	)
	plane := collectHeldOpen(
		context.Background(),
		RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
			return raw, &RunError{Code: "permission_denied"}
		}),
		"none",
		"/work",
	)
	if plane.CollectionStatus != StatusPartial || plane.ProcessesDenied != nil {
		t.Fatalf("plane=%#v", plane)
	}
	if len(plane.Coverage.Gaps) != 1 ||
		plane.Coverage.Gaps[0].Code != "permission_denied" ||
		plane.Coverage.Gaps[0].OmittedObjects != nil ||
		plane.Coverage.Gaps[0].OmittedHolders != nil {
		t.Fatalf("gaps=%#v", plane.Coverage.Gaps)
	}
}

func TestHeldOpenGroupOverflowUsesBoundedPartitionWithoutInventingHolders(t *testing.T) {
	files := make([]heldOpenFile, 0, heldOpenEntryLimit+1)
	for index := 0; index <= heldOpenEntryLimit; index++ {
		files = append(files, heldOpenFile{
			device: "0x1",
			inode:  fmt.Sprintf("%d", index),
			size:   int64(index + 1),
			holder: heldOpenProcess{
				pid:        index + 1,
				executable: fmt.Sprintf("tool-%03d", index),
			},
		})
	}
	plane := buildHeldOpenPlane(heldOpenParseResult{
		files:             files,
		processesExamined: len(files),
	}, "none", "/work", "unprivileged")
	if len(plane.Groups) != heldOpenEntryLimit {
		t.Fatalf("groups=%d", len(plane.Groups))
	}
	var groupObjects int
	var groupBytes int64
	for _, group := range plane.Groups {
		groupObjects += group.UniqueObjects
		if group.LogicalBytes == nil || group.LogicalBytes.Bytes == nil {
			t.Fatalf("group=%#v", group)
		}
		groupBytes += *group.LogicalBytes.Bytes
	}
	if groupObjects != len(files) || plane.LogicalBytes == nil ||
		plane.LogicalBytes.Bytes == nil || groupBytes != *plane.LogicalBytes.Bytes {
		t.Fatalf("objects=%d bytes=%d plane=%#v", groupObjects, groupBytes, plane.LogicalBytes)
	}
	if len(plane.Coverage.Gaps) != 1 ||
		plane.Coverage.Gaps[0].OmittedObjects == nil ||
		*plane.Coverage.Gaps[0].OmittedObjects != 1 ||
		plane.Coverage.Gaps[0].OmittedHolders != nil {
		t.Fatalf("gaps=%#v", plane.Coverage.Gaps)
	}
}

func lsofSet(fields ...string) string {
	return strings.Join(fields, "\x00") + "\x00\n"
}

func optionalHeldInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
