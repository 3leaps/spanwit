package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/inventory"
	"github.com/3leaps/spanwit/internal/space"
)

func TestSpaceObservationFlagsAndModeExclusion(t *testing.T) {
	for name, want := range map[string]string{"workers": "auto", "backend": "auto", "include-remote": "false", "stall-timeout": "1m0s"} {
		cmd := newSpaceCmd(inventoryTestIdentity())
		flag := cmd.Flags().Lookup(name)
		if flag == nil || flag.DefValue != want {
			t.Fatalf("%s: %+v", name, flag)
		}
		if err := cmd.Flags().Set(name, want); err != nil {
			t.Fatal(err)
		}
		changed := changedFlags(cmd, "workers", "backend", "include-remote", "stall-timeout")
		for _, compare := range []bool{false, true} {
			var stdout, stderr bytes.Buffer
			opts := spaceRunOpts{format: "json", capacityOnly: !compare, changedInventoryFlags: changed}
			if compare {
				opts.comparePaths = []string{"left", "right"}
				opts.changedCompareFlags = changed
			}
			if code := runSpace(inventoryTestIdentity(), &stdout, &stderr, opts); code != 3 || stdout.Len() != 0 {
				t.Fatalf("%s compare=%v: code=%d stdout=%q stderr=%q", name, compare, code, stdout.String(), stderr.String())
			}
		}
	}
}

func TestSpaceObservationUsageRejectsBeforeAnalysis(t *testing.T) {
	orig := spaceAnalyze
	t.Cleanup(func() { spaceAnalyze = orig })
	spaceAnalyze = func(context.Context, space.Options) (space.Report, error) {
		t.Fatal("invalid options reached filesystem analysis")
		return space.Report{}, nil
	}
	for _, opts := range []spaceRunOpts{
		{workers: "-1"}, {workers: "0"}, {workers: "wat"}, {backend: "wat"},
		{backend: "serial", workers: "4"}, {stallTimeout: -time.Second},
	} {
		opts.format = "json"
		var stdout, stderr bytes.Buffer
		if code := runSpace(inventoryTestIdentity(), &stdout, &stderr, opts); code != 3 || stdout.Len() != 0 {
			t.Fatalf("%+v code=%d stderr=%q", opts, code, stderr.String())
		}
	}
}

func TestSpaceObservationWiringAndPartialExit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	for _, tc := range []struct {
		flag, want time.Duration
		remote     bool
	}{{inventory.DefaultStallTimeout, inventory.DefaultStallTimeout, false}, {0, -1, true}, {time.Second, time.Second, true}} {
		orig := spaceAnalyze
		var got space.Options
		spaceAnalyze = func(ctx context.Context, opts space.Options) (space.Report, error) {
			got = opts
			if opts.OnStall == nil {
				t.Fatal("stall alert missing")
			}
			opts.OnStall(inventory.StallAlert{Pending: 1, Waited: time.Second, Timeout: tc.flag})
			report, err := orig(ctx, opts)
			report.Warnings = append(report.Warnings, "hotspots observation coverage: remote=1; wholly_unmeasured=1 (partial)")
			report.Completion = space.NewCompletion(report.Warnings)
			return report, err
		}
		var stdout, stderr bytes.Buffer
		code := runSpace(inventoryTestIdentity(), &stdout, &stderr, spaceRunOpts{
			path: root, format: "json", maxDepth: 2, workers: "1", backend: "serial",
			stallTimeout: tc.flag, includeRemote: tc.remote,
		})
		spaceAnalyze = orig
		if code != 1 || got.Workers != 1 || got.Backend != "serial" || got.IncludeRemote != tc.remote || got.StallTimeout != tc.want {
			t.Fatalf("code=%d options=%+v stderr=%q", code, got, stderr.String())
		}
		if !strings.Contains(stdout.String(), `"lifecycle": "partial"`) || !strings.Contains(stderr.String(), "backend=serial workers=1") {
			t.Fatalf("partial/backend undisclosed: stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		for _, line := range strings.Split(stderr.String(), "\n") {
			if strings.Contains(line, "directory open(s) not responding") && strings.Contains(line, root) {
				t.Fatalf("alert disclosed path: %q", line)
			}
		}
		if !strings.Contains(stderr.String(), "directory open(s) not responding") {
			t.Fatal("missing alert")
		}
	}
}
