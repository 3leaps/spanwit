package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
)

// Temp roots the shipped crisis profile must never enable. Comments in the
// YAML are not the control; the parsed profile is.
func tempPlaneRoots() []string {
	roots := []string{"/tmp", "/private/tmp", "/var/folders", "/private/var/folders"}
	if tmp := os.TempDir(); tmp != "" {
		roots = append(roots, filepath.Clean(tmp))
	}
	return roots
}

func underAnyTempRoot(p string) bool {
	p = filepath.Clean(expandHome(p))
	for _, r := range tempPlaneRoots() {
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func loadCrisisProfile(t *testing.T, path string) *Config {
	t.Helper()
	identity := &appidentity.Identity{BinaryName: "spanwit", Vendor: "fulmenhq", EnvPrefix: "SPANWIT_", ConfigName: "spanwit"}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(context.Background(), identity, logger, path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return cfg
}

const shippedCrisisProfile = "../../docs/examples/configs/crisis-dev.yaml"

func TestShippedCrisisProfile_EnablesNoTempPlanePath(t *testing.T) {
	cfg := loadCrisisProfile(t, shippedCrisisProfile)
	for _, p := range cfg.Paths {
		if p.IsEnabled() && underAnyTempRoot(p.Path) {
			t.Fatalf("shipped crisis profile enables temp-plane path %q", p.Path)
		}
	}
}

// The commented template must stay valid config when copied, and even then
// it is disabled and carries an age gate (temp snapshots may be live).
func TestShippedCrisisProfile_TempTemplateParsesInertAndAgeGated(t *testing.T) {
	raw, err := os.ReadFile(shippedCrisisProfile)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	inTemplate := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "#  - path: ") {
			inTemplate = true
		}
		if inTemplate && strings.HasPrefix(line, "#  ") {
			line = strings.TrimPrefix(line, "#")
		}
		out = append(out, line)
	}
	if !inTemplate {
		t.Fatal("temp-plane template missing from shipped crisis profile")
	}
	copied := filepath.Join(t.TempDir(), "crisis-copy.yaml")
	if err := os.WriteFile(copied, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadCrisisProfile(t, copied)
	found := false
	for _, p := range cfg.Paths {
		if !underAnyTempRoot(p.Path) {
			continue
		}
		found = true
		if p.IsEnabled() {
			t.Errorf("template path %q must ship enabled: false", p.Path)
		}
		if p.MinAge == "" {
			t.Errorf("template path %q must carry an age gate", p.Path)
		}
	}
	if !found {
		t.Fatal("uncommented template produced no temp-plane path")
	}
}
