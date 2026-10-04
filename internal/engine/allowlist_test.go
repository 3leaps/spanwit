package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanFromAllowlist_OnlyExactPaths(t *testing.T) {
	tmp := t.TempDir()
	// Valid cargo target
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a.o"), 2048)
	// Sibling name-shaped only (no cargo) — must not appear unless listed
	writeFile(t, filepath.Join(tmp, "other", "target", "b"), 2048)
	// Extra cargo target not on allowlist
	writeFile(t, filepath.Join(tmp, "skip", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "skip", "target", "x"), 4096)

	allow := []string{filepath.Join(tmp, "app", "target")}
	plan, err := PlanFromAllowlist(context.Background(), allow)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("candidates=%d want 1: %+v", len(plan.Candidates), plan.Candidates)
	}
	if plan.Candidates[0].State != CandidateStatePrunable {
		t.Fatalf("state=%s", plan.Candidates[0].State)
	}
	if plan.Candidates[0].Path != filepath.Join(tmp, "app", "target") {
		t.Fatalf("path=%s", plan.Candidates[0].Path)
	}
}

func TestPlanFromAllowlist_RejectsUnverified(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "other", "target", "b"), 2048)
	plan, err := PlanFromAllowlist(context.Background(), []string{filepath.Join(tmp, "other", "target")})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 0 {
		t.Fatalf("unverified path must not enter plan: %+v", plan.Candidates)
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("expected warning for non-matching allowlist path")
	}
}

func TestPlanFromAllowlist_MismatchedRootDoesNotPullSiblings(t *testing.T) {
	tmp := t.TempDir()
	// cargo-target requires a cargo-like child (debug/release/…)
	writeFile(t, filepath.Join(tmp, "a", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "a", "target", "debug", "x"), 100)
	writeFile(t, filepath.Join(tmp, "b", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "b", "target", "debug", "y"), 200)

	plan, err := PlanFromAllowlist(context.Background(), []string{filepath.Join(tmp, "a", "target")})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("got %d candidates (%v warnings)", len(plan.Candidates), plan.Warnings)
	}
	if plan.Candidates[0].Path != filepath.Join(tmp, "a", "target") {
		t.Fatal(plan.Candidates[0].Path)
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if size > 0 {
		if _, err := f.Write(make([]byte, size)); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
