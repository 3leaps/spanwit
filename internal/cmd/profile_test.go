package cmd

import (
	"bytes"
	"encoding/json"
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
