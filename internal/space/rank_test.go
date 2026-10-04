package space

import "testing"

func TestSortEntriesByBoundAwareSize_CompleteBeforeIncomplete(t *testing.T) {
	entries := []Entry{
		{Path: "/inc-big", SizeBytes: 9e9, SizeIncomplete: true},
		{Path: "/complete-small", SizeBytes: 100, SizeIncomplete: false},
		{Path: "/complete-big", SizeBytes: 200, SizeIncomplete: false},
		{Path: "/inc-small", SizeBytes: 50, SizeIncomplete: true},
	}
	SortEntriesByBoundAwareSize(entries)
	want := []string{"/complete-big", "/complete-small", "/inc-big", "/inc-small"}
	for i, p := range want {
		if entries[i].Path != p {
			t.Fatalf("order[%d]=%s want %s (full=%v)", i, entries[i].Path, p, pathsOf(entries))
		}
	}
}

func pathsOf(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Path
	}
	return out
}

func TestMinSizeThresholdOutcome(t *testing.T) {
	if got := minSizeThresholdOutcome(100, true, 1000); got != "indeterminate" {
		t.Fatalf("incomplete below floor: got %q", got)
	}
	if got := minSizeThresholdOutcome(100, false, 1000); got != "excluded" {
		t.Fatalf("complete below floor: got %q", got)
	}
	if got := minSizeThresholdOutcome(2000, false, 1000); got != "matched" {
		t.Fatalf("complete above floor: got %q", got)
	}
	if got := minSizeThresholdOutcome(50, true, 0); got != "none" {
		t.Fatalf("no floor: got %q", got)
	}
	// Incomplete lower bound already ≥ floor is matched (true size ≥ LB).
	if got := minSizeThresholdOutcome(2000, true, 1000); got != "matched" {
		t.Fatalf("incomplete LB above floor: got %q", got)
	}
}
