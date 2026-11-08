package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/3leaps/spanwit/internal/space"
)

func i64(n int64) *int64 { return &n }

func TestWriteContainerRuntimeText_StoppedEngineShowsHostDisk(t *testing.T) {
	rt := &space.ContainerRuntime{
		Engine:          space.EngineColima,
		Detection:       space.EngineDetection{Status: space.DetectionUnidentified, Basis: []string{space.DetectionBasisEndpointPath}, Reason: space.ReasonEngineStopped},
		GuestFilesystem: space.GuestFilesystem{Status: space.ProbeUnavailable, Basis: space.GuestBasisNone, Reason: space.ReasonEngineStopped},
		HostDisk:        &space.RuntimeHostDisk{Status: space.ProbeMeasured, Files: i64(1), LogicalBytes: i64(125 << 30), AllocatedBytes: i64(55 << 30)},
	}
	var buf bytes.Buffer
	writeContainerRuntimeText(&buf, rt)
	out := buf.String()
	for _, want := range []string{"colima (unidentified, engine_stopped)", "VM disk on the host: 55.0G allocated, 125.0G logical", "not running. Start it"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "disk-usage accounting failed") {
		t.Fatalf("no repair hint for a stopped engine:\n%s", out)
	}
}

func TestWriteContainerRuntimeText_DFFailureShowsRepairHint(t *testing.T) {
	status := 500
	fail := func(kind string) space.RuntimeBucket {
		return space.RuntimeBucket{Kind: kind, Status: space.ProbeUnavailable, Reason: space.ReasonEngineError, HTTPStatus: &status}
	}
	rt := &space.ContainerRuntime{
		Engine:          space.EngineColima,
		Detection:       space.EngineDetection{Status: space.DetectionIdentified, Basis: []string{space.DetectionBasisEndpointPath, space.DetectionBasisEngineInfo}},
		GuestFilesystem: space.GuestFilesystem{Status: space.ProbeNotProbed, Basis: space.GuestBasisNone, Reason: space.ReasonProbeNotRequested},
		Buckets:         []space.RuntimeBucket{fail(space.BucketImages), fail(space.BucketBuildCache)},
	}
	var buf bytes.Buffer
	writeContainerRuntimeText(&buf, rt)
	out := buf.String()
	for _, want := range []string{"colima (identified)", "images                  unavailable (engine_error)", "disk-usage accounting failed (HTTP 500); figures derived from it are unavailable", "If it persists"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "damage") || strings.Contains(out, "disk pressure") {
		t.Fatalf("the body is never read, so the note must not diagnose a cause:\n%s", out)
	}
	if strings.Contains(out, "VM disk on the host") || strings.Contains(out, "not running") {
		t.Fatalf("unexpected host-disk or stopped text:\n%s", out)
	}
}

func TestWriteContainerRuntimeText_DetectionHTTPStatus(t *testing.T) {
	status := 503
	rt := &space.ContainerRuntime{
		Engine:          space.EngineColima,
		Detection:       space.EngineDetection{Status: space.DetectionUnidentified, Basis: []string{space.DetectionBasisEndpointPath}, Reason: space.ReasonEngineError, HTTPStatus: &status},
		GuestFilesystem: space.GuestFilesystem{Status: space.ProbeUnavailable, Basis: space.GuestBasisNone, Reason: space.ReasonEngineError},
	}
	var buf bytes.Buffer
	writeContainerRuntimeText(&buf, rt)
	if !strings.Contains(buf.String(), "colima (unidentified, engine_error HTTP 503)") {
		t.Fatalf("got:\n%s", buf.String())
	}
}

func TestWriteContainerRuntimeText_ImageFamilies(t *testing.T) {
	f := &space.ImageFamilies{Status: space.ProbeMeasured, Families: i64(3), OrderAmbiguous: i64(1), InUseStatus: space.ProbeMeasured,
		OlderSiblings: i64(2), OlderSiblingBytes: i64(14 << 30), BytesBound: space.BoundUpper, LocallyBuilt: i64(1), InUseByExitedOnly: i64(1)}
	rt := &space.ContainerRuntime{Engine: space.EngineColima,
		Detection:       space.EngineDetection{Status: space.DetectionIdentified},
		GuestFilesystem: space.GuestFilesystem{Status: space.ProbeNotProbed, Reason: space.ReasonProbeNotRequested},
		ImageFamilies:   f,
		ImageRefs:       []space.RuntimeImageRef{{Ref: "r/a:\x1b[31mv1\u2028x", ID: "sha256:bbbbbbbbbbbb", Bucket: space.BucketOlderSibling, UniqueBytes: 1 << 30, Bound: space.BoundUpper}},
	}
	var buf bytes.Buffer
	writeContainerRuntimeText(&buf, rt)
	out := buf.String()
	for _, want := range []string{"Image families: 3 version lines with more than one build, 1 with an ambiguous order (not ranked)",
		"Older builds not used by any container: 2, ≤14.0G (1 built locally: removal means a rebuild)",
		"used only by stopped containers: 1", "sha256:bbbbbbbbbbbb"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "\x1b\u2028") {
		t.Fatalf("control or separator characters reached the terminal:\n%q", out)
	}

	f.InUseStatus, f.InUseReason, f.OlderSiblings = space.ProbeUnavailable, space.ReasonEngineError, nil
	buf.Reset()
	writeContainerRuntimeText(&buf, rt)
	if !strings.Contains(buf.String(), "not ranked; containers could not be listed (engine_error)") {
		t.Fatalf("got:\n%s", buf.String())
	}
}
