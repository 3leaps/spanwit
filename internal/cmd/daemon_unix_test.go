//go:build darwin || linux

package cmd

import (
	"github.com/fulmenhq/gofulmen/appidentity"
	"testing"
)

func TestDaemonCapabilities(t *testing.T) {
	resetPolicyForTest(t)
	c := newDaemonCmd(&appidentity.Identity{BinaryName: "testtool"})
	for _, command := range c.Commands() {
		want := CapabilityObservational
		if command.Name() == "run" || command.Name() == "once" {
			want = CapabilityMutating
		}
		if got := resolveInvocationCapability(command); got != want {
			t.Fatalf("%s: %s, want %s", command.Name(), got, want)
		}
	}
}
