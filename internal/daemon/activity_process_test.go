//go:build darwin || linux

package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/capacity"
)

func TestActivityProcessHelper(t *testing.T) {
	if os.Getenv("SPANWIT_ACTIVITY_TEST_HELPER") != "1" {
		return
	}
	fmt.Println("ready")
	time.Sleep(30 * time.Second)
}

func TestBuildActivityRealProcessDefersOnlyItsRoot(t *testing.T) {
	activeRoot, otherRoot := privateDir(t), privateDir(t)
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	executable := filepath.Join(privateDir(t), "go")
	output, err := os.OpenFile(executable, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal(copyErr, closeErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestActivityProcessHelper$")
	child.Dir = activeRoot
	child.Env = append(os.Environ(), "SPANWIT_ACTIVITY_TEST_HELPER=1")
	child.WaitDelay = time.Second
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper readiness %q %v", line, err)
	}
	runner := capacity.ExecRunner{MaxOutput: buildActivityMaxOutput}
	// A sandbox may hide even our child. Make that missing platform evidence
	// explicit; only a visible helper can qualify the actual lsof selection.
	raw, err := runner.Run(ctx, buildActivityLsofPath(), "-nP", "-F0pcfn", "-p", strconv.Itoa(child.Process.Pid))
	if err != nil || !bytes.Contains(raw, []byte("cgo\x00")) || !bytes.Contains(raw, []byte("n"+activeRoot+"\x00")) {
		t.Skipf("lsof cannot observe helper command/cwd on this host: %v", err)
	}
	active, err := (BuildActivityProbe{}).Probe(ctx, []string{activeRoot, otherRoot})
	if err != nil || !active[activeRoot] || active[otherRoot] {
		t.Fatalf("real activity map=%v err=%v", active, err)
	}
	c, err := Open(privateDir(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	if _, err = c.Sample(ctx, otherRoot); err != nil {
		t.Fatal(err)
	}
	o, err := newTestRootObserver(t, []string{activeRoot, otherRoot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Attribute(ctx, o, []DeepReason{DeepInitialBaseline}, active); err != nil {
		t.Fatal(err)
	}
	rows := c.Status().Attribution.Rows
	if rows[0].Observation.Coverage != "deferred" || rows[1].Observation.Coverage != "complete" {
		t.Fatalf("unexpected root coverage %+v", rows)
	}
}
