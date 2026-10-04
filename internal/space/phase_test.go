package space

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAnalyze_PhaseOrderPressureHotspotsBeforeVerified(t *testing.T) {
	tmp := t.TempDir()
	// Deep-ish tree under analysis root so verified phase has work.
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a.o"), 2048)
	for i := 0; i < 50; i++ {
		_ = os.MkdirAll(filepath.Join(tmp, "bulk", filepath.Join(stringsJoin(i)...)), 0o755)
	}

	var phases []string
	_, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1K",
		Top:               10,
		IncludeHomeCaches: true,
		DisableRecipes:    true, // skip recipes phase for simpler order assert
		MaxDepth:          8,
		Now:               time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		OnPhase: func(phase string) {
			phases = append(phases, phase)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx := func(name string) int {
		for i, p := range phases {
			if p == name {
				return i
			}
		}
		return -1
	}
	ip, ih, iv := idx(PhasePressure), idx(PhaseHotspots), idx(PhaseVerified)
	if ip < 0 || ih < 0 || iv < 0 {
		t.Fatalf("missing phases: %v", phases)
	}
	if ip >= ih || ih >= iv {
		t.Fatalf("expected pressure < hotspots < verified, got %v", phases)
	}
	iu, iunk := idx(PhaseUnverified), idx(PhaseUnknown)
	if iu >= 0 && iv >= 0 && iv >= iu {
		t.Fatalf("verified should precede unverified: %v", phases)
	}
	if iunk >= 0 && iu >= 0 && iu >= iunk {
		t.Fatalf("unverified should precede unknown: %v", phases)
	}
}

// stringsJoin builds a nested path segment list for bulk dirs.
func stringsJoin(n int) []string {
	// shallow unique dirs
	return []string{"d" + itoa(n)}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
