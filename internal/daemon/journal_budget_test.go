//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestJournalUsageCountsAllFilesAndRejectsBoundary(t *testing.T) {
	dir := privateDir(t)
	for _, name := range []string{"journal.ndjson", "heads.idx", ".tmp-index"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("1234"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := journalUsage(context.Background(), dir, 13); err != nil || got != 12 {
		t.Fatalf("usage %d %v", got, err)
	}
	if _, err := journalUsage(context.Background(), dir, 12); !errors.Is(err, errJournalCapacity) {
		t.Fatalf("boundary %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := journalUsage(ctx, dir, 13); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestJournalUsageRefusesUnsafeEntries(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "hardlink", "public", "depth"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateDir(t)
			p := filepath.Join(dir, "entry")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(privateDir(t), p)
			case "fifo":
				err = unix.Mkfifo(p, 0600)
			case "hardlink":
				src := filepath.Join(privateDir(t), "original")
				if err = os.WriteFile(src, []byte("keep"), 0600); err == nil {
					err = os.Link(src, p)
				}
			case "public":
				err = os.WriteFile(p, []byte("keep"), 0644)
				if err == nil {
					err = os.Chmod(p, 0644)
				}
			case "depth":
				p = dir
				for i := 0; i < journalDepthLimit+1; i++ {
					p = filepath.Join(p, "d")
					if err = os.Mkdir(p, 0700); err != nil {
						break
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = journalUsage(context.Background(), dir, journalWatermark); err == nil {
				t.Fatal("accepted unsafe/unbounded tree")
			}
		})
	}
}

func fillJournal(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "journal", "budget-fixture")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse fixture exercises logical accounting without consuming the watermark.
	err = f.Truncate(journalWatermark)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	return p
}

func TestJournalCapacityPreservesDurableStatusAndLatches(t *testing.T) {
	dir := privateDir(t)
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	previous, err := c.Sample(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	j := &uncertainJournal{sampleJournal: c.stream}
	c.stream = j
	p := fillJournal(t, dir)
	status, err := c.Sample(context.Background(), dir)
	if !errors.Is(err, errJournalCapacity) || status.State != "journal_capacity_reached" || status.RecordID != previous.RecordID || j.calls != 0 {
		t.Fatalf("capacity status=%+v calls=%d err=%v", status, j.calls, err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sample(context.Background(), dir); err == nil || j.calls != 0 {
		t.Fatal("resumed writes without restart")
	}
	if _, ok, err := j.Head(previous.RecordID); err != nil || !ok {
		t.Fatal("lost durable sample", err)
	}
}

func TestJournalCapacityRefusesStartupWithoutRewriting(t *testing.T) {
	dir := privateDir(t)
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	p := fillJournal(t, dir)
	if opened, err := Open(dir, nil); !errors.Is(err, errJournalCapacity) {
		if opened != nil {
			closeTest(t, opened)
		}
		t.Fatalf("startup: %v", err)
	}
	if info, err := os.Stat(p); err != nil || info.Size() != journalWatermark {
		t.Fatal("capacity refusal modified fixture", err)
	}
	o, err := Acquire(dir)
	if err != nil {
		t.Fatal("leaked ownership", err)
	}
	closeTest(t, o)
}

func TestJournalCapacitySkipsAttributionWork(t *testing.T) {
	dir := privateDir(t)
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	if _, err = c.Sample(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	o, err := newTestRootObserver(t, []string{privateDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	o.run = nil // Any walk would panic; admission must precede observation.
	fillJournal(t, dir)
	if _, err = c.Attribute(context.Background(), o, []DeepReason{DeepInitialBaseline}, nil); !errors.Is(err, errJournalCapacity) {
		t.Fatal(err)
	}
	if c.Status().Attribution != nil || c.attributionStream != nil {
		t.Fatal("published/created attribution on refused admission")
	}
}
