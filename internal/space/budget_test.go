package space

import "testing"

func TestApplyCriticalWorkBudget_Normal(t *testing.T) {
	b := ApplyCriticalWorkBudget(PressureOK, 12)
	if b.DeepMaxDepth != 12 || b.SkipUnknown || len(b.Notes) != 0 {
		t.Fatalf("%+v", b)
	}
}

func TestApplyCriticalWorkBudget_CriticalAlwaysNotesUnknownSkip(t *testing.T) {
	// Even when max_depth already <= cap, unknown skip must be disclosed.
	b := ApplyCriticalWorkBudget(PressureCritical, 4)
	if !b.SkipUnknown {
		t.Fatal("expected SkipUnknown")
	}
	if b.DeepMaxDepth != 4 {
		t.Fatalf("depth=%d", b.DeepMaxDepth)
	}
	if len(b.Notes) == 0 {
		t.Fatal("expected note for unknown omission")
	}
	found := false
	for _, n := range b.Notes {
		if containsSub(n, "unknown") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes missing unknown disclosure: %v", b.Notes)
	}
}

func TestApplyCriticalWorkBudget_CriticalCapsHighDepth(t *testing.T) {
	b := ApplyCriticalWorkBudget(PressureCritical, 20)
	if b.DeepMaxDepth != CriticalDeepMaxDepth {
		t.Fatalf("depth=%d want %d", b.DeepMaxDepth, CriticalDeepMaxDepth)
	}
	if len(b.Notes) < 2 {
		t.Fatalf("expected unknown + cap notes: %v", b.Notes)
	}
}

func TestApplyCriticalWorkBudget_CriticalUnlimited(t *testing.T) {
	b := ApplyCriticalWorkBudget(PressureCritical, -1)
	if b.DeepMaxDepth != CriticalDeepMaxDepth {
		t.Fatalf("depth=%d", b.DeepMaxDepth)
	}
}

func containsSub(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}
