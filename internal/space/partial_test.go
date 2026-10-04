package space

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAnalyze_OnPartialBeforeVerified(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "a"), 2048)

	var mu sync.Mutex
	var partialSeen bool
	var verifiedAfterPartial bool
	verifiedGate := make(chan struct{})

	// Unblock verified only after partial is observed.
	go func() {
		for {
			mu.Lock()
			ok := partialSeen
			mu.Unlock()
			if ok {
				close(verifiedGate)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	_, err := Analyze(context.Background(), Options{
		Path:              tmp,
		MinSize:           "1K",
		IncludeHomeCaches: true,
		DisableRecipes:    true,
		MaxDepth:          6,
		Now:               time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		OnPartial: func(u PartialUpdate) {
			if u.Phase == PhaseHotspots {
				mu.Lock()
				partialSeen = true
				mu.Unlock()
			}
		},
		OnPhase: func(phase string) {
			if phase == PhaseVerified {
				mu.Lock()
				if !partialSeen {
					mu.Unlock()
					t.Error("verified phase started before OnPartial hotspots")
					return
				}
				verifiedAfterPartial = true
				mu.Unlock()
				<-verifiedGate // wait until test goroutine confirms partial
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !partialSeen || !verifiedAfterPartial {
		t.Fatalf("partialSeen=%v verifiedAfterPartial=%v", partialSeen, verifiedAfterPartial)
	}
}

// Critical budget host-dependent coverage moved to TestAnalyze_CriticalBudgetForced
// (ForcePressureLevel) and pure ApplyCriticalWorkBudget unit tests.
