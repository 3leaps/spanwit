//go:build darwin

package capacity

import (
	"context"
	"strings"
	"testing"
)

func TestDefaultBootMetadataDarwinIsAvailableAndStable(t *testing.T) {
	first := defaultBootMetadata(context.Background())
	second := defaultBootMetadata(context.Background())
	if first.ID == "" || first.Time.IsZero() {
		t.Fatalf("boot metadata unavailable: %#v", first)
	}
	if !strings.HasPrefix(first.ID, "darwin-utmpx:") {
		t.Fatalf("boot id=%q", first.ID)
	}
	if first.ID != second.ID || !first.Time.Equal(second.Time) {
		t.Fatalf("boot observation changed: first=%#v second=%#v", first, second)
	}
}
