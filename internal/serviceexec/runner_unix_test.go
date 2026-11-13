//go:build darwin || linux

package serviceexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecRunner(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		limit   int
		timeout time.Duration
		want    Outcome
	}{
		{"success", "success", 0, 0, Success},
		{"failure", "failure", 0, 0, Failed},
		{"flood", "flood", 64, 0, OutputLimit},
		{"timeout", "sleep", 0, 80 * time.Millisecond, TimedOut},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runHelper(t, test.action, "", test.timeout, test.limit)
			if result.Outcome != test.want {
				t.Fatalf("outcome=%q error=%v", result.Outcome, result.Err)
			}
			if !result.Started {
				t.Fatal("helper did not start")
			}
			if test.want == Success && (!strings.Contains(string(result.Stdout), "out") || !strings.Contains(string(result.Stderr), "err")) {
				t.Fatalf("missing captured output: stdout=%q stderr=%q", result.Stdout, result.Stderr)
			}
			if (test.want == TimedOut || test.want == OutputLimit) && !result.Uncertain {
				t.Fatal("externally effective result must be uncertain")
			}
			if test.want == Failed && !result.Uncertain {
				t.Fatal("started nonzero command must remain externally uncertain")
			}
		})
	}
}

func TestCleanupGroupWithAndResultClassification(t *testing.T) {
	termFailure := errors.New("term failure")
	cases := []struct {
		name          string
		remaining     bool
		termError     error
		killError     error
		probeError    error
		goneAfterKill bool
		wantErr       error
	}{
		{"gone after term", true, nil, nil, nil, false, nil},
		{"gone after kill", true, nil, nil, nil, true, nil},
		{"remains after kill", true, nil, nil, nil, false, errOwnedGroupRemaining},
		{"term error", true, termFailure, nil, nil, false, termFailure},
		{"kill error", true, nil, termFailure, nil, false, termFailure},
		{"probe error", true, nil, nil, syscall.EPERM, false, syscall.EPERM},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			remaining := test.remaining
			clock := time.Unix(0, 0)
			termSent := false
			ops := groupOps{
				signal: func(_ int, signal syscall.Signal) error {
					switch signal {
					case syscall.SIGTERM:
						termSent = true
						if test.termError != nil {
							return test.termError
						}
						if test.name == "gone after term" {
							remaining = false
						}
					case syscall.SIGKILL:
						if test.killError != nil {
							return test.killError
						}
						if test.goneAfterKill {
							remaining = false
						}
					}
					return nil
				},
				probe: func(int) error {
					if test.probeError != nil {
						return test.probeError
					}
					if remaining {
						return nil
					}
					return syscall.ESRCH
				},
				now:   func() time.Time { return clock },
				sleep: func(d time.Duration) { clock = clock.Add(d) },
			}
			err := cleanupGroupWith(42, ops)
			if !termSent || !errors.Is(err, test.wantErr) {
				t.Fatalf("cleanup error=%v want=%v", err, test.wantErr)
			}
		})
	}
	prior := errors.New("nonzero exit")
	cleanup := errors.New("cleanup denied")
	result := applyCleanupResult(Result{Outcome: Failed, Started: true, Err: prior}, cleanup)
	if result.Outcome != Uncertain || !result.Uncertain || !errors.Is(result.Err, prior) || !errors.Is(result.Err, cleanup) {
		t.Fatalf("cleanup classification=%+v", result)
	}
	result = applyCleanupResult(Result{Outcome: Success, Started: true}, syscall.EPERM)
	if result.Outcome != Uncertain || !result.Uncertain || !errors.Is(result.Err, syscall.EPERM) {
		t.Fatalf("probe classification=%+v", result)
	}
	if !errors.Is(cleanupGroupWith(0, groupOps{}), errInvalidOwnedGroup) {
		t.Fatal("invalid process group was not rejected")
	}
}

func TestExecRunnerRejectsInvalidAndMissingPrograms(t *testing.T) {
	result := (ExecRunner{}).Run(context.Background(), Request{Program: "sh"})
	if result.Outcome != NotStarted || result.Err == nil {
		t.Fatalf("relative program=%+v", result)
	}
	result = (ExecRunner{}).Run(context.Background(), Request{Program: "/definitely/not/a-program", Dir: t.TempDir()})
	if result.Outcome != NotStarted || result.Err == nil {
		t.Fatalf("missing program=%+v", result)
	}
	result = (ExecRunner{}).Run(context.Background(), Request{Program: os.Args[0], Dir: "relative"})
	if result.Outcome != NotStarted || result.Err == nil {
		t.Fatalf("relative directory=%+v", result)
	}
	result = (ExecRunner{}).Run(context.Background(), Request{Program: os.Args[0], Dir: t.TempDir(), MaxOutput: maximumOutput + 1})
	if result.Outcome != NotStarted || result.Err == nil {
		t.Fatalf("oversized output=%+v", result)
	}
}

func TestExecRunnerDrainsFastExitOutputBeforeClassifyingResult(t *testing.T) {
	for range 3 {
		result := runHelper(t, "fast-large", "", 0, 64<<10)
		if result.Outcome != Success {
			t.Fatalf("large output outcome=%q error=%v", result.Outcome, result.Err)
		}
		wantStdout := strings.Repeat("o", 24<<10)
		wantStderr := strings.Repeat("e", 24<<10)
		if string(result.Stdout) != wantStdout || string(result.Stderr) != wantStderr {
			t.Fatalf("output changed: stdout=%d stderr=%d", len(result.Stdout), len(result.Stderr))
		}
	}
	for range 3 {
		result := runHelper(t, "fast-overflow", "", 0, 64<<10)
		if result.Outcome != OutputLimit || !result.Uncertain {
			t.Fatalf("overflow outcome=%q uncertain=%t error=%v", result.Outcome, result.Uncertain, result.Err)
		}
	}
}

func TestExecRunnerDoesNotStartCanceledContextOrInheritEnvironment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := (ExecRunner{}).Run(ctx, Request{Program: os.Args[0], Dir: t.TempDir()})
	if result.Outcome != Canceled || result.Started {
		t.Fatalf("pre-canceled result=%+v", result)
	}
	t.Setenv("SPANWIT_SERVICEEXEC_SHOULD_NOT_INHERIT", "present")
	result = (ExecRunner{}).Run(context.Background(), Request{
		Program: os.Args[0], Args: []string{"-test.run=TestServiceExecHelper", "--", "require-empty-env"}, Dir: t.TempDir(),
	})
	if result.Outcome != Success {
		t.Fatalf("nil environment inherited or helper failed: %+v", result)
	}
}

func TestExecRunnerCleansDescendantsAfterLeaderExit(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "escaped-child-marker")
	started := time.Now()
	// The race runtime otherwise intentionally keeps the helper alive for about
	// one second after this test function returns. Disable only that exit delay:
	// race instrumentation remains enabled and the fixture now tests post-exit
	// process-group cleanup rather than the race runtime's pre-exit grace.
	result := runHelperWithEnv(t, "leader-exit-child", marker, 0, 0, "GORACE=atexit_sleep_ms=0")
	runElapsed := time.Since(started)
	// A permission-denied cleanup observation remains uncertain. The late
	// marker check must still show that the fixture child did not survive.
	knownCleanupUncertainty := errors.Is(result.Err, errOwnedGroupRemaining) || errors.Is(result.Err, syscall.EPERM)
	cleanupUncertain := result.Outcome == Uncertain && result.Uncertain && knownCleanupUncertainty
	if result.ExitCode != 0 || (result.Outcome != Success && !cleanupUncertain) {
		t.Fatalf("outcome=%q error=%v", result.Outcome, result.Err)
	}
	if runElapsed > 2*time.Second {
		t.Fatalf("leader-exit cleanup was not bounded: %s", runElapsed)
	}
	if _, err := os.Stat(marker + ".ready"); err != nil {
		t.Fatalf("fixture child did not start before leader exit: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("descendant survived group cleanup: %v elapsed=%s", err, runElapsed)
	}
}

func TestExecRunnerBoundsInheritedPipeDescendant(t *testing.T) {
	started := time.Now()
	result := runHelperWithEnv(t, "leader-exit-pipe-child", filepath.Join(t.TempDir(), "pipe-child-marker"), 0, 0, "GORACE=atexit_sleep_ms=0")
	if result.Outcome != Uncertain && result.Outcome != Success {
		t.Fatalf("outcome=%q error=%v", result.Outcome, result.Err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("inherited pipe held execution beyond bound")
	}
}

func TestExecRunnerNonzeroLeaderWithPipeDescendantIsUncertain(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "nonzero-pipe-child-marker")
	started := time.Now()
	result := runHelperWithEnv(t, "failure-pipe-child", marker, 0, 0, "GORACE=atexit_sleep_ms=0")
	validOutcome := result.Outcome == Failed || (result.Outcome == Uncertain && errors.Is(result.Err, errOwnedGroupRemaining))
	if !validOutcome || !result.Uncertain || result.ExitCode != 7 {
		t.Fatalf("result=%+v", result)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("nonzero leader with inherited pipe was not bounded")
	}
}

func runHelper(t *testing.T, action, marker string, timeout time.Duration, limit int) Result {
	t.Helper()
	return runHelperWithEnv(t, action, marker, timeout, limit)
}

func runHelperWithEnv(t *testing.T, action, marker string, timeout time.Duration, limit int, extraEnv ...string) Result {
	t.Helper()
	request := Request{
		Program:   os.Args[0],
		Args:      []string{"-test.run=TestServiceExecHelper", "--", action},
		Dir:       t.TempDir(),
		Env:       append(append(os.Environ(), "SPANWIT_SERVICEEXEC_MARKER="+marker), extraEnv...),
		Timeout:   timeout,
		MaxOutput: limit,
	}
	return (ExecRunner{}).Run(context.Background(), request)
}

func TestServiceExecHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	action := os.Args[len(os.Args)-1]
	switch action {
	case "success":
		_, _ = os.Stdout.WriteString("out")
		_, _ = os.Stderr.WriteString("err")
	case "failure":
		os.Exit(7)
	case "flood":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 4096))
	case "fast-large":
		_, _ = os.Stdout.WriteString(strings.Repeat("o", 24<<10))
		_, _ = os.Stderr.WriteString(strings.Repeat("e", 24<<10))
		os.Exit(0)
	case "fast-overflow":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 96<<10))
		os.Exit(0)
	case "sleep":
		time.Sleep(5 * time.Second)
	case "leader-exit-child", "leader-exit-pipe-child", "failure-pipe-child":
		child := exec.Command(os.Args[0], "-test.run=TestServiceExecHelper", "--", "child-marker")
		child.Env = os.Environ()
		if action == "leader-exit-pipe-child" || action == "failure-pipe-child" {
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
		}
		if err := child.Start(); err != nil {
			os.Exit(9)
		}
		marker := os.Getenv("SPANWIT_SERVICEEXEC_MARKER")
		deadline := time.Now().Add(time.Second)
		for {
			if _, err := os.Stat(marker + ".ready"); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(11)
			}
			time.Sleep(5 * time.Millisecond)
		}
		if action == "failure-pipe-child" {
			os.Exit(7)
		}
	case "child-marker":
		if marker := os.Getenv("SPANWIT_SERVICEEXEC_MARKER"); marker != "" {
			_ = os.WriteFile(marker+".ready", []byte("started"), 0o600)
		}
		time.Sleep(time.Second)
		if marker := os.Getenv("SPANWIT_SERVICEEXEC_MARKER"); marker != "" {
			_ = os.WriteFile(marker, []byte("survived"), 0o600)
		}
	case "require-empty-env":
		if os.Getenv("SPANWIT_SERVICEEXEC_SHOULD_NOT_INHERIT") != "" {
			os.Exit(10)
		}
	default:
		os.Exit(8)
	}
}
