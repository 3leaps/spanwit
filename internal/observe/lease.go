package observe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrCycleBusy is returned when another process holds the state-dir cycle lease
// and the caller's context ends before acquisition (or non-blocking try fails).
var ErrCycleBusy = errors.New("observe cycle lease busy")

// CycleLease is an exclusive, state-dir-scoped lock held for one state mutation/cycle.
type CycleLease interface {
	Acquire(ctx context.Context) error
	Release() error
}

// FileCycleLease uses an OS exclusive lock on a lock file next to the state document.
type FileCycleLease struct {
	Path string
	f    *os.File
}

// NewFileCycleLease creates a lease at stateDir/cycle.lock.
func NewFileCycleLease(stateDir string) *FileCycleLease {
	return &FileCycleLease{Path: filepath.Join(stateDir, "cycle.lock")}
}

func (l *FileCycleLease) Acquire(ctx context.Context) error {
	if l == nil || l.Path == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	// Poll non-blocking exclusive lock until success or ctx done.
	for {
		err := tryLockFile(f)
		if err == nil {
			l.f = f
			return nil
		}
		if !isLockBusy(err) {
			_ = f.Close()
			return fmt.Errorf("observe cycle lease: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return fmt.Errorf("%w: %v", ErrCycleBusy, ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (l *FileCycleLease) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	_ = unlockFile(f)
	return f.Close()
}

// MemoryCycleLease is a process-local exclusive lease for tests.
type MemoryCycleLease struct {
	mu     sync.Mutex
	held   bool
	waiter chan struct{}
}

func (l *MemoryCycleLease) Acquire(ctx context.Context) error {
	if l == nil {
		return nil
	}
	for {
		l.mu.Lock()
		if !l.held {
			l.held = true
			l.mu.Unlock()
			return nil
		}
		if l.waiter == nil {
			l.waiter = make(chan struct{})
		}
		ch := l.waiter
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ErrCycleBusy, ctx.Err())
		case <-ch:
		}
	}
}

func (l *MemoryCycleLease) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = false
	if l.waiter != nil {
		close(l.waiter)
		l.waiter = nil
	}
	return nil
}

// errLockBusy is returned by tryLockFile when the lock is held by another process.
var errLockBusy = errors.New("lock busy")

func isLockBusy(err error) bool {
	return errors.Is(err, errLockBusy)
}
