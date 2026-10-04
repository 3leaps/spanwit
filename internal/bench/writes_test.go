package bench

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWitnessSeparatesAttributableFromAmbient covers the split that keeps a
// browser's cache writes from being charged to this tool.
func TestWitnessSeparatesAttributableFromAmbient(t *testing.T) {
	root := t.TempDir()
	ownedDir := filepath.Join(root, "spanwit")
	otherDir := filepath.Join(root, "some-other-app")
	for _, d := range []string{ownedDir, otherDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	w, err := NewWriteWitness([]string{root}, []string{ownedDir})
	if err != nil {
		t.Fatalf("new witness: %v", err)
	}

	if err := os.WriteFile(filepath.Join(ownedDir, "cache.db"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write owned: %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "Code Cache"), []byte("y"), 0o600); err != nil {
		t.Fatalf("write other: %v", err)
	}

	report, err := w.Check()
	if err != nil {
		t.Fatalf("check: %v", err)
	}

	if len(report.Attributable) != 1 {
		t.Fatalf("expected 1 attributable write, got %d: %v", len(report.Attributable), report.Attributable)
	}
	if filepath.Base(report.Attributable[0].Path) != "cache.db" {
		t.Errorf("wrong file attributed: %s", report.Attributable[0].Path)
	}
	if report.Ambient != 1 {
		t.Errorf("expected 1 ambient change, got %d", report.Ambient)
	}
	if report.Clean() {
		t.Error("a witness with an attributable write reported clean")
	}
}

// TestWitnessIgnoresItsOwnStreams covers the case that produced a false
// finding in a real sweep: the harness's stderr redirected into the working
// directory it was witnessing.
func TestWitnessIgnoresItsOwnStreams(t *testing.T) {
	root := t.TempDir()

	logPath := filepath.Join(root, "sweep.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer func() { _ = logFile.Close() }()

	w, err := NewWriteWitness([]string{root}, []string{root})
	if err != nil {
		t.Fatalf("new witness: %v", err)
	}
	w.IgnoreStreams(logFile)

	if _, err := logFile.WriteString("progress\n"); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := logFile.Sync(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// A genuine write, which must still be caught.
	if err := os.WriteFile(filepath.Join(root, "stray.tmp"), []byte("z"), 0o600); err != nil {
		t.Fatalf("write stray: %v", err)
	}

	report, err := w.Check()
	if err != nil {
		t.Fatalf("check: %v", err)
	}

	for _, v := range report.Attributable {
		if filepath.Base(v.Path) == "sweep.log" {
			t.Error("the harness's own stream was reported as an attributable write")
		}
	}
	var foundStray bool
	for _, v := range report.Attributable {
		if filepath.Base(v.Path) == "stray.tmp" {
			foundStray = true
		}
	}
	if !foundStray {
		t.Error("stream exclusion also silenced a genuine write")
	}
}
