package space

import (
	"strings"
	"testing"
)

func TestPlacementNotes_OnlyOnPressure(t *testing.T) {
	if notes := PlacementNotes(Pressure{Level: PressureOK}, nil); len(notes) != 0 {
		t.Fatalf("ok pressure should have no notes: %v", notes)
	}
	notes := PlacementNotes(Pressure{Level: PressureCritical, AvailBytes: 1e9, VolumeID: "a"}, &Pressure{
		Level: PressureOK, AvailBytes: 50e9, VolumeID: "b",
	})
	if len(notes) < 3 {
		t.Fatalf("expected placement notes, got %v", notes)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "CARGO_TARGET_DIR") {
		t.Fatalf("missing CARGO_TARGET_DIR guidance: %s", joined)
	}
	if !strings.Contains(joined, "never moves") {
		t.Fatalf("missing advice-only wording: %s", joined)
	}
	if !strings.Contains(joined, "Analysis root volume has more free space") {
		t.Fatalf("missing roomier-volume note: %s", joined)
	}
}
