package space

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/engine"
)

// A relocated Cargo target is one verified row: the name-shaped directories
// inside it (debug/build) are not re-listed as unverified, while an unrelated
// name-shaped directory still is.
func TestSpace_RelocatedCargoSuppressesInnerUnverified(t *testing.T) {
	realCacheDir, cacheErr := os.UserCacheDir() // before HOME is overridden
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GOCACHE", "")
	tmp := t.TempDir()
	if engine.UnderSharedTemp(tmp) {
		// Linux CI: t.TempDir() is under /tmp, where relocated caches are
		// diagnostic only. Build the fixture under the user cache dir instead.
		base, err := realCacheDir, cacheErr
		if err != nil || engine.UnderSharedTemp(base) {
			t.Skip("no non-shared fixture location")
		}
		if err := os.MkdirAll(base, 0o700); err != nil {
			t.Skip("user cache dir not writable")
		}
		tmp, err = os.MkdirTemp(base, "spanwit-relocated-test-")
		if err != nil {
			t.Skip("user cache dir not writable")
		}
		t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	}
	reloc := filepath.Join(tmp, "rsfulmen-pr30-devrev-target")
	if err := os.MkdirAll(reloc, 0o755); err != nil {
		t.Fatal(err)
	}
	tag := "Signature: 8a477f597d28d172789f06886806bc55\n# This file is a cache directory tag created by cargo.\n"
	if err := os.WriteFile(filepath.Join(reloc, "CACHEDIR.TAG"), []byte(tag), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(reloc, "debug", "build", "x", "out.o"), 4096)
	writeFile(t, filepath.Join(tmp, "other", "build", "blob"), 4096)

	report, err := Analyze(context.Background(), Options{
		DisableTempPlanes: true,
		Path:              tmp,
		MinSize:           "1K",
		Top:               10,
		MaxDepth:          6,
		Now:               time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	found := false
	for _, c := range report.Verified.Candidates {
		if c.Path == reloc {
			found = true
		}
	}
	if !found {
		t.Fatalf("relocated target should be verified: %+v", report.Verified.Candidates)
	}
	sawOther := false
	for _, u := range report.Unverified.Entries {
		if strings.HasPrefix(u.Path, reloc+string(filepath.Separator)) {
			t.Fatalf("inner path of a verified relocated target listed as unverified: %s", u.Path)
		}
		if u.Path == filepath.Join(tmp, "other", "build") {
			sawOther = true
		}
	}
	if !sawOther {
		t.Fatalf("unrelated name-shaped dir should stay unverified: %+v", report.Unverified.Entries)
	}
}
