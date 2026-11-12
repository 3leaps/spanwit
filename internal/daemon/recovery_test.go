//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/recordprims"
)

// The helper exits without Close to exercise OS ownership release and recovery
// from durable state rather than a graceful coordinator shutdown.
func TestRecoveryProcess(t *testing.T) {
	if os.Getenv("SPANWIT_RECOVERY_HELPER") != "1" {
		return
	}
	dir := os.Getenv("SPANWIT_RECOVERY_DIR")
	c, err := Open(dir, nil)
	if errors.Is(err, ErrBusy) {
		os.Exit(42)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(43)
	}
	s, err := c.Sample(context.Background(), dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(44)
	}
	if _, err := fmt.Fprintln(os.Stdout, s.RecordID); err != nil {
		os.Exit(45)
	}
	os.Exit(73)
}

func recoveryProcess(t *testing.T, dir string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryProcess$")
	cmd.Env = append(os.Environ(), "SPANWIT_RECOVERY_HELPER=1", "SPANWIT_RECOVERY_DIR="+dir)
	raw, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("helper: %v: %s", err, raw)
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	return strings.TrimSpace(string(raw)), exit.ExitCode()
}

func TestProcessOwnershipAndUncleanRecovery(t *testing.T) {
	dir := privateDir(t)
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw, code := recoveryProcess(t, dir); code != 42 {
		t.Fatalf("competing writer: %d %s", code, raw)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	id, code := recoveryProcess(t, dir)
	if code != 73 {
		t.Fatalf("unclean writer: %d %s", code, id)
	}
	recovered, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, recovered)
	if recovered.Status().State != "awaiting_sample" {
		t.Fatal("published an unverified cached sample")
	}
	if _, ok, err := recovered.stream.Head(id); err != nil || !ok {
		t.Fatalf("durable sample lost: %v %v", ok, err)
	}
	next, err := recovered.Sample(context.Background(), dir)
	if err != nil || next.RecordID == id {
		t.Fatalf("fresh sample: %+v %v", next, err)
	}
}

func TestUnsupportedStoreDoesNotReinitialize(t *testing.T) {
	dir := privateDir(t)
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal", "STORE")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata["format"] = "recordprims-fs/v999"
	modified, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, modified, 0600); err != nil {
		t.Fatal(err)
	}
	if c, err := Open(dir, nil); err == nil {
		closeTest(t, c)
		t.Fatal("accepted unsupported store")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(modified) {
		t.Fatal("rewrote unsupported store", err)
	}
	owner, err := Acquire(dir)
	if err != nil {
		t.Fatal("failed open leaked ownership", err)
	}
	closeTest(t, owner)
}

type uncertainJournal struct {
	sampleJournal
	id    string
	calls int
}

func (j *uncertainJournal) Append(ctx context.Context, w recordprims.Write) (recordprims.Receipt, error) {
	j.calls++
	receipt, err := j.sampleJournal.Append(ctx, w)
	if err != nil {
		return receipt, err
	}
	var record struct {
		ID string `json:"record_id"`
	}
	if err := json.Unmarshal(w.Record, &record); err != nil {
		return receipt, err
	}
	j.id = record.ID
	// The append reached durable storage, but the caller cannot establish that.
	return recordprims.Receipt{}, errors.New("injected response loss after durable append")
}

func TestUncertainAppendNeverPublishesOrRetries(t *testing.T) {
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
	status, err := c.Sample(context.Background(), dir)
	if err == nil || status.State != "journal_unavailable" || status.RecordID != previous.RecordID {
		t.Fatalf("uncertain append published: %+v %v", status, err)
	}
	if _, ok, err := j.Head(j.id); err != nil || !ok {
		t.Fatalf("fault did not follow durable append: %v %v", ok, err)
	}
	if _, err := c.Sample(context.Background(), dir); err == nil || j.calls != 1 {
		t.Fatalf("uncertain append retried: calls=%d err=%v", j.calls, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, restarted)
	if _, ok, err := restarted.stream.Head(j.id); err != nil || !ok {
		t.Fatalf("lost uncertain record on restart: %v %v", ok, err)
	}
	if _, err := restarted.Sample(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
}
