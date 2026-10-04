package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "darwin", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testSample(path string) FilesystemSample {
	return FilesystemSample{
		Path: path, Mount: "/System/Volumes/Data", VolumeID: "fsid:9:5", FSType: "apfs",
		Total: 1_000_000, Used: 600_000, Available: 400_000,
	}
}

func TestCollectorDarwinSupportedSelectsExactTarget(t *testing.T) {
	t.Parallel()
	var calls []string
	var mu sync.Mutex
	runner := RunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, strings.Join(args, " "))
		mu.Unlock()
		switch strings.Join(args[:len(args)-1], " ") {
		case "info -plist":
			return fixture(t, "info-apfs.plist"), nil
		case "apfs list":
			return fixture(t, "apfs-list-multi.plist"), nil
		case "apfs listSnapshots -plist":
			return fixture(t, "snapshots.plist"), nil
		default:
			t.Fatalf("unexpected argv: %q", args)
			return nil, nil
		}
	})
	collector := Collector{
		Platform:     "darwin",
		Architecture: "arm64",
		Now: func() time.Time {
			return time.Date(2026, 7, 30, 16, 0, 0, 0, time.UTC)
		},
		FilesystemSampler: func(_ context.Context, path string) (FilesystemSample, error) {
			return testSample(path), nil
		},
		DarwinRunner: runner,
		ManagedRootMeasurer: func(context.Context, string, int) ManagedMeasurement {
			return ManagedMeasurement{Complete: true, Gaps: []CoverageGap{}}
		},
	}
	result, err := collector.Collect(context.Background(), Request{
		TargetPath: "/target",
		Tool:       ToolIdentity{Name: "spanwit", Version: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accounting.Target.ContainerID != "disk9" {
		t.Fatalf("container=%q", result.Accounting.Target.ContainerID)
	}
	if result.Accounting.Target.VolumeID != "fsid:9:5" {
		t.Fatalf("filesystem identity must remain statfs identity, got %q", result.Accounting.Target.VolumeID)
	}
	volumes := result.Accounting.Planes.Volumes.Entries
	if len(volumes) != 6 {
		t.Fatalf("target container volumes=%d want 6", len(volumes))
	}
	targets := 0
	for _, volume := range volumes {
		if volume.IsTarget {
			targets++
			if volume.ID != "disk9s5" || volume.Role != "Data" {
				t.Fatalf("wrong target: %#v", volume)
			}
		}
	}
	if targets != 1 {
		t.Fatalf("target entries=%d", targets)
	}
	snapshots := result.Accounting.Planes.Snapshots
	if snapshots.CollectionStatus != StatusPartial || snapshots.Count == nil || *snapshots.Count != 1 {
		t.Fatalf("snapshots=%#v", snapshots)
	}
	if snapshots.Bytes == nil || snapshots.Bytes.Status != StatusUnsupported || snapshots.Bytes.Bytes != nil {
		t.Fatalf("snapshot bytes must be unsupported without numeric zero: %#v", snapshots.Bytes)
	}
	if got := strings.Join(calls, "|"); !strings.Contains(got, "info -plist /System/Volumes/Data") ||
		!strings.Contains(got, "apfs list -plist") ||
		!strings.Contains(got, "apfs listSnapshots -plist disk9s5") {
		t.Fatalf("fixed argv calls=%q", got)
	}
}

func TestCollectorDarwinSnapshotFailurePreservesContainer(t *testing.T) {
	t.Parallel()
	runner := RunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case args[0] == "info":
			return fixture(t, "info-apfs.plist"), nil
		case args[1] == "list":
			return fixture(t, "apfs-list-multi.plist"), nil
		default:
			return nil, &RunError{Code: "permission_denied"}
		}
	})
	result, err := (Collector{
		Platform: "darwin",
		FilesystemSampler: func(_ context.Context, path string) (FilesystemSample, error) {
			return testSample(path), nil
		},
		DarwinRunner: runner,
		ManagedRootMeasurer: func(context.Context, string, int) ManagedMeasurement {
			return ManagedMeasurement{Complete: true}
		},
	}).Collect(context.Background(), Request{TargetPath: "/target", Tool: ToolIdentity{Name: "spanwit", Version: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accounting.Planes.Container.CollectionStatus != StatusMeasured {
		t.Fatal("container result was erased by snapshot failure")
	}
	snapshots := result.Accounting.Planes.Snapshots
	if snapshots.CollectionStatus != StatusUnavailable || len(snapshots.Coverage.Gaps) != 1 ||
		snapshots.Coverage.Gaps[0].Code != "permission_denied" {
		t.Fatalf("snapshots=%#v", snapshots)
	}
}

func TestCollectorDarwinOperationalFailuresAreUnavailable(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"command_missing", "command_failed", "permission_denied", "timeout"} {
		code := code
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			result, err := (Collector{
				Platform: "darwin",
				FilesystemSampler: func(_ context.Context, path string) (FilesystemSample, error) {
					return testSample(path), nil
				},
				DarwinRunner: RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
					return nil, &RunError{Code: code}
				}),
				ManagedRootMeasurer: func(context.Context, string, int) ManagedMeasurement {
					return ManagedMeasurement{Complete: true}
				},
			}).Collect(context.Background(), Request{TargetPath: "/target", Tool: ToolIdentity{Name: "spanwit", Version: "test"}})
			if err != nil {
				t.Fatal(err)
			}
			plane := result.Accounting.Planes.Container
			if plane.CollectionStatus != StatusUnavailable || plane.Coverage.Gaps[0].Code != code {
				t.Fatalf("plane=%#v", plane)
			}
		})
	}
}

func TestCollectorNonDarwinLeavesOptionalPlanesUnsupported(t *testing.T) {
	t.Parallel()
	result, err := (Collector{
		Platform: "linux",
		FilesystemSampler: func(_ context.Context, path string) (FilesystemSample, error) {
			sample := testSample(path)
			sample.FSType = "ext4"
			return sample, nil
		},
	}).Collect(context.Background(), Request{TargetPath: "/target", Tool: ToolIdentity{Name: "spanwit", Version: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accounting.Planes.Filesystem.CollectionStatus != StatusMeasured {
		t.Fatal("filesystem must remain measured")
	}
	for name, status := range map[string]string{
		"container":      result.Accounting.Planes.Container.CollectionStatus,
		"volumes":        result.Accounting.Planes.Volumes.CollectionStatus,
		"snapshots":      result.Accounting.Planes.Snapshots.CollectionStatus,
		"system_managed": result.Accounting.Planes.SystemManaged.CollectionStatus,
	} {
		if status != StatusUnsupported {
			t.Fatalf("%s status=%q", name, status)
		}
	}
}

func TestCollectorDarwinNonAPFSDoesNotRunAPFSQueries(t *testing.T) {
	t.Parallel()
	calls := 0
	result, err := (Collector{
		Platform: "darwin",
		FilesystemSampler: func(_ context.Context, path string) (FilesystemSample, error) {
			sample := testSample(path)
			sample.Mount = "/Volumes/Archive"
			return sample, nil
		},
		DarwinRunner: RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
			calls++
			return fixture(t, "info-non-apfs.plist"), nil
		}),
		ManagedRootMeasurer: func(context.Context, string, int) ManagedMeasurement {
			return ManagedMeasurement{Complete: true}
		},
	}).Collect(context.Background(), Request{
		TargetPath: "/target",
		Tool:       ToolIdentity{Name: "spanwit", Version: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("diskutil calls=%d want info only", calls)
	}
	if result.Accounting.Planes.Container.CollectionStatus != StatusUnsupported ||
		result.Accounting.Planes.Snapshots.CollectionStatus != StatusUnsupported {
		t.Fatalf("non-APFS planes=%#v", result.Accounting.Planes)
	}
}

func TestCollectorSharesOneDeadline(t *testing.T) {
	t.Parallel()
	var first time.Time
	var calls int
	runner := RunnerFunc(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("runner context has no deadline")
		}
		if first.IsZero() {
			first = deadline
		} else if !deadline.Equal(first) {
			t.Fatalf("deadline reset: first=%s next=%s", first, deadline)
		}
		return nil, &RunError{Code: "command_failed"}
	})
	_, err := (Collector{
		Platform: "darwin",
		Deadline: 250 * time.Millisecond,
		FilesystemSampler: func(ctx context.Context, path string) (FilesystemSample, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("filesystem sampler context has no deadline")
			}
			first = deadline
			return testSample(path), nil
		},
		DarwinRunner: runner,
		ManagedRootMeasurer: func(ctx context.Context, _ string, _ int) ManagedMeasurement {
			deadline, ok := ctx.Deadline()
			if !ok || !deadline.Equal(first) {
				t.Fatalf("managed root did not share collector deadline")
			}
			return ManagedMeasurement{Complete: true}
		},
	}).Collect(context.Background(), Request{TargetPath: "/target", Tool: ToolIdentity{Name: "spanwit", Version: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("info failure should stop APFS queries, calls=%d", calls)
	}
}

func TestCollectorFilesystemSamplesStayInsideDeadline(t *testing.T) {
	t.Parallel()

	t.Run("mandatory sample", func(t *testing.T) {
		t.Parallel()
		returned := make(chan struct{})
		_, err := (Collector{
			Platform: "linux",
			Deadline: 100 * time.Millisecond,
			FilesystemSampler: func(ctx context.Context, _ string) (FilesystemSample, error) {
				defer close(returned)
				<-ctx.Done()
				return FilesystemSample{}, ctx.Err()
			},
		}).Collect(context.Background(), Request{
			TargetPath: "/target",
			Tool:       ToolIdentity{Name: "spanwit", Version: "test"},
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v", err)
		}
		select {
		case <-returned:
		default:
			t.Fatal("mandatory sampler remained active after collection returned")
		}
	})

	t.Run("analysis sample", func(t *testing.T) {
		t.Parallel()
		var deadline time.Time
		var calls int
		var runnerCalls int
		var managedCalls int
		returned := make(chan struct{})
		result, err := (Collector{
			Platform: "darwin",
			Deadline: 100 * time.Millisecond,
			FilesystemSampler: func(ctx context.Context, path string) (FilesystemSample, error) {
				calls++
				got, ok := ctx.Deadline()
				if !ok {
					t.Fatal("filesystem sampler context has no deadline")
				}
				if deadline.IsZero() {
					deadline = got
				} else if !deadline.Equal(got) {
					t.Fatalf("analysis deadline reset: first=%s next=%s", deadline, got)
				}
				if path == "/target" {
					return testSample(path), nil
				}
				defer close(returned)
				<-ctx.Done()
				return FilesystemSample{}, ctx.Err()
			},
			DarwinRunner: RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
				runnerCalls++
				return nil, errors.New("must not run")
			}),
			ManagedRootMeasurer: func(context.Context, string, int) ManagedMeasurement {
				managedCalls++
				return ManagedMeasurement{}
			},
		}).Collect(context.Background(), Request{
			TargetPath:   "/target",
			AnalysisPath: "/analysis",
			Tool:         ToolIdentity{Name: "spanwit", Version: "test"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 2 || runnerCalls != 0 || managedCalls != 0 || result.AnalysisFilesystem != nil {
			t.Fatalf(
				"filesystem=%d runner=%d managed=%d analysis=%#v",
				calls,
				runnerCalls,
				managedCalls,
				result.AnalysisFilesystem,
			)
		}
		select {
		case <-returned:
		default:
			t.Fatal("analysis sampler remained active after collection returned")
		}
		if result.Accounting.Narrative == nil ||
			!strings.Contains(strings.Join(result.Accounting.Narrative.Unavailable, "\n"), "analysis_pressure coverage gap: timeout") {
			t.Fatalf("narrative=%#v", result.Accounting.Narrative)
		}
	})
}

func TestCollectorCanceledParentSkipsFilesystemSampling(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := (Collector{
		Platform: "linux",
		FilesystemSampler: func(context.Context, string) (FilesystemSample, error) {
			calls++
			return FilesystemSample{}, nil
		},
	}).Collect(ctx, Request{
		TargetPath: "/target",
		Tool:       ToolIdentity{Name: "spanwit", Version: "test"},
	})
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestBuildNarrativeReconcilesClaimsCoverageAndNotRequested(t *testing.T) {
	t.Parallel()
	three := 3
	snapshotCount := 1
	containerCapacity := measuredClaim(1000, "apfs_container_size", "exact")
	containerFree := measuredClaim(400, "apfs_container_free", "exact")
	volumeUsed := measuredClaim(600, "CapacityInUse", "exact")
	snapshotBytes := unsupportedClaim("public API does not expose bytes")
	managedBytes := partialClaim(4096, "allocated_blocks", "lower")
	planes := Planes{
		Filesystem: filesystemPlane(testSample("/target")),
		Container: ContainerPlane{
			CollectionStatus: StatusMeasured,
			Coverage:         emptyCoverage(),
			ID:               "disk9",
			Capacity:         &containerCapacity,
			Free:             &containerFree,
		},
		Volumes: VolumesPlane{
			CollectionStatus: StatusMeasured,
			Coverage:         emptyCoverage(),
			Entries: []VolumeEntry{{
				ID: "disk9s5", CapacityInUse: &volumeUsed, IsTarget: true,
			}},
		},
		Snapshots: SnapshotsPlane{
			CollectionStatus: StatusPartial,
			Coverage:         emptyCoverage(),
			Count:            &snapshotCount,
			Bytes:            &snapshotBytes,
		},
		SystemManaged: SystemManagedPlane{
			CollectionStatus: StatusPartial,
			Coverage: Coverage{Gaps: []CoverageGap{{
				Code: "permission_denied", Scope: "system_managed",
				Detail: "curated denied detail", OmittedObjects: &three,
			}}},
			Policy: ManagedPolicy,
			Entries: []SystemManagedEntry{{
				ID: "apple_assets_v2", Allocated: &managedBytes,
			}},
		},
		HeldOpen: HeldOpenPlane{
			CollectionStatus: StatusNotRequested,
			Coverage:         emptyCoverage(),
		},
	}
	narrative := buildNarrative(planes)
	known := strings.Join(narrative.Known, "\n")
	unavailable := strings.Join(narrative.Unavailable, "\n")
	for _, want := range []string{
		"container.capacity: 1000 B",
		"volumes.disk9s5.capacity_in_use: 600 B",
		"snapshots: partial (1 entries)",
		"system_managed.apple_assets_v2.allocated: 4.0 KiB",
		"status=partial; basis=allocated_blocks; bound=lower",
	} {
		if !strings.Contains(known, want) {
			t.Fatalf("known story missing %q:\n%s", want, known)
		}
	}
	for _, want := range []string{
		"snapshots.bytes: unsupported",
		"system_managed coverage gap: permission_denied",
		"omitted_objects=3",
	} {
		if !strings.Contains(unavailable, want) {
			t.Fatalf("unavailable story missing %q:\n%s", want, unavailable)
		}
	}
	if strings.Contains(unavailable, "held_open") ||
		!strings.Contains(narrative.Summary, "Held-open collection is not_requested") {
		t.Fatalf("not-requested classification summary=%q unavailable=%q", narrative.Summary, unavailable)
	}
	raw, err := json.Marshal(narrative)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"unexplained_hints":[]`) {
		t.Fatalf("empty unexplained axis was omitted: %s", raw)
	}
}

func TestParseDarwinRejectsMalformedAndAmbiguousIdentity(t *testing.T) {
	t.Parallel()
	if _, err := parseDarwinInfo([]byte(`<plist><dict>`)); err == nil {
		t.Fatal("truncated plist accepted")
	}
	wrongType := []byte(`<?xml version="1.0"?><plist><dict>
<key>DeviceIdentifier</key><integer>9</integer>
<key>FilesystemType</key><string>apfs</string>
<key>APFSContainerReference</key><string>disk9</string>
</dict></plist>`)
	if _, err := parseDarwinInfo(wrongType); err == nil {
		t.Fatal("wrong typed identity accepted")
	}
	raw := fixture(t, "apfs-list-multi.plist")
	if _, err := parseAPFSList(raw, "missing"); err == nil {
		t.Fatal("missing target mapping accepted")
	}
	wrongNumeric := strings.Replace(string(raw), "<integer>1000000</integer>", "<string>wrong</string>", 1)
	if _, err := parseAPFSList([]byte(wrongNumeric), "disk9s5"); err == nil {
		t.Fatal("wrong typed numeric field accepted")
	}
	ambiguous := strings.Replace(string(raw), "<string>disk9s1</string>", "<string>disk9s5</string>", 1)
	if _, err := parseAPFSList([]byte(ambiguous), "disk9s5"); err == nil {
		t.Fatal("ambiguous target mapping accepted")
	}
}

func TestCollectManagedRootsAggregatesCoverageAndKeepsLowerBound(t *testing.T) {
	t.Parallel()
	one := 1
	plane := collectManagedRoots(context.Background(), []ManagedRoot{{
		ID: "managed", Path: "/fixed", Remediation: "unknown",
	}}, 10, func(context.Context, string, int) ManagedMeasurement {
		return ManagedMeasurement{
			Present: true, Complete: false, Bytes: 4096,
			Gaps: []CoverageGap{
				{Code: "permission_denied", Scope: "system_managed", Detail: "denied", OmittedObjects: &one},
				{Code: "permission_denied", Scope: "system_managed", Detail: "denied", OmittedObjects: &one},
			},
		}
	})
	if plane.CollectionStatus != StatusPartial || len(plane.Entries) != 1 ||
		plane.Entries[0].Allocated == nil || plane.Entries[0].Allocated.Bound != "lower" {
		t.Fatalf("plane=%#v", plane)
	}
	if len(plane.Coverage.Gaps) != 1 || plane.Coverage.Gaps[0].OmittedObjects == nil ||
		*plane.Coverage.Gaps[0].OmittedObjects != 2 {
		t.Fatalf("gaps=%#v", plane.Coverage.Gaps)
	}
}

func TestBoundedWriterMarksTruncation(t *testing.T) {
	t.Parallel()
	var output strings.Builder
	writer := &boundedWriter{Writer: &output, Remaining: 2}
	if _, err := writer.Write([]byte("abcd")); err == nil {
		t.Fatal("expected bounded writer error")
	}
	if !writer.Exceeded || output.String() != "ab" {
		t.Fatalf("writer=%#v output=%q", writer, output.String())
	}
}

func TestExecRunnerReturnsBoundedPrefixOnTruncation(t *testing.T) {
	t.Parallel()
	output, err := (ExecRunner{MaxOutput: 2}).Run(
		context.Background(),
		"/usr/bin/printf",
		"abcd",
	)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Code != "truncated" {
		t.Fatalf("err=%v", err)
	}
	if string(output) != "ab" {
		t.Fatalf("output=%q", output)
	}
}

func TestParseSnapshotsEmpty(t *testing.T) {
	t.Parallel()
	entries, err := parseSnapshots(fixture(t, "snapshots-empty.plist"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries=%#v", entries)
	}
}

func TestManagedRootMeasuredAbsentTruncatedAndCanceled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data"), make([]byte, 8192), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "mass"), make([]byte, 8192), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	measured := MeasureManagedRoot(context.Background(), root, 100)
	if !measured.Present || !measured.Complete || measured.Bytes <= 0 {
		t.Fatalf("measured=%#v", measured)
	}
	absent := MeasureManagedRoot(context.Background(), filepath.Join(root, "absent"), 100)
	if absent.Present || !absent.Complete || absent.Bytes != 0 {
		t.Fatalf("absent=%#v", absent)
	}
	truncated := MeasureManagedRoot(context.Background(), root, 1)
	if truncated.Complete || len(truncated.Gaps) == 0 || truncated.Gaps[0].Code != "truncated" {
		t.Fatalf("truncated=%#v", truncated)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := MeasureManagedRoot(ctx, root, 100)
	if canceled.Complete || len(canceled.Gaps) == 0 || canceled.Gaps[0].Code != "timeout" {
		t.Fatalf("canceled=%#v", canceled)
	}
}

func TestMandatoryFilesystemFailureFailsCollection(t *testing.T) {
	t.Parallel()
	_, err := (Collector{
		Platform: "linux",
		FilesystemSampler: func(context.Context, string) (FilesystemSample, error) {
			return FilesystemSample{}, errors.New("no filesystem")
		},
	}).Collect(context.Background(), Request{
		TargetPath: "/target",
		Tool:       ToolIdentity{Name: "spanwit", Version: "test"},
	})
	if err == nil || !strings.Contains(err.Error(), "mandatory filesystem observation") {
		t.Fatalf("err=%v", err)
	}
}
