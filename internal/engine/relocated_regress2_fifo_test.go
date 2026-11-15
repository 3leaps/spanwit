//go:build !windows

package engine

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// B5: a GOENV naming a FIFO never blocks the plan.
func TestRelocatedRegress2_B5_GOENVFifoDoesNotHang(t *testing.T) {
	isolateLiveCaches(t)
	fifo := filepath.Join(t.TempDir(), "envfifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	t.Setenv("GOENV", fifo)
	done := make(chan struct{})
	var certain bool
	go func() { _, certain = goEnvFileValue("GOCACHE"); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goEnvFileValue blocked on a FIFO GOENV")
	}
	if certain {
		t.Fatal("a FIFO GOENV must leave the configuration uncertain")
	}
}
