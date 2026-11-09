package cmd

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/space"
)

func rebootClaim(n int64, basis, bound string) *capacity.ByteClaim {
	return &capacity.ByteClaim{Status: capacity.StatusMeasured, Bytes: &n, Human: capacity.HumanBytes(n), Basis: basis, Bound: bound}
}

func rebootBlock(swap capacity.RebootSwap, held capacity.RebootHeldOpen, status string) *capacity.RebootReleasable {
	return &capacity.RebootReleasable{
		Status:     status,
		Semantics:  capacity.RebootSemanticsObservedGross,
		Swap:       swap,
		HeldOpen:   held,
		TempPlanes: capacity.RebootTempPlanes{Disposition: capacity.RebootTempMayBeCleared},
		Purgeable:  capacity.RebootPurgeable{Status: capacity.StatusUnavailable, Reason: capacity.RebootReasonNoUnprivilegedSrc},
	}
}

var (
	swapTarget = capacity.RebootSwap{Status: capacity.StatusMeasured, Scope: capacity.RebootScopeTargetContainer,
		Bytes: rebootClaim(6<<30, capacity.BasisVMSwapUsageTotal, capacity.BoundExact)}
	swapUnknown  = capacity.RebootSwap{Status: capacity.StatusMeasured, Scope: capacity.RebootScopeUnknown}
	swapOther    = capacity.RebootSwap{Status: capacity.RebootSwapNotReleasable, Reason: capacity.RebootReasonNotBootContainer, Scope: capacity.RebootScopeOtherContainer}
	swapStatic   = capacity.RebootSwap{Status: capacity.RebootSwapNotReleasable, Reason: capacity.RebootReasonStaticSwap, Scope: capacity.RebootScopeUnknown}
	swapFailed   = capacity.RebootSwap{Status: capacity.StatusUnavailable, Reason: capacity.RebootReasonProbeFailed, Scope: capacity.RebootScopeTargetContainer}
	swapUnsupp   = capacity.RebootSwap{Status: capacity.StatusUnsupported, Scope: capacity.RebootScopeUnknown}
	heldNotReq   = capacity.RebootHeldOpen{Status: capacity.StatusNotRequested, ScopeFilter: capacity.RebootScopeTargetContainer}
	heldMeasured = capacity.RebootHeldOpen{Status: capacity.StatusMeasured, ScopeFilter: capacity.RebootScopeTargetContainer,
		LogicalBytes: rebootClaim(1<<30, "lsof_file_size_dedup_device_inode", capacity.BoundLower)}
	heldPartialNoFigure = capacity.RebootHeldOpen{Status: capacity.StatusPartial, ScopeFilter: capacity.RebootScopeTargetContainer}
	heldUnsupported     = capacity.RebootHeldOpen{Status: capacity.StatusUnsupported, ScopeFilter: capacity.RebootScopeTargetContainer}
)

func rebootText(r *capacity.RebootReleasable) string {
	var buf bytes.Buffer
	writeRebootReleasableText(&buf, r)
	return buf.String()
}

// Golden wording. A figure is never combined with another and never worded
// as a return; held-open is always labelled logical.
func TestWriteRebootReleasableText_Golden(t *testing.T) {
	got := rebootText(rebootBlock(swapTarget, heldMeasured, capacity.StatusMeasured))
	want := "Reboot-releasable (observed now; not a guaranteed gain, swap may be re-created after boot):\n" +
		"  swap reserved          " + capacity.HumanBytes(6<<30) + " (this container; basis vm.swapusage total)\n" +
		"  held-open, deleted     ≥" + capacity.HumanBytes(1<<30) + " (logical; may share blocks; not counted as allocation)\n" +
		"  temp planes            not measured in this view (see spanwit space)\n" +
		"  purgeable              unavailable\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestWriteRebootReleasableText_EveryStateWithoutPromises(t *testing.T) {
	cases := []struct {
		swap     capacity.RebootSwap
		held     capacity.RebootHeldOpen
		wantSwap string
		wantHeld string
	}{
		{swapTarget, heldMeasured, "(this container; basis vm.swapusage total)", "logical; may share blocks"},
		{swapUnknown, heldNotReq, "not attributed (container unknown)", "not scanned (add --held-open)"},
		{swapOther, heldPartialNoFigure, "none (swap is on another container)", "partial (no figure for this container)"},
		{swapStatic, heldUnsupported, "none (swap is static on this platform)", "unsupported"},
		{swapFailed, heldNotReq, "unavailable (probe failed)", "not scanned"},
		{swapUnsupp, heldUnsupported, "unsupported", "unsupported"},
	}
	for _, tc := range cases {
		out := rebootText(rebootBlock(tc.swap, tc.held, capacity.StatusPartial))
		if !strings.Contains(out, tc.wantSwap) || !strings.Contains(out, tc.wantHeld) {
			t.Fatalf("swap %+v / held %+v:\n%s", tc.swap, tc.held, out)
		}
		// Startup-reallocation counterexample: nothing here may read as a
		// net or combined return.
		// "vm.swapusage total" names the sysctl field (the reservation, not
		// "used"); any other "total" would be a combined figure.
		lower := strings.ReplaceAll(strings.ToLower(out), "vm.swapusage total", "vm.swapusage")
		for _, banned := range []string{"would return", "at least", "total", "returnable", "will free", "frees"} {
			if strings.Contains(lower, banned) {
				t.Fatalf("banned wording %q:\n%s", banned, out)
			}
		}
		if tc.swap.Bytes == nil && strings.Contains(out, "GiB (this container") {
			t.Fatalf("figure printed without attribution:\n%s", out)
		}
	}
}

// Default space prints only a pointer, only at critical, with no figure.
func TestWriteRebootPointer_CriticalOnlyNoFigure(t *testing.T) {
	for level, want := range map[string]bool{space.PressureCritical: true, space.PressureWarn: false, space.PressureOK: false} {
		var buf bytes.Buffer
		writeRebootPointer(&buf, level)
		out := buf.String()
		if want != (out != "") {
			t.Fatalf("level %s: %q", level, out)
		}
		if strings.ContainsAny(out, "0123456789") {
			t.Fatalf("pointer carries a figure: %q", out)
		}
	}
}

// Default space at critical: the pointer line appears and no capacity
// probe runs (default space never builds a capacity collector).
func TestWriteSpaceText_CriticalShowsRebootPointer(t *testing.T) {
	var buf bytes.Buffer
	writeSpaceText(&buf, space.Report{
		Root: "/tmp/root", MinSize: "none", Top: 5, MaxDepth: 2,
		Pressure: space.Pressure{Level: space.PressureCritical, TotalHuman: "1G", UsedHuman: "1G", AvailHuman: "0B"},
	}, false)
	if !strings.Contains(buf.String(), "Reboot-releasable space: see spanwit space --capacity-only") {
		t.Fatalf("pointer missing:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "swap reserved") {
		t.Fatal("default space must not print the capacity block")
	}
}

// Every state the derivation can produce validates against the signed v2
// schema, and the presence rules reject misattached figures.
func TestCapacityJSON_RebootStatesAgainstSchema(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	report, err := space.AnalyzeCapacityOnly(context.Background(), space.CapacityOptions{
		Path: home,
		Collector: capacity.Collector{
			Platform: "linux", Architecture: "amd64",
			Now: func() time.Time { return time.Date(2026, 11, 9, 20, 0, 0, 0, time.UTC) },
			FilesystemSampler: func(_ context.Context, path string) (capacity.FilesystemSample, error) {
				return capacity.FilesystemSample{Path: path, Mount: "/", VolumeID: "fs:1", FSType: "ext4",
					Total: 1_000_000, Used: 600_000, Available: 400_000}, nil
			},
		},
		Tool: capacity.ToolIdentity{Name: "spanwit", Version: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]*capacity.RebootReleasable{
		"target":  rebootBlock(swapTarget, heldMeasured, capacity.StatusMeasured),
		"unknown": rebootBlock(swapUnknown, heldNotReq, capacity.StatusPartial),
		"other":   rebootBlock(swapOther, heldPartialNoFigure, capacity.StatusPartial),
		"static":  rebootBlock(swapStatic, heldUnsupported, capacity.StatusMeasured),
		"failed":  rebootBlock(swapFailed, heldNotReq, capacity.StatusPartial),
		"unsupp":  rebootBlock(swapUnsupp, heldUnsupported, capacity.StatusUnsupported),
	}
	for name, block := range valid {
		report.CapacityAccounting.RebootReleasable = block
		if err := writeCapacityJSON(&bytes.Buffer{}, report); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	unknownWithBytes := swapUnknown
	unknownWithBytes.Bytes = swapTarget.Bytes
	targetNoBytes := swapTarget
	targetNoBytes.Bytes = nil
	otherWithBytes := swapOther
	otherWithBytes.Bytes = swapTarget.Bytes
	measuredWithReason := swapTarget
	measuredWithReason.Reason = capacity.RebootReasonProbeFailed
	failedNoReason := swapFailed
	failedNoReason.Reason = ""
	wrongBasis := swapTarget
	wrongBasis.Bytes = rebootClaim(6<<30, "physical_blocks", capacity.BoundExact)
	heldNoFigure := heldMeasured
	heldNoFigure.LogicalBytes = nil
	heldNotReqWithFigure := heldNotReq
	heldNotReqWithFigure.LogicalBytes = heldMeasured.LogicalBytes
	invalid := map[string]*capacity.RebootReleasable{
		"unknown scope with bytes":        rebootBlock(unknownWithBytes, heldNotReq, capacity.StatusPartial),
		"target scope without bytes":      rebootBlock(targetNoBytes, heldNotReq, capacity.StatusMeasured),
		"other container with bytes":      rebootBlock(otherWithBytes, heldNotReq, capacity.StatusMeasured),
		"measured swap with reason":       rebootBlock(measuredWithReason, heldNotReq, capacity.StatusMeasured),
		"unavailable swap without reason": rebootBlock(failedNoReason, heldNotReq, capacity.StatusPartial),
		"swap bytes on another basis":     rebootBlock(wrongBasis, heldNotReq, capacity.StatusMeasured),
		"measured held-open, no figure":   rebootBlock(swapTarget, heldNoFigure, capacity.StatusMeasured),
		"not-requested held-open figure":  rebootBlock(swapTarget, heldNotReqWithFigure, capacity.StatusMeasured),
	}
	for name, block := range invalid {
		report.CapacityAccounting.RebootReleasable = block
		if err := writeCapacityJSON(&bytes.Buffer{}, report); err == nil {
			t.Fatalf("%s: schema accepted it", name)
		}
	}
	semantics := rebootBlock(swapTarget, heldNotReq, capacity.StatusMeasured)
	semantics.Semantics = "net_gain"
	report.CapacityAccounting.RebootReleasable = semantics
	if err := writeCapacityJSON(&bytes.Buffer{}, report); err == nil {
		t.Fatal("semantics const not enforced")
	}
}
