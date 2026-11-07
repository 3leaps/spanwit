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
	for _, want := range []string{"colima (identified)", "images                  unavailable (engine_error)", "disk-usage accounting failed (HTTP 500)", "not disk pressure"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
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
