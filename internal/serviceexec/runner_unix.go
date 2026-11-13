//go:build darwin || linux

// Package serviceexec provides the deliberately small process boundary used by
// service-manager adapters. It does not interpret service-manager output or
// establish that a timeout left an external service unchanged.
package serviceexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	defaultTimeout = 10 * time.Second
	maximumTimeout = 30 * time.Second
	defaultOutput  = 64 << 10
	maximumOutput  = 1 << 20
	cleanupGrace   = 500 * time.Millisecond
)

var errOwnedGroupRemaining = errors.New("owned process group remains after bounded cleanup")
var errInvalidOwnedGroup = errors.New("invalid owned process group")

// Outcome is the bounded execution result. A timed-out, cancelled, or
// output-limited request that started is uncertain: callers must query their
// service manager before deciding what happened externally.
type Outcome string

const (
	NotStarted  Outcome = "not_started"
	Success     Outcome = "success"
	Failed      Outcome = "failed"
	TimedOut    Outcome = "timed_out"
	Canceled    Outcome = "canceled"
	OutputLimit Outcome = "output_limit"
	Uncertain   Outcome = "uncertain"
)

// Request contains no shell fragment. Program and Dir must be absolute. Env
// is passed exactly as supplied; callers that need an environment must provide
// it explicitly. Trusted internal adapters select Program and command semantics;
// arbitrary external execution requests must not be passed to this boundary.
type Request struct {
	Program   string
	Args      []string
	Dir       string
	Env       []string
	Timeout   time.Duration
	MaxOutput int
}

type Result struct {
	Outcome   Outcome
	Started   bool
	Uncertain bool
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Err       error
}

// Runner permits a future sysprims-backed implementation without changing
// service adapters.
type Runner interface {
	Run(context.Context, Request) Result
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, request Request) Result {
	if err := validate(request); err != nil {
		return Result{Outcome: NotStarted, Err: err}
	}
	if err := ctx.Err(); err != nil {
		return Result{Outcome: contextOutcome(err), Err: err}
	}
	timeout := request.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	limit := request.MaxOutput
	if limit == 0 {
		limit = defaultOutput
	}
	// #nosec G204 -- Trusted internal adapters select the absolute-validated program and argv; no shell interpretation.
	command := exec.Command(request.Program, request.Args...)
	command.Dir = request.Dir
	// A nil environment means empty, not inherited. Service adapters must make
	// every inherited variable an intentional part of their request.
	command.Env = append([]string{}, request.Env...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output := newOutput(limit)
	command.Stdout = output.stdout
	command.Stderr = output.stderr
	// Wait owns the pipe-copy lifecycle. If the leader exits while a descendant
	// holds a copied descriptor, WaitDelay closes our pipes and returns an
	// uncertainty rather than letting a manager call hang forever.
	command.WaitDelay = cleanupGrace
	if err := command.Start(); err != nil {
		return Result{Outcome: NotStarted, Err: err}
	}
	result := Result{Started: true, ExitCode: -1}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case waitErr := <-wait:
		result = completed(result, command.ProcessState, waitErr)
	case <-output.exceededCh:
		result.Outcome, result.Uncertain = OutputLimit, true
		result.Err = errors.New("command output exceeded limit")
		stopAndWait(command.Process.Pid, wait)
	case <-ctx.Done():
		result.Outcome, result.Uncertain, result.Err = contextOutcome(ctx.Err()), true, ctx.Err()
		stopAndWait(command.Process.Pid, wait)
	case <-timer.C:
		result.Outcome, result.Uncertain = TimedOut, true
		result.Err = fmt.Errorf("command exceeded %s", timeout)
		stopAndWait(command.Process.Pid, wait)
	}
	cleanupErr := cleanupGroup(command.Process.Pid)
	if output.exceeded() && (result.Outcome == Success || result.Outcome == Failed || result.Outcome == Uncertain) {
		result.Outcome, result.Uncertain = OutputLimit, true
		result.Err = errors.New("command output exceeded limit")
	}
	result = applyCleanupResult(result, cleanupErr)
	result.Stdout, result.Stderr = output.bytes()
	return result
}

func validate(request Request) error {
	if !filepath.IsAbs(request.Program) {
		return errors.New("program must be absolute")
	}
	if !filepath.IsAbs(request.Dir) {
		return errors.New("working directory must be absolute")
	}
	if request.Timeout < 0 || request.Timeout > maximumTimeout {
		return fmt.Errorf("timeout must be between zero and %s", maximumTimeout)
	}
	if request.MaxOutput < 0 || request.MaxOutput > maximumOutput {
		return fmt.Errorf("output limit must be between zero and %d bytes", maximumOutput)
	}
	return nil
}

func contextOutcome(err error) Outcome {
	if errors.Is(err, context.DeadlineExceeded) {
		return TimedOut
	}
	return Canceled
}

func completed(result Result, state *os.ProcessState, err error) Result {
	if errors.Is(err, exec.ErrWaitDelay) {
		result.Outcome, result.Uncertain, result.Err = Uncertain, true, err
		if state != nil {
			result.ExitCode = state.ExitCode()
		}
		return result
	}
	if err == nil {
		result.Outcome, result.ExitCode = Success, 0
		return result
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		// A service manager can act before it exits nonzero. Also, Cmd.Wait can
		// retain ExitError when a descendant delayed pipe draining past WaitDelay.
		// Callers must query manager state after every started non-success result.
		result.Outcome, result.Uncertain, result.ExitCode, result.Err = Failed, true, exit.ExitCode(), err
		return result
	}
	result.Outcome, result.Uncertain, result.Err = Uncertain, true, err
	return result
}

// stopAndWait drains an already-uncertain interruption. The subsequent
// cleanupGroup call owns and reports the final cleanup observation.
func stopAndWait(pid int, wait <-chan error) {
	_ = terminate(pid, syscall.SIGTERM)
	select {
	case <-wait: //nolint:errcheck // Interruption is already uncertain; cleanupGroup reports cleanup errors.
	case <-time.After(cleanupGrace):
		_ = terminate(pid, syscall.SIGKILL)
		select {
		case <-wait: //nolint:errcheck // Interruption is already uncertain; cleanupGroup reports cleanup errors.
		case <-time.After(cleanupGrace):
		}
	}
}

func applyCleanupResult(result Result, cleanupErr error) Result {
	if cleanupErr == nil {
		return result
	}
	result.Outcome, result.Uncertain = Uncertain, true
	result.Err = errors.Join(result.Err, fmt.Errorf("owned process-group cleanup failed: %w", cleanupErr))
	return result
}

type groupOps struct {
	signal func(int, syscall.Signal) error
	probe  func(int) error
	now    func() time.Time
	sleep  func(time.Duration)
}

func cleanupGroup(pid int) error {
	return cleanupGroupWith(pid, groupOps{
		signal: terminate,
		probe:  probeGroup,
		now:    time.Now,
		sleep:  time.Sleep,
	})
}

// cleanupGroupWith is bounded best effort for this owned process group. A
// descendant that escapes the group or a kernel task that cannot be interrupted
// is outside this boundary. If a group remains observable after SIGKILL, callers
// receive an uncertain result rather than a claim that cleanup completed.
func cleanupGroupWith(pid int, ops groupOps) error {
	if pid <= 0 {
		return errInvalidOwnedGroup
	}
	if err := ops.signal(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	if gone, err := awaitGroupGone(pid, ops); err != nil {
		return err
	} else if gone {
		return nil
	}
	if err := ops.signal(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	if gone, err := awaitGroupGone(pid, ops); err != nil {
		return err
	} else if gone {
		return nil
	}
	return errOwnedGroupRemaining
}

func awaitGroupGone(pid int, ops groupOps) (bool, error) {
	deadline := ops.now().Add(cleanupGrace)
	for ops.now().Before(deadline) {
		if err := ops.probe(pid); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return true, nil
			}
			return false, err
		}
		ops.sleep(20 * time.Millisecond)
	}
	if err := ops.probe(pid); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func terminate(pid int, signal syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(-pid, signal)
}

func probeGroup(pid int) error {
	if pid <= 0 {
		return errInvalidOwnedGroup
	}
	return syscall.Kill(-pid, 0)
}

type output struct {
	mu         sync.Mutex
	remaining  int
	exceeded_  bool
	exceededCh chan struct{}
	exceedOnce sync.Once
	stdoutBuf  bytes.Buffer
	stderrBuf  bytes.Buffer
	stdout     io.Writer
	stderr     io.Writer
}

func newOutput(limit int) *output {
	o := &output{remaining: limit, exceededCh: make(chan struct{})}
	o.stdout = writer{output: o, destination: &o.stdoutBuf}
	o.stderr = writer{output: o, destination: &o.stderrBuf}
	return o
}

func (o *output) exceeded() bool { o.mu.Lock(); defer o.mu.Unlock(); return o.exceeded_ }
func (o *output) bytes() ([]byte, []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.stdoutBuf.Bytes()...), append([]byte(nil), o.stderrBuf.Bytes()...)
}

type writer struct {
	output      *output
	destination *bytes.Buffer
}

func (w writer) Write(data []byte) (int, error) {
	w.output.mu.Lock()
	defer w.output.mu.Unlock()
	if len(data) > w.output.remaining {
		if w.output.remaining > 0 {
			_, _ = w.destination.Write(data[:w.output.remaining])
		}
		w.output.remaining = 0
		w.output.exceeded_ = true
		w.output.exceedOnce.Do(func() { close(w.output.exceededCh) })
		return len(data), errors.New("output limit")
	}
	_, _ = w.destination.Write(data)
	w.output.remaining -= len(data)
	return len(data), nil
}
