//go:build unix

package bench

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

// processUsage extracts CPU time and peak resident set size from a finished
// child process.
//
// Measuring the child rather than the harness is what makes an in-process
// walker and an external tool comparable: ru_maxrss on the harness itself is a
// high-water mark for the whole harness lifetime, so it would carry over from
// whichever comparator ran first.
func processUsage(state *os.ProcessState) (cpu time.Duration, peakRSS int64, ok bool) {
	if state == nil {
		return 0, 0, false
	}
	ru, isRusage := state.SysUsage().(*syscall.Rusage)
	if !isRusage {
		return 0, 0, false
	}
	cpu = time.Duration(ru.Utime.Nano()) + time.Duration(ru.Stime.Nano())
	return cpu, maxRSSBytes(ru.Maxrss), true
}

// maxRSSBytes converts the platform's ru_maxrss unit to bytes.
//
// Linux reports kilobytes and Darwin reports bytes. Getting this wrong scales
// every memory figure by 1024 in one direction or the other, and the mistake
// is invisible in a single-platform run.
func maxRSSBytes(maxrss int64) int64 {
	if runtime.GOOS == "linux" {
		return maxrss * 1024
	}
	return maxrss
}

// setProcessGroup puts the child in its own process group so a timeout can
// signal the whole tree rather than leaving orphaned walkers behind, holding
// descriptors open and skewing the next measurement.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup terminates a child and its descendants.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative PID signals the group. Fall back to the process itself if the
	// group signal fails.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
