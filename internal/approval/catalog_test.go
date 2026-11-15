package approval

import (
	"testing"

	"github.com/3leaps/spanwit/internal/config"
)

func TestSignatureCatalogDigestStable(t *testing.T) {
	a, err := SignatureCatalogDigest()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SignatureCatalogDigest()
	if a != b || len(a) != len("sha256:")+64 {
		t.Fatalf("unstable or malformed: %s %s", a, b)
	}
}

// TestSignatureCatalogDigestCoversHiddenFields mutates a built-in-only field
// (json:"-") and requires the digest to change.
func TestSignatureCatalogDigestCoversHiddenFields(t *testing.T) {
	base, _ := catalogDigest(config.BuiltInSignatures())
	mutate := func(f func(*config.Signature)) string {
		c := config.BuiltInSignatures()
		for d, families := range c {
			for fam, sigs := range families {
				for id, s := range sigs {
					if id == "cargo-target-relocated" || len(s.ForbiddenChildPaths) > 0 {
						f(&s)
						c[d][fam][id] = s
						got, err := catalogDigest(c)
						if err != nil {
							t.Fatal(err)
						}
						return got
					}
				}
			}
		}
		t.Fatal("no signature with built-in-only fields found")
		return ""
	}
	cases := map[string]func(*config.Signature){
		"ForbiddenChildPaths": func(s *config.Signature) { s.ForbiddenChildPaths = append([]string{}, s.ForbiddenChildPaths[1:]...) },
		"AnyChildDirsOnly":    func(s *config.Signature) { s.AnyChildDirsOnly = !s.AnyChildDirsOnly },
		"MinAgeFloor":         func(s *config.Signature) { s.MinAgeFloor = "1m" },
		"ExcludeLiveGoCache":  func(s *config.Signature) { s.ExcludeLiveGoCache = !s.ExcludeLiveGoCache },
	}
	for name, f := range cases {
		if mutate(f) == base {
			t.Errorf("%s change did not change the digest", name)
		}
	}
}
