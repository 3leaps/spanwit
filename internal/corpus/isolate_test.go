package corpus

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestIsolatingExclusions_ExcludesEverySiblingButKeep(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"workspace2", ".timemachine", "Macintosh HD", "other"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rules, err := IsolatingExclusions(parent, filepath.Join(parent, "workspace2"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(rules)
	want := []string{"./.timemachine", "./Macintosh HD", "./other"}
	if len(rules) != len(want) {
		t.Fatalf("rules=%v want %v", rules, want)
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Fatalf("rules=%v want %v", rules, want)
		}
	}
}

func TestIsolatingExclusions_RejectsNonChildKeep(t *testing.T) {
	parent := t.TempDir()
	if _, err := IsolatingExclusions(parent, filepath.Join(parent, "a", "b")); err == nil {
		t.Fatal("nested keep must be rejected")
	}
	if _, err := IsolatingExclusions(parent, t.TempDir()); err == nil {
		t.Fatal("keep outside parent must be rejected")
	}
}
