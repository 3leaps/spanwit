package catalog_test

import (
	"testing"

	"github.com/3leaps/spanwit/internal/catalog"
	"github.com/3leaps/spanwit/internal/config"
)

// Every catalog entry that links a signature names a real built-in
// signature, checked against the live signature set so a new signature
// cannot leave a hand-kept list stale.
func TestBuiltInSignatureLinksMatchBuiltInSignatures(t *testing.T) {
	cat, err := catalog.BuiltIn()
	if err != nil {
		t.Fatal(err)
	}
	ids := config.BuiltInSignatures().IDs()
	if err := cat.ValidateLinks(ids, nil); err != nil {
		t.Fatalf("catalog links a signature that is not built in: %v", err)
	}
	linked := map[string]bool{}
	for _, e := range cat.Entries {
		if e.Signature != "" {
			linked[e.Signature] = true
		}
	}
	for id := range ids {
		if !linked[id] {
			t.Errorf("built-in signature %s has no catalog entry", id)
		}
	}
}
