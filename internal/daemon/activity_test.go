//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type activityRunner func(context.Context, string, ...string) ([]byte, error)

func (f activityRunner) Run(ctx context.Context, program string, args ...string) ([]byte, error) {
	return f(ctx, program, args...)
}

func TestParseBuildActivityLsof_PositiveOnlyAndRootBoundaries(t *testing.T) {
	roots := []string{"/work/cache", "/work/cache2"}
	raw := []byte("p1\x00cgo\x00fcwd\x00n/work/cache/pkg\x00" +
		"\np2\x00ccargo\x00f3\x00n/work/cache2/target\x00" +
		"\np3\x00crustc\x00f4\x00n/work/cache-other/nope\x00" +
		"\np4\x00cgo-tool\x00f5\x00n/work/cache/ignored\x00" +
		"\np5\x00ccargo\x00f6\x00nrelative/path\x00")
	got := parseBuildActivityLsof(raw, roots)
	want := map[string]bool{"/work/cache": true, "/work/cache2": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("activity=%v want %v", got, want)
	}
}

func TestParseBuildActivityLsof_DoesNotCarryCommandAcrossProcess(t *testing.T) {
	raw := []byte("p1\x00cgo\x00fcwd\x00n/tmp/not-a-root\x00\np2\x00f3\x00n/work/cache/file\x00")
	if got := parseBuildActivityLsof(raw, []string{"/work/cache"}); len(got) != 0 {
		t.Fatalf("stale command marked a new process active: %v", got)
	}
}

func TestParseBuildActivityLsof_RequiresProcessCommandAndFileOrder(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte("cgo\x00f3\x00n/work/cache/a\x00"),
		[]byte("p1\x00cgo\x00n/work/cache/a\x00"),
		[]byte("p1\x00fcwd\x00n/work/cache/a\x00"),
		[]byte("p1\x00cgo\x00fcwd\x00n\n/work/cache/a\x00"),
	} {
		if got := parseBuildActivityLsof(raw, []string{"/work/cache"}); len(got) != 0 {
			t.Fatalf("malformed field order marked activity: %q => %v", raw, got)
		}
	}
}

func TestBuildActivityProbe_ReturnsErrorAndNoActivityOnRunnerFailure(t *testing.T) {
	probe := BuildActivityProbe{Runner: activityRunner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("truncated")
	})}
	got, err := probe.Probe(context.Background(), []string{"/work/cache"})
	if err == nil || got != nil {
		t.Fatalf("got (%v, %v), want nil activity and an error", got, err)
	}
}

func TestBuildActivityProbe_CanceledContextNeverProducesActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probe := BuildActivityProbe{Runner: activityRunner(func(context.Context, string, ...string) ([]byte, error) {
		// A badly behaved runner may return output despite cancellation. The
		// probe itself must still treat that observation as unavailable.
		return []byte("p1\x00cgo\x00fcwd\x00n/work/cache/a\x00"), nil
	})}
	got, err := probe.Probe(ctx, []string{"/work/cache"})
	if err == nil || got != nil {
		t.Fatalf("got (%v, %v), want nil activity and cancellation error", got, err)
	}
}

func TestBuildActivityProbe_TruncatedOutputNeverProducesActivity(t *testing.T) {
	probe := BuildActivityProbe{Runner: activityRunner(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("p1\x00cgo\x00n/work/cache/a\x00"), errors.New("command output exceeded limit")
	})}
	got, err := probe.Probe(context.Background(), []string{"/work/cache"})
	if err == nil || got != nil {
		t.Fatalf("got (%v, %v), want nil activity and truncation error", got, err)
	}
}

func TestBuildActivityProbe_UsesBoundedFixedLsofInvocation(t *testing.T) {
	probe := BuildActivityProbe{Runner: activityRunner(func(_ context.Context, program string, args ...string) ([]byte, error) {
		if program != buildActivityLsofPath() {
			t.Fatalf("program=%q", program)
		}
		want := []string{"-nP", "-F0pcfn", "-c", "/^(go|cargo|rustc)$/"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("args=%q want %q", args, want)
		}
		return []byte("p1\x00cgo\x00fcwd\x00n/work/cache/a\x00"), nil
	})}
	got, err := probe.Probe(context.Background(), []string{"/work/cache"})
	if err != nil || !got["/work/cache"] || len(got) != 1 {
		t.Fatalf("got (%v, %v)", got, err)
	}
}

func TestBuildActivityProbe_BoundsCustomRunnerOutputAndSetsDeadline(t *testing.T) {
	probe := BuildActivityProbe{Runner: activityRunner(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > buildActivityTimeout {
			t.Fatalf("unexpected probe deadline: %v %v", deadline, ok)
		}
		return make([]byte, buildActivityMaxOutput+1), nil
	})}
	got, err := probe.Probe(context.Background(), []string{"/work/cache"})
	if err == nil || got != nil {
		t.Fatalf("got (%v, %v), want bounded-output error", got, err)
	}
}

func TestBuildActivityProbe_RejectsRelativeRoots(t *testing.T) {
	got, err := (BuildActivityProbe{}).Probe(context.Background(), []string{"cache"})
	if err == nil || got != nil {
		t.Fatalf("got (%v, %v)", got, err)
	}
}
