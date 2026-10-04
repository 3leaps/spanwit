package capacity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type CommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type RunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (f RunnerFunc) Run(ctx context.Context, program string, args ...string) ([]byte, error) {
	return f(ctx, program, args...)
}

type ExecRunner struct {
	MaxOutput          int64
	NoMatchExitCode    int
	PermissionWarnings bool
}

type RunError struct {
	Code string
}

func (e *RunError) Error() string {
	return e.Code
}

func (r ExecRunner) Run(ctx context.Context, program string, args ...string) ([]byte, error) {
	limit := r.MaxOutput
	if limit <= 0 {
		limit = DefaultCommandOutput
	}
	cmd := exec.CommandContext(ctx, program, args...)
	var stdout, stderr bytes.Buffer
	stdoutWriter := &boundedWriter{Writer: &stdout, Remaining: limit}
	stderrWriter := &boundedWriter{Writer: &stderr, Remaining: 64 << 10}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	err := cmd.Run()
	if stdoutWriter.Exceeded {
		return stdout.Bytes(), &RunError{Code: "truncated"}
	}
	lower := strings.ToLower(stderr.String())
	if r.PermissionWarnings &&
		(strings.Contains(lower, "permission denied") || strings.Contains(lower, "not permitted")) {
		return stdout.Bytes(), &RunError{Code: "permission_denied"}
	}
	if err == nil {
		return stdout.Bytes(), nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
		return stdout.Bytes(), &RunError{Code: "timeout"}
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return nil, &RunError{Code: "command_missing"}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && r.NoMatchExitCode > 0 &&
		exitErr.ExitCode() == r.NoMatchExitCode && stderr.Len() == 0 && stdout.Len() == 0 {
		return []byte{}, nil
	}
	if strings.Contains(lower, "permission") || strings.Contains(lower, "not permitted") {
		return stdout.Bytes(), &RunError{Code: "permission_denied"}
	}
	return stdout.Bytes(), &RunError{Code: "command_failed"}
}

type boundedWriter struct {
	Writer    io.Writer
	Remaining int64
	Exceeded  bool
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.Remaining <= 0 {
		w.Exceeded = true
		return len(p), fmt.Errorf("command output exceeded limit")
	}
	if int64(len(p)) > w.Remaining {
		_, _ = w.Writer.Write(p[:w.Remaining])
		w.Remaining = 0
		w.Exceeded = true
		return len(p), fmt.Errorf("command output exceeded limit")
	}
	n, err := w.Writer.Write(p)
	w.Remaining -= int64(n)
	return n, err
}

func gapForRunError(err error, scope string) CoverageGap {
	code := "command_failed"
	var runErr *RunError
	if errors.As(err, &runErr) {
		code = runErr.Code
	}
	details := map[string]string{
		"timeout":           "public platform query did not complete before the collector deadline",
		"command_missing":   "required public platform query is not installed",
		"permission_denied": "public platform query was denied by current privileges",
		"command_failed":    "public platform query failed",
		"truncated":         "public platform query exceeded the bounded output limit",
	}
	detail, ok := details[code]
	if !ok {
		code = "command_failed"
		detail = details[code]
	}
	return CoverageGap{Code: code, Scope: scope, Detail: detail}
}
