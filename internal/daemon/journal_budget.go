//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// This is a stop-admission watermark, not a physical filesystem quota.
// recordprims may rewrite indexes and temporarily hold old and new copies.
const journalWatermark int64 = 16 << 20
const journalEntryLimit = 4096
const journalDepthLimit = 8

var errJournalCapacity = errors.New("journal_capacity_reached")

// journalUsage bounds both metadata enumeration and replay inputs. It never
// opens file contents or follows links. All regular files count, including
// indexes and temporary files; sparse files count by their logical length.
func journalUsage(ctx context.Context, dir string, limit int64) (int64, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	f := os.NewFile(uintptr(fd), dir)
	defer func() { _ = f.Close() }()
	var root unix.Stat_t
	if err := unix.Fstat(fd, &root); err != nil {
		return 0, err
	}
	if int64(root.Uid) != int64(os.Getuid()) || root.Mode&0077 != 0 {
		return 0, fmt.Errorf("unsafe journal root")
	}
	var total int64
	entries := 0
	var walk func(*os.File, int) error
	walk = func(parent *os.File, depth int) error {
		if depth > journalDepthLimit {
			return errJournalCapacity
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			names, readErr := parent.Readdirnames(64)
			for _, name := range names {
				entries++
				if entries > journalEntryLimit {
					return errJournalCapacity
				}
				var st unix.Stat_t
				if err := unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
					return err
				}
				if st.Dev != root.Dev || int64(st.Uid) != int64(os.Getuid()) || st.Mode&0077 != 0 {
					return fmt.Errorf("unsafe journal entry")
				}
				switch st.Mode & unix.S_IFMT {
				case unix.S_IFREG:
					if st.Nlink != 1 || st.Size < 0 {
						return fmt.Errorf("unsafe journal file")
					}
					if st.Size >= limit-total {
						return errJournalCapacity
					}
					total += st.Size
				case unix.S_IFDIR:
					childFD, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
					if err != nil {
						return err
					}
					child := os.NewFile(uintptr(childFD), name)
					var opened unix.Stat_t
					err = unix.Fstat(childFD, &opened)
					if err == nil && (opened.Dev != st.Dev || opened.Ino != st.Ino) {
						err = fmt.Errorf("journal directory changed")
					}
					if err == nil {
						err = walk(child, depth+1)
					}
					_ = child.Close()
					if err != nil {
						return err
					}
				default:
					return fmt.Errorf("unsupported journal entry")
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					return nil
				}
				return readErr
			}
		}
	}
	err = walk(f, 0)
	return total, err
}

// Called under the coordinator lock before work that can grow the store.
// Unreadable usage is not permission to append. No records are retired/deleted.
func (c *Coordinator) admitJournal(ctx context.Context) error {
	_, err := journalUsage(ctx, filepath.Join(c.owner.Dir, "journal"), journalWatermark)
	if err != nil && ctx.Err() == nil {
		state := "journal_unavailable"
		if errors.Is(err, errJournalCapacity) {
			state = "journal_capacity_reached"
		}
		c.setState(state)
	}
	return err
}

func journalStopped(state string) bool {
	return state == "journal_unavailable" || state == "journal_capacity_reached"
}
