package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProfileCommandsAreReadOnly(t *testing.T) {
	for _, args := range [][]string{{"diff", "../../docs/examples/profiles/developer.json", "../../docs/examples/profiles/developer.json"}, {"validate", "../../docs/examples/profiles/developer.json"}, {"preview", "../../docs/examples/profiles/developer.json", "--name", "media"}} {
		root := newProfileCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		leaf, _, err := root.Find(args)
		if err != nil {
			t.Fatal(err)
		}
		if got := resolveInvocationCapability(leaf); got != CapabilityObservational {
			t.Fatal("mutating profile command", got)
		}
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if args[0] == "validate" && result["state"] != "valid" {
			t.Fatal(result)
		}
		if args[0] == "preview" && result["execution_authorized"] != false {
			t.Fatal(result)
		}
	}
}

func TestProfileActivateStatusDeactivateCLI(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("profile activation store requires darwin or linux")
	}
	stateDir := t.TempDir()
	stateDir, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0700); err != nil {
		t.Fatal(err)
	}

	runProfile := func(args ...string) map[string]any {
		t.Helper()
		root := newProfileCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		fullArgs := append([]string{"--state-dir", stateDir}, args...)
		root.SetArgs(fullArgs)
		leaf, _, err := root.Find(fullArgs)
		if err != nil {
			t.Fatal(err)
		}
		if args[0] == "status" {
			if got := resolveInvocationCapability(leaf); got != CapabilityObservational {
				t.Fatalf("status should be observational, got %s", got)
			}
		} else {
			if got := resolveInvocationCapability(leaf); got != CapabilityMutating {
				t.Fatalf("%s should be mutating, got %s", args[0], got)
			}
		}
		if err := root.Execute(); err != nil {
			t.Fatalf("%v failed: %v (output=%s)", args, err, out.String())
		}
		var result map[string]any
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("invalid json from %v: %v (%s)", args, err, out.String())
		}
		return result
	}

	st0 := runProfile("status")
	if st0["active"] != false || st0["state"] != "inactive" {
		t.Fatalf("expected inactive, got %+v", st0)
	}

	act := runProfile("activate", "../../docs/examples/profiles/developer.json", "--name", "media", "--expected-revision", "none")
	if act["active"] != true || act["profile"] != "media" || act["state"] != "active" {
		t.Fatalf("expected active media profile, got %+v", act)
	}
	rev, _ := act["pack_revision"].(string)
	if rev == "" {
		t.Fatalf("missing pack_revision in %+v", act)
	}

	st1 := runProfile("status")
	if st1["active"] != true || st1["pack_revision"] != rev {
		t.Fatalf("status mismatch after activate: %+v", st1)
	}

	deact := runProfile("deactivate", "--expected-revision", rev)
	if deact["active"] != false || deact["state"] != "inactive" || deact["previous_revision"] != rev {
		t.Fatalf("unexpected deactivate result: %+v", deact)
	}
}
