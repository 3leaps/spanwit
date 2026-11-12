//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/3leaps/spanwit/internal/inventory"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const (
	MaxAttributionRoots = 16
	maxRootPathBytes    = 1024
	maxEncodedRootPaths = 16 * 1024
	attributionTimeout  = 2 * time.Minute
	AttributionBasis    = inventory.BasisAllocatedPathEntrySum
)

// RootObservation measures regular-file path entries, not unique physical
// storage or reclaimable bytes.
// Unknown build activity is never presented as proof that a root is idle.
type RootObservation struct {
	Path           string    `json:"path"`
	DeviceID       string    `json:"device_id,omitempty"`
	FileID         string    `json:"file_id,omitempty"`
	Identity       string    `json:"identity,omitempty"`
	Basis          string    `json:"basis"`
	Coverage       string    `json:"coverage"`
	Activity       string    `json:"activity"`
	CapturedAt     time.Time `json:"captured_at"`
	AllocatedBytes *int64    `json:"allocated_bytes,omitempty"`
	Files          int64     `json:"files"`
	Gaps           int64     `json:"gaps"`
}

// RootObserver serializes batches; each root uses at most two walker workers,
// two open directories, and 4096 pending directories. Results are bounded by
// MaxAttributionRoots and contain no per-file rows. A deadline is cooperative:
// kernel metadata calls cannot be forcibly interrupted by a Go context.
type RootObserver struct {
	mu     sync.Mutex
	roots  []string
	run    func(context.Context, inventory.Options, inventory.Sink) (inventory.Summary, error)
	pins   map[string]rootPin
	closed bool
}

func NewRootObserver(roots []string) (*RootObserver, error) {
	if len(roots) == 0 || len(roots) > MaxAttributionRoots {
		return nil, fmt.Errorf("attribution requires 1..%d explicit roots", MaxAttributionRoots)
	}
	copyRoots := make([]string, 0, len(roots))
	encodedBytes := 0
	for _, raw := range roots {
		if !filepath.IsAbs(raw) || len(raw) > maxRootPathBytes || strings.ContainsRune(raw, 0) || !utf8.ValidString(raw) {
			return nil, fmt.Errorf("attribution roots must be bounded absolute paths")
		}
		root := filepath.Clean(raw)
		if strings.TrimSpace(root) != root || strings.TrimSpace(raw) != raw {
			return nil, fmt.Errorf("attribution roots must not change under whitespace normalization")
		}
		encoded, err := json.Marshal(root)
		if err != nil {
			return nil, err
		}
		encodedBytes += len(encoded)
		if encodedBytes > maxEncodedRootPaths {
			return nil, fmt.Errorf("encoded attribution roots exceed 16 KiB")
		}
		for _, prior := range copyRoots {
			if containsRoot(prior, root) || containsRoot(root, prior) {
				return nil, fmt.Errorf("overlapping attribution roots")
			}
		}
		copyRoots = append(copyRoots, root)
	}
	return &RootObserver{roots: copyRoots, run: inventory.Run, pins: make(map[string]rootPin)}, nil
}

type rootPin struct {
	file       *os.File
	id         rootID
	generation string
}

// Close releases at most one continuity handle per configured root.
func (o *RootObserver) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	var err error
	for root, pin := range o.pins {
		err = errors.Join(err, pin.file.Close())
		delete(o.pins, root)
	}
	return err
}

func (o *RootObserver) clearPin(root string) {
	if pin, ok := o.pins[root]; ok {
		_ = pin.file.Close()
		delete(o.pins, root)
	}
}

func (o *RootObserver) pinRoot(root string, id rootID) (string, error) {
	if pin, ok := o.pins[root]; ok && pin.id == id {
		return pin.generation, nil
	}
	o.clearPin(root)
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), root)
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return "", err
	}
	actual, err := identityOf(info)
	if err != nil || actual != id {
		_ = f.Close()
		return "", fmt.Errorf("root changed before continuity pin")
	}
	generation := uuid.NewString()
	o.pins[root] = rootPin{f, id, generation}
	return generation, nil
}

func containsRoot(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Observe does not update any baseline or publish a result. The coordinator must
// commit results before adopting them. active comes from an independent activity
// observation; missing/false entries mean unknown, never idle. Cancellation
// discards the entire batch, including earlier completed roots.
func (o *RootObserver) Observe(ctx context.Context, active map[string]bool) ([]RootObservation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, fmt.Errorf("root observer closed")
	}
	ctx, cancel := context.WithTimeout(ctx, attributionTimeout)
	defer cancel()
	rows := make([]RootObservation, 0, len(o.roots))
	for _, root := range o.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := RootObservation{Path: root, Basis: AttributionBasis, Coverage: "unavailable", Activity: "unknown", CapturedAt: time.Now().UTC()}
		if active[root] {
			row.Activity, row.Coverage = "active", "deferred"
			rows = append(rows, row)
			continue
		}
		before, err := rootIdentity(root)
		if err != nil {
			o.clearPin(root)
			rows = append(rows, row)
			continue
		}
		row.DeviceID, row.FileID = before.device, before.file
		row.Identity, err = o.pinRoot(root, before)
		if err != nil {
			rows = append(rows, row)
			continue
		}
		sink := &rootSummarySink{}
		summary, err := o.run(ctx, inventory.Options{
			Roots: []string{root}, Backend: inventory.BackendParallel, Workers: 2,
			MaxOpenDirs: 2, MaxPendingDirs: 4096, OneFilesystem: true,
			SizeBasis: inventory.SizeAllocated, EmissionMode: inventory.EmissionSummaryOnly,
			MutationContract: inventory.MutationContractReadOnly, StallTimeout: 5 * time.Second,
		}, sink)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		after, identityErr := rootIdentity(root)
		row.CapturedAt = time.Now().UTC()
		if identityErr != nil || before != after || sink.device != before.device || sink.path != root {
			row.Coverage = "incomparable"
			o.clearPin(root)
		} else if err == nil && !summary.Canceled && summary.Lifecycle == inventory.LifecycleComplete &&
			summary.GapCount == 0 && summary.BoundarySkipCount == 0 && summary.RemoteSkipCount == 0 &&
			summary.AllocatedUnmeasuredCount == 0 && summary.MatchedAllocatedBytes >= 0 {
			row.Coverage = "complete"
			n := summary.MatchedAllocatedBytes
			row.AllocatedBytes = &n
		} else {
			row.Coverage = "partial"
		}
		row.Files, row.Gaps = summary.MatchedCount, summary.GapCount
		rows = append(rows, row)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

type rootID struct{ device, file string }

func rootIdentity(root string) (rootID, error) {
	// Reject symlink spellings, including ancestor components. This is a
	// best-effort observation check, not a race-free deletion admission guard.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return rootID{}, fmt.Errorf("root unavailable or symlinked")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return rootID{}, err
	}
	return identityOf(info)
}

func identityOf(info os.FileInfo) (rootID, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok {
		return rootID{}, fmt.Errorf("root identity unavailable")
	}
	return rootID{fmt.Sprint(st.Dev), fmt.Sprint(st.Ino)}, nil
}

type rootSummarySink struct{ device, path string }

func (s *rootSummarySink) Header(h inventory.Header) error {
	if len(h.Roots) == 1 {
		s.device = h.Roots[0].DeviceID
		s.path = h.Roots[0].Path
	}
	return nil
}
func (*rootSummarySink) Entry(inventory.Entry) error     { return nil }
func (*rootSummarySink) Gap(inventory.Gap) error         { return nil }
func (*rootSummarySink) Summary(inventory.Summary) error { return nil }

// RootDelta compares complete committed observations only. It cannot attribute
// a volume's free-space loss to this root, nor establish bytes written: entry
// allocation can change because files are removed, replaced or shared.
func RootDelta(older, newer RootObservation) (int64, bool) {
	if older.Coverage != "complete" || newer.Coverage != "complete" ||
		older.AllocatedBytes == nil || newer.AllocatedBytes == nil ||
		*older.AllocatedBytes < 0 || *newer.AllocatedBytes < 0 ||
		older.Path != newer.Path || older.DeviceID == "" || older.FileID == "" ||
		older.Identity == "" || older.Identity != newer.Identity ||
		older.DeviceID != newer.DeviceID || older.FileID != newer.FileID ||
		older.Basis != AttributionBasis || newer.Basis != AttributionBasis ||
		older.CapturedAt.IsZero() || !newer.CapturedAt.After(older.CapturedAt) {
		return 0, false
	}
	return *newer.AllocatedBytes - *older.AllocatedBytes, true
}
