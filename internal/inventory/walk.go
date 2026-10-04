package inventory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const dirBatchSize = 256

type serialBackend struct{}

func (serialBackend) walk(ctx context.Context, opts normalizedOptions, state *runState) error {
	return walkRootsBounded(ctx, opts, state)
}

type parallelBackend struct{}

func (parallelBackend) walk(ctx context.Context, opts normalizedOptions, state *runState) error {
	return walkRootsBounded(ctx, opts, state)
}

func walkRootsBounded(
	ctx context.Context,
	opts normalizedOptions,
	state *runState,
) error {
	for _, root := range opts.roots {
		state.beginRoot(root)
		if gap, failed := opts.admissionGaps[root.ID]; failed {
			state.addGap(gap)
			state.completeRoot(root)
			continue
		}
		if opts.Traversal != nil && opts.Traversal.DiscoveredRoots && !opts.IncludeRemote {
			info, err := os.Lstat(root.Path)
			if err != nil {
				state.addGap(pathGap(root, root.Path, err))
				state.completeRoot(root)
				continue
			}
			if kind := state.remote.remoteKind(root.Path, info, metadataOf(info).deviceID, ""); kind != "" {
				state.addGap(Gap{RootID: root.ID, Kind: gapKindRemote,
					Detail: kind + " automatically selected root skipped; use --include-remote to walk it"})
				state.completeRoot(root)
				continue
			}
		}
		if err := walkRootBounded(ctx, root, opts, state); err != nil {
			return err
		}
		state.completeRoot(root)
	}
	return ctx.Err()
}

func walkRootBounded(
	ctx context.Context,
	root Root,
	opts normalizedOptions,
	state *runState,
) error {
	coord := newCoordinator(root.Path, opts.MaxPendingDirs)
	fdSem := make(chan struct{}, opts.MaxOpenDirs)

	// A context cancellation must wake workers waiting on the coordinator's
	// condition variable.
	wakeDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			coord.stop()
		case <-wakeDone:
		}
	}()

	var workers sync.WaitGroup
	for range opts.Workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				dir, ok := coord.next(ctx)
				if !ok {
					return
				}
				state.visitDirectory(root, dir)
				scanDirectory(ctx, root, dir, opts, state, fdSem, coord)
				coord.done()
			}
		}()
	}
	workers.Wait()
	close(wakeDone)
	state.observePeakPending(coord.peakPending())
	return ctx.Err()
}

func scanDirectory(
	ctx context.Context,
	root Root,
	dir string,
	opts normalizedOptions,
	state *runState,
	fdSem chan struct{},
	coord *coordinator,
) {
	if fdSem != nil {
		select {
		case fdSem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-fdSem }()
	}

	f, stalled, err := openDirWithDeadline(dir, opts.stallTimeout, opts.alertAfter, state.stalls)
	if stalled {
		state.addGap(Gap{
			RootID: root.ID, RelativePath: relative(root, dir), LocalAbsolutePath: dir,
			Kind:                gapKindStalled,
			Detail:              fmt.Sprintf("directory open did not respond within %s; subtree skipped", opts.stallTimeout),
			AffectsCompleteness: true,
		})
		return
	}
	if err != nil {
		state.addGap(pathGap(root, dir, err))
		return
	}
	defer func() { _ = f.Close() }()

	scanDirectoryContents(ctx, root, dir, opts, state, f, coord)
}

func scanDirectoryContents(ctx context.Context, root Root, dir string, opts normalizedOptions,
	state *runState, f fs.ReadDirFile, coord *coordinator,
) {
	for {
		if ctx.Err() != nil {
			return
		}
		batch, readErr := f.ReadDir(dirBatchSize)
		if readErr == nil || errors.Is(readErr, io.EOF) || len(batch) > 0 {
			state.observeDirectoryRead(root)
		}
		for _, dirEntry := range batch {
			if ctx.Err() != nil {
				return
			}
			path := filepath.Join(dir, dirEntry.Name())
			// Exclusions define content outside the declared subject. Apply them
			// before metadata reads or directory queueing so excluded subtrees are
			// never descended and never become coverage gaps.
			if excludedBy(opts.exclusions, root, path) {
				state.exclude(root)
				continue
			}

			// Never follow symlinks, even when they point at a directory.
			if dirEntry.Type()&fs.ModeSymlink != 0 {
				state.visitEntry(root)
				if opts.Traversal != nil {
					state.addGap(Gap{RootID: root.ID, Kind: "symlink", AffectsCompleteness: true,
						Detail: "symbolic link not followed or included in observed file bytes"})
				}
				continue
			}

			info, infoErr := dirEntry.Info()
			if infoErr != nil {
				state.addGap(pathGap(root, path, infoErr))
				continue
			}
			// Some filesystems omit d_type; Info still identifies the link itself.
			if info.Mode()&fs.ModeSymlink != 0 {
				state.visitEntry(root)
				if opts.Traversal != nil {
					state.addGap(Gap{RootID: root.ID, Kind: "symlink", AffectsCompleteness: true,
						Detail: "symbolic link not followed or included in observed file bytes"})
				}
				continue
			}
			meta := metadataOf(info)
			if opts.OneFilesystem {
				if meta.deviceID == "" {
					state.addGap(Gap{
						RootID: root.ID, RelativePath: relative(root, path),
						LocalAbsolutePath: path, Kind: "metadata-unavailable",
						Detail:              "device identity unavailable; one-filesystem cannot be confirmed",
						AffectsCompleteness: true,
					})
					continue
				}
				if meta.deviceID != root.DeviceID {
					state.addGap(Gap{
						RootID: root.ID, RelativePath: relative(root, path),
						LocalAbsolutePath: path, Kind: "mount-boundary",
						Detail:              "entry device differs from admitted root device",
						AffectsCompleteness: false,
					})
					continue
				}
			}

			prune := false
			if info.IsDir() && opts.Traversal != nil {
				depth := strings.Count(relative(root, path), "/") + 1
				if opts.Traversal.MaxDepth >= 0 && depth > opts.Traversal.MaxDepth {
					state.addGap(Gap{RootID: root.ID, RelativePath: relative(root, path),
						LocalAbsolutePath: path, Kind: "depth-limit", AffectsCompleteness: true,
						Detail: "subtree omitted at declared traversal depth"})
					continue
				}
				prune = state.discoverDirectory(root, path, depth)
			}
			if info.IsDir() && !opts.IncludeRemote {
				if kind := state.remote.remoteKind(path, info, meta.deviceID, root.DeviceID); kind != "" {
					// Policy exclusion decided from lstat (+ one cached statfs
					// per device) before any open: no download, no network wait.
					state.addGap(Gap{
						RootID: root.ID, RelativePath: relative(root, path),
						LocalAbsolutePath: path, Kind: gapKindRemote,
						Detail:              kind + " skipped by default; pass --include-remote to walk it",
						AffectsCompleteness: false,
					})
					continue
				}
			}

			switch {
			case info.IsDir():
				if prune {
					state.exclude(root)
					continue
				}
				accepted, running := coord.offer(path)
				if !running {
					return
				}
				if !accepted {
					state.addGap(Gap{
						RootID: root.ID, RelativePath: relative(root, path),
						LocalAbsolutePath: path, Kind: "directory-queue-limit",
						Detail: fmt.Sprintf(
							"subtree skipped because the pending-directory limit (%d) was reached",
							opts.MaxPendingDirs),
						AffectsCompleteness: true,
					})
					continue
				}
			case info.Mode().IsRegular():
				state.visitEntry(root)
				state.consider(root, path, info)
			default:
				state.visitEntry(root)
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				state.addGap(pathGap(root, dir, readErr))
			}
			return
		}
	}
}

func pathGap(root Root, path string, err error) Gap {
	kind := "io"
	switch {
	case errors.Is(err, fs.ErrNotExist):
		kind = "vanished"
	case errors.Is(err, fs.ErrPermission):
		kind = "permission"
	case errors.Is(err, context.Canceled):
		kind = "canceled"
	}
	return Gap{
		RootID: root.ID, RelativePath: relative(root, path),
		LocalAbsolutePath: path, Kind: kind, Detail: gapDetail(kind),
		AffectsCompleteness: true,
	}
}

func gapDetail(kind string) string {
	switch kind {
	case "vanished":
		return "path disappeared during enumeration"
	case "permission":
		return "permission denied"
	case "canceled":
		return "enumeration canceled"
	default:
		return "filesystem operation failed"
	}
}

type coordinator struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending []string
	active  int
	stopped bool
	limit   int
	peak    int64
}

func newCoordinator(root string, limit int) *coordinator {
	c := &coordinator{pending: []string{root}, limit: limit, peak: 1}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *coordinator) next(ctx context.Context) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if c.stopped || ctx.Err() != nil {
			return "", false
		}
		if len(c.pending) > 0 {
			dir := c.pending[len(c.pending)-1]
			c.pending = c.pending[:len(c.pending)-1]
			c.active++
			return dir, true
		}
		if c.active == 0 {
			c.stopped = true
			c.cond.Broadcast()
			return "", false
		}
		c.cond.Wait()
	}
}

func (c *coordinator) offer(path string) (accepted, running bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return false, false
	}
	if len(c.pending) >= c.limit {
		return false, true
	}
	c.pending = append(c.pending, path)
	if int64(len(c.pending)) > c.peak {
		c.peak = int64(len(c.pending))
	}
	c.cond.Signal()
	return true, true
}

func (c *coordinator) done() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	c.cond.Broadcast()
}

func (c *coordinator) stop() {
	c.mu.Lock()
	c.stopped = true
	c.cond.Broadcast()
	c.mu.Unlock()
}

func (c *coordinator) peakPending() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}
