package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
)

func scanTestIdentity(t *testing.T) *appidentity.Identity {
	t.Helper()
	identity := &appidentity.Identity{
		BinaryName: "spanwit",
		EnvPrefix:  "SPANWIT_",
		ConfigName: "spanwit",
	}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatalf("logger init failed: %v", err)
	}
	configPath = ""
	Initialize(context.Background(), identity, logger)
	return identity
}

// TestRunScan_ContextVerifiedVsNameShaped is the core alignment guarantee: a
// Rust target/ with Cargo.toml evidence is reported (context-verified), while a
// build/ inside vendored deps and a bare node_modules — both name-shaped, no
// evidence — are NOT counted, only summarized in the unverified footer.
func TestRunScan_ContextVerifiedVsNameShaped(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "app.o"), 4096)
	writeFile(t, filepath.Join(tmp, "app", "vendor", "pkg", "build", "lib.a"), 8192)
	writeFile(t, filepath.Join(tmp, "web", "node_modules", "dep", "index.js"), 2048)

	identity := scanTestIdentity(t)
	var stdout, stderr bytes.Buffer
	code := runScan(identity, &stdout, &stderr, scanOptions{path: tmp})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr: %s)", code, stderr.String())
	}
	out := stdout.String()

	// Verified: the real Rust target, labelled with its signature.
	if !strings.Contains(out, "development.rust.cargo-target") ||
		!strings.Contains(out, filepath.Join(tmp, "app", "target")) {
		t.Fatalf("expected context-verified Rust target in output:\n%s", out)
	}
	// The vendored build/ must not be counted as reclaimable — it belongs in the
	// unverified footer, not the "Prune candidates" table.
	buildPath := filepath.Join(tmp, "app", "vendor", "pkg", "build")
	if idx := strings.Index(out, "Prune candidates:"); idx >= 0 {
		footerIdx := strings.Index(out, "not context-verified")
		if footerIdx < 0 {
			t.Fatalf("expected an unverified footer, got:\n%s", out)
		}
		if strings.Contains(out[idx:footerIdx], buildPath) {
			t.Fatalf("vendored build/ must not appear in the reclaimable table:\n%s", out)
		}
	}
	if !strings.Contains(out, "not context-verified — not counted") {
		t.Fatalf("expected unverified footer summary, got:\n%s", out)
	}
}

// TestRunScan_ShowUnverifiedListsNameShaped confirms the footer lists the
// name-shaped, unverified dirs when asked (the vendored build/ and node_modules).
func TestRunScan_ShowUnverifiedListsNameShaped(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "app.o"), 4096)
	writeFile(t, filepath.Join(tmp, "app", "vendor", "pkg", "build", "lib.a"), 8192)
	writeFile(t, filepath.Join(tmp, "web", "node_modules", "dep", "index.js"), 2048)

	identity := scanTestIdentity(t)
	var stdout, stderr bytes.Buffer
	code := runScan(identity, &stdout, &stderr, scanOptions{path: tmp, showUnverified: true})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	out := stdout.String()
	for _, want := range []string{
		filepath.Join(tmp, "app", "vendor", "pkg", "build"),
		filepath.Join(tmp, "web", "node_modules"),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected --show-unverified to list %q, got:\n%s", want, out)
		}
	}
}

// TestRunScan_NoSilentSizeGate verifies the ephemeral profile applies no default
// size floor: a small context-verified target is reported without --min-size.
func TestRunScan_NoSilentSizeGate(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "app", "Cargo.toml"), 64)
	writeFile(t, filepath.Join(tmp, "app", "target", "debug", "app.o"), 128) // well under any 100M default

	identity := scanTestIdentity(t)
	var stdout, stderr bytes.Buffer
	if code := runScan(identity, &stdout, &stderr, scanOptions{path: tmp}); code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), filepath.Join(tmp, "app", "target")) {
		t.Fatalf("small target should be reported with no silent size gate:\n%s", stdout.String())
	}
}

// TestRunScan_BadMinSizeIsUsageError checks the usage exit code (3).
func TestRunScan_BadMinSizeIsUsageError(t *testing.T) {
	tmp := t.TempDir()
	identity := scanTestIdentity(t)
	var stdout, stderr bytes.Buffer
	if code := runScan(identity, &stdout, &stderr, scanOptions{path: tmp, minSize: "not-a-size"}); code != 3 {
		t.Fatalf("expected usage exit 3 for bad --min-size, got %d", code)
	}
}
