package inventory

import (
	"os"
	"sync"
	"time"
)

// Stall backstop. A directory open can block in the kernel indefinitely (a
// hung network mount, a File Provider waiting on its daemon), and context
// cancellation does not interrupt a blocked open. Rather than wait forever,
// each directory open runs in a helper goroutine and the worker gives up after
// the stall timeout: the subtree is recorded as a path-free, completeness-
// affecting "stalled" gap and the walk continues. The helper is left to finish
// and closes any descriptor that arrives late.
//
// This primitive is deliberately unexported and inventory-only. It abandons a
// read-only open; it must never be reused for a mutating operation.

const (
	// DefaultStallTimeout is how long a single directory open may block before
	// the walker skips that subtree.
	DefaultStallTimeout = 60 * time.Second
	// defaultStallAlertAfter is when a still-pending open first raises the
	// operator alert.
	defaultStallAlertAfter = 10 * time.Second
)

// StallAlert is a path-free notice that directory opens are not responding.
// It carries counters only: a stalled directory is never named, because the
// blocked path may itself be sensitive.
type StallAlert struct {
	// Pending is the number of directory opens currently past the alert delay.
	Pending int
	// Waited is how long the oldest such open had been pending when alerted.
	Waited time.Duration
	// Timeout is the effective stall timeout (0 = never abandon).
	Timeout time.Duration
}

// openDir is the directory open used by the walker; tests substitute a
// blocking opener to exercise the backstop deterministically.
var openDir = os.Open

type pendingOpen struct {
	mu        sync.Mutex
	done      bool
	abandoned bool
	f         *os.File
	err       error
	ready     chan struct{}
}

// stallWatch coalesces alerts: one alert per stall episode, where an episode
// ends when no open is past the alert delay.
type stallWatch struct {
	mu       sync.Mutex
	slow     int
	alerted  bool
	onAlert  func(StallAlert)
	timeout  time.Duration
	alertAge time.Duration
}

func (w *stallWatch) slowBegin() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.slow++
	fire := !w.alerted && w.onAlert != nil
	if fire {
		w.alerted = true
	}
	alert := StallAlert{Pending: w.slow, Waited: w.alertAge, Timeout: w.timeout}
	w.mu.Unlock()
	if fire {
		w.onAlert(alert)
	}
}

func (w *stallWatch) slowEnd() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.slow--
	if w.slow == 0 {
		w.alerted = false
	}
	w.mu.Unlock()
}

// openDirWithDeadline opens dir, giving up after timeout (timeout <= 0 waits
// indefinitely). stalled reports that the open was abandoned; f and err are
// then nil.
func openDirWithDeadline(dir string, timeout, alertAfter time.Duration, watch *stallWatch) (f *os.File, stalled bool, err error) {
	p := &pendingOpen{ready: make(chan struct{})}
	// Capture the opener before spawning: an abandoned helper may outlive the
	// walk, and it must not read shared state after the caller has moved on.
	open := openDir
	go func() {
		file, openErr := open(dir)
		p.mu.Lock()
		if p.abandoned {
			p.mu.Unlock()
			if file != nil {
				_ = file.Close()
			}
			return
		}
		p.f, p.err, p.done = file, openErr, true
		p.mu.Unlock()
		close(p.ready)
	}()

	var alertC <-chan time.Time
	if alertAfter > 0 && (timeout <= 0 || alertAfter < timeout) {
		alertTimer := time.NewTimer(alertAfter)
		defer alertTimer.Stop()
		alertC = alertTimer.C
	}
	var timeoutC <-chan time.Time
	if timeout > 0 {
		timeoutTimer := time.NewTimer(timeout)
		defer timeoutTimer.Stop()
		timeoutC = timeoutTimer.C
	}

	slow := false
	defer func() {
		if slow {
			watch.slowEnd()
		}
	}()
	for {
		select {
		case <-p.ready:
			return p.f, false, p.err
		case <-alertC:
			alertC = nil
			slow = true
			watch.slowBegin()
		case <-timeoutC:
			p.mu.Lock()
			if p.done {
				p.mu.Unlock()
				return p.f, false, p.err
			}
			p.abandoned = true
			p.mu.Unlock()
			return nil, true, nil
		}
	}
}
