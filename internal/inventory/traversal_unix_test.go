//go:build unix

package inventory

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoveredRemoteRootNeverOpenedWithoutOptIn(t *testing.T) {
	cloud := t.TempDir()
	mustWrite(t, filepath.Join(cloud, "file"), 123)
	info, err := os.Stat(cloud)
	if err != nil {
		t.Fatal(err)
	}
	origDataless, origOpen := datalessDir, openDir
	datalessDir = func(got os.FileInfo) bool { return os.SameFile(got, info) }
	var opens atomic.Int32
	openDir = func(p string) (*os.File, error) { opens.Add(1); return os.Open(p) }
	t.Cleanup(func() { datalessDir, openDir = origDataless, origOpen })
	for _, tc := range []struct {
		discovered, include bool
		wantBytes           int64
	}{{true, false, 0}, {true, true, 123}, {false, false, 123}} {
		opens.Store(0)
		sink := &memorySink{}
		summary, err := Run(context.Background(), Options{Roots: []string{cloud}, EmissionMode: EmissionSummaryOnly,
			IncludeRemote: tc.include, Traversal: &TraversalOptions{MaxDepth: -1, DiscoveredRoots: tc.discovered}}, sink)
		if err != nil || summary.MatchedApparentBytes != tc.wantBytes {
			t.Fatalf("%+v: %+v %v", tc, summary, err)
		}
		if tc.wantBytes == 0 {
			if opens.Load() != 0 || *summary.Roots[0].Observed || summary.RemoteSkipCount != 1 || len(sink.gaps) != 1 {
				t.Fatalf("discovered remote opened/mismeasured: opens=%d summary=%+v", opens.Load(), summary)
			}
		} else if opens.Load() == 0 || !*summary.Roots[0].Observed {
			t.Fatalf("opt-in not walked: %+v", tc)
		}
	}
}

func TestObservationNeverOpenedStallVersusMeasuredPartial(t *testing.T) {
	root := t.TempDir()
	stuck := filepath.Join(root, "stuck")
	mustWrite(t, filepath.Join(stuck, "file"), 100)
	mustWrite(t, filepath.Join(root, "shallow"), 7)
	release := blockOpen(t, stuck)
	defer release()
	for _, p := range []string{stuck, root} {
		sink := &memorySink{}
		summary, err := Run(context.Background(), Options{Roots: []string{p}, EmissionMode: EmissionSummaryOnly,
			StallTimeout: 30 * time.Millisecond, Traversal: &TraversalOptions{MaxDepth: -1, DiscoveredRoots: true}}, sink)
		if err != nil || summary.StalledCount != 1 || summary.Lifecycle != LifecyclePartial {
			t.Fatalf("stall: %+v %v", summary, err)
		}
		wantObserved := p == root
		if *summary.Roots[0].Observed != wantObserved {
			t.Fatalf("measured/unknown confused for %s: %+v", p, summary)
		}
		if wantObserved && summary.MatchedApparentBytes != 7 {
			t.Fatalf("partial sibling bytes lost: %+v", summary)
		}
		for _, gap := range sink.gaps {
			if gap.LocalAbsolutePath != "" || gap.RelativePath != "" {
				t.Fatalf("stall disclosed path: %+v", gap)
			}
		}
	}
}

func TestDiscoveredNetworkRootChecksItsOwnDeviceBeforeOpen(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "file"), 123)
	origType, origOpen := remoteFilesystemType, openDir
	remoteFilesystemType = func(string) (string, bool) { return "nfs", true }
	var opens atomic.Int32
	openDir = func(p string) (*os.File, error) { opens.Add(1); return os.Open(p) }
	t.Cleanup(func() { remoteFilesystemType, openDir = origType, origOpen })
	for _, discovered := range []bool{true, false} {
		opens.Store(0)
		summary, err := Run(context.Background(), Options{Roots: []string{root}, EmissionMode: EmissionSummaryOnly,
			Traversal: &TraversalOptions{MaxDepth: -1, DiscoveredRoots: discovered}}, &memorySink{})
		if err != nil {
			t.Fatal(err)
		}
		if discovered {
			if opens.Load() != 0 || summary.RemoteSkipCount != 1 || *summary.Roots[0].Observed {
				t.Fatalf("automatic network root opened: %+v", summary)
			}
		} else if opens.Load() != 1 || summary.MatchedApparentBytes != 123 {
			t.Fatalf("explicit network root no longer walked: %+v", summary)
		}
	}
}

func TestTraversalDiscoversPlaceholderBeforeSkippingIt(t *testing.T) {
	root := t.TempDir()
	cloud := filepath.Join(root, "target")
	mustWrite(t, filepath.Join(cloud, "file"), 123)
	info, err := os.Stat(cloud)
	if err != nil {
		t.Fatal(err)
	}
	origDataless, origOpen := datalessDir, openDir
	datalessDir = func(got os.FileInfo) bool { return os.SameFile(got, info) }
	openDir = func(p string) (*os.File, error) {
		if filepath.Base(p) == "target" {
			t.Error("discovered placeholder was opened")
		}
		return os.Open(p)
	}
	t.Cleanup(func() { datalessDir, openDir = origDataless, origOpen })
	var found []DirectoryVisit
	summary, err := Run(context.Background(), Options{Roots: []string{root}, EmissionMode: EmissionSummaryOnly,
		Traversal: &TraversalOptions{MaxDepth: 1, OnDirectory: func(v DirectoryVisit) bool {
			found = append(found, v)
			return true
		}}}, &memorySink{})
	if err != nil || len(found) != 1 || found[0].RelativePath != "target" || summary.RemoteSkipCount != 1 {
		t.Fatalf("skipped candidate disappeared from discovery: found=%+v summary=%+v err=%v", found, summary, err)
	}
}
