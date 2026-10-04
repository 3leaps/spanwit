//go:build !unix

package bench

import (
	"os"
	"os/exec"
	"time"
)

// processUsage reports that per-child resource accounting is unavailable.
// Callers record the absence rather than substituting a zero, which would
// present an unmeasured run as one that used no CPU and no memory.
func processUsage(_ *os.ProcessState) (cpu time.Duration, peakRSS int64, ok bool) {
	return 0, 0, false
}

// setProcessGroup is a no-op off unix.
func setProcessGroup(_ *exec.Cmd) {}

// killProcessGroup terminates the child directly, without group semantics.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
