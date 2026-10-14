package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/config"
	"gopkg.in/yaml.v3"
)

func tempRootPlan(t *testing.T, root string, temp bool) []PruneCandidate {
	t.Helper()
	cfg := &config.Config{Version: 1, Paths: []config.PathProfile{{
		Path: root, MaxDepth: 12, TempRoot: temp,
		Targets: []config.Target{{Signature: "development.rust.cargo-target"}},
	}}}
	plan, err := BuildPrunePlan(context.Background(), cfg)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan.Candidates
}

func cargoCheckout(t *testing.T, dir string) string {
	t.Helper()
	writeRegular(t, filepath.Join(dir, "Cargo.toml"))
	target := filepath.Join(dir, "target")
	writeRegular(t, filepath.Join(target, "debug", "x.o"))
	return target
}

// R4: under a temp root, a group- or other-writable directory is not
// descended, so a cargo target planted beneath it is absent.
func TestTempRoot_WritableChildNotDescended(t *testing.T) {
	skipUnlessUnixTemp(t)
	root := privateDir(t, 0o700)
	open := filepath.Join(root, "open")
	target := cargoCheckout(t, open)
	ageTree(t, root, 48*time.Hour)
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(open, 0o755) })
	assertAbsent(t, tempRootPlan(t, root, true), target)
	// Control: the same tree under a non-temp profile still matches.
	if got := tempRootPlan(t, root, false); len(got) == 0 {
		t.Fatal("control: a non-temp profile should still find the target")
	}
}

// R5: a fresh temp-rooted cargo target with no configured min_age is
// withheld for age (temp age floor), never prunable.
func TestTempRoot_FreshTargetWithheldForAge(t *testing.T) {
	skipUnlessUnixTemp(t)
	root := privateDir(t, 0o700)
	target := cargoCheckout(t, filepath.Join(root, "checkout"))
	jobs := tempRootPlan(t, root, true)
	n := 0
	for _, j := range jobs {
		if j.Path != target {
			continue
		}
		n++
		if j.State != CandidateStateWithheld || j.WithheldReason != WithheldReasonAge {
			t.Fatalf("fresh temp target must be withheld for age, got %+v", j)
		}
	}
	if n != 1 {
		t.Fatalf("fresh temp target must be present exactly once (got %d)", n)
	}
	// Once old enough it is prunable.
	ageTree(t, root, 48*time.Hour)
	for _, j := range tempRootPlan(t, root, true) {
		if j.Path == target && j.State != CandidateStatePrunable {
			t.Fatalf("an old temp target should be prunable, got %+v", j)
		}
	}
}

// R6: ancestor evidence stays inside the temp root. A target whose only
// Cargo.toml sits above the root is absent.
func TestTempRoot_AncestorEvidenceStaysInside(t *testing.T) {
	skipUnlessUnixTemp(t)
	root := privateDir(t, 0o700)
	writeRegular(t, filepath.Join(filepath.Dir(root), "Cargo.toml"))
	target := filepath.Join(root, "target")
	writeRegular(t, filepath.Join(target, "debug", "x.o"))
	ageTree(t, root, 48*time.Hour)
	assertAbsent(t, tempRootPlan(t, root, true), target)
}

// TempRoot is never read from config: a YAML profile that tries to set it
// leaves it false, so admission cannot be skipped by configuration.
func TestTempRoot_NotSettableFromConfig(t *testing.T) {
	var p config.PathProfile
	if err := yaml.Unmarshal([]byte("path: /x\ntemproot: true\ntemp_root: true\nTempRoot: true\n"), &p); err != nil {
		t.Fatal(err)
	}
	if p.TempRoot {
		t.Fatal("TempRoot must not be settable from config")
	}
}

// The S4a hard gates hold for candidates under an admitted temp root, for
// both new signatures and for a profile pattern target, plus the temp age
// floor on cargo.
func TestTempRoot_HardGatesHold(t *testing.T) {
	skipUnlessUnixTemp(t)
	plan := func(t *testing.T, root string, targets []config.Target) []PruneCandidate {
		t.Helper()
		cfg := &config.Config{Version: 1, Paths: []config.PathProfile{{
			Path: root, MaxDepth: 12, TempRoot: true, Targets: targets,
		}}}
		p, err := BuildPrunePlan(context.Background(), cfg)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		return p.Candidates
	}
	for name, targets := range map[string][]config.Target{
		"built-in signatures": {{Signature: r4bGrypeSig}, {Signature: r4bGocacheSig}},
		"profile patterns":    {{Pattern: "**/grype/db/grype-db-download*"}, {Pattern: "**/.gocache"}, {Pattern: "**/db/6"}},
	} {
		t.Run(name, func(t *testing.T) {
			root := privateDir(t, 0o700)
			db := filepath.Join(root, "grype", "db")
			for _, n := range []string{"5", "6"} {
				writeRegular(t, filepath.Join(db, n, "import.json"))
				writeRegular(t, filepath.Join(db, n, "vulnerability.db"))
			}
			repo := filepath.Join(root, "repo")
			writeRegular(t, filepath.Join(repo, "go.mod"))
			cache := filepath.Join(repo, ".gocache")
			write(t, filepath.Join(cache, "README"), "my notes\n")
			residue := filepath.Join(db, "grype-db-download1")
			writeRegular(t, filepath.Join(residue, "vulnerability.db"))
			ageTree(t, root, 48*time.Hour)
			now := time.Now()
			if err := os.Chtimes(filepath.Join(residue, "vulnerability.db"), now, now); err != nil {
				t.Fatal(err)
			}
			jobs := plan(t, root, targets)
			assertAbsent(t, jobs, filepath.Join(db, "6"))
			assertAbsent(t, jobs, filepath.Join(db, "5"))
			assertAbsent(t, jobs, cache)
			assertNeverPrunable(t, jobs, residue)
		})
	}
}
