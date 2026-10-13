package capacity

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestExecRunner_TimeoutBoundsChildrenHoldingStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ExecRunner{}.Run(ctx, "sh", "-c", "(sleep 8; echo late) | cat")
	elapsed := time.Since(start)
	var re *RunError
	if !errors.As(err, &re) || re.Code != "timeout" {
		t.Fatalf("want timeout RunError, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Run blocked %s on a child holding stdout; WaitDelay must bound it", elapsed)
	}
}
