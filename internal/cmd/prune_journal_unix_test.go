//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"
	"github.com/fulmenhq/gofulmen/logging"
	"github.com/spf13/cobra"

	"github.com/3leaps/spanwit/internal/daemon"
)

func journalRig(t *testing.T) (identity *appidentity.Identity, configFile, target, stateDir string) {
	t.Helper()
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "project", "Cargo.toml"), 64)
	target = filepath.Join(tmp, "project", "target")
	writeFile(t, filepath.Join(target, "debug", "app.o"), 2048)
	mustAgeTree(t, target, time.Now().Add(-48*time.Hour))
	configFile = filepath.Join(tmp, "config.yaml")
	cfg := "version: 1\ndefaults:\n  min_size: 1K\n  min_age: 24h\npaths:\n  - path: " + tmp + "\n    max_depth: 3\n    targets:\n      - signature: development.rust.cargo-target\n"
	if err := os.WriteFile(configFile, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	identity = &appidentity.Identity{BinaryName: "spanwit", EnvPrefix: "SPANWIT_", ConfigName: "spanwit"}
	logger, err := logging.NewCLI(identity.BinaryName)
	if err != nil {
		t.Fatal(err)
	}
	configPath = ""
	Initialize(context.Background(), identity, logger)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	// Not created yet: the first --execute creates the private store.
	stateDir = filepath.Join(base, ".local", "state", "spanwit", "daemon")
	return identity, configFile, target, stateDir
}

func TestPruneExecuteIsJournaled(t *testing.T) {
	identity, configFile, target, stateDir := journalRig(t)
	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--config", configFile, "--format", "json", "--execute", "--state-dir", stateDir})
	stdout := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("prune: %v", err)
		}
	})
	var out struct {
		Execution struct {
			Journal     string `json:"journal"`
			OperationID string `json:"operation_id"`
			Assertion   string `json:"assertion"`
		} `json:"execution"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatal(err)
	}
	if out.Execution.Journal != "journaled" || out.Execution.OperationID == "" || out.Execution.Assertion != "open" {
		t.Fatalf("execution %+v", out.Execution)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target not deleted: %v", err)
	}
	c, err := daemon.Open(stateDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	recs, err := c.ExecutionRecords(context.Background(), out.Execution.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, r := range recs {
		kinds = append(kinds, r.Kind+":"+r.Outcome)
	}
	if strings.Join(kinds, ",") != "outcome:applied,finished:,manifest:,started:" {
		t.Fatalf("journal %v", kinds)
	}
}

func TestPruneExecuteRefusesWhileDaemonHoldsStoreBeforePlanning(t *testing.T) {
	identity, _, target, stateDir := journalRig(t)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := daemon.Open(stateDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	cmd := newPruneCmd(identity)
	// The config does not exist: a run that planned first would fail on it.
	cmd.SetArgs([]string{"--config", "/nonexistent/spanwit.yaml", "--execute", "--state-dir", stateDir})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "writer_busy") || !strings.Contains(err.Error(), "daemon service stop") {
		t.Fatalf("want writer_busy with the remedy before planning, got %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target touched: %v", err)
	}
}

func TestPruneExecuteRefusesWhenStoreCannotInitialise(t *testing.T) {
	identity, configFile, target, _ := journalRig(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	bad := filepath.Join(base, "shared")
	if err := os.Mkdir(bad, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bad, 0o777); err != nil { // group/other writable: not a private state dir
		t.Fatal(err)
	}
	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--config", configFile, "--execute", "--state-dir", bad})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("execute ran without a journal")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target deleted without a journal: %v", err)
	}
}

func TestPruneApprovalFlagRequiresExactPlanAndExecute(t *testing.T) {
	identity, configFile, _, stateDir := journalRig(t)
	for _, args := range [][]string{
		{"--config", configFile, "--execute", "--approval", "a-1", "--state-dir", stateDir},
		{"--from-exact-plan", "/p.json", "--approval", "a-1", "--state-dir", stateDir},
		{"--from-exact-plan", "/p.json", "--execute", "--operation-id", "op", "--state-dir", stateDir},
	} {
		cmd := newPruneCmd(identity)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

// TestPruneExecuteRefusesUnsupportedStateFilesystemBeforePlanning points
// --state-dir at a filesystem the journal never accepts (devfs on macOS,
// proc on Linux) and requires a refusal naming it and the remedy, before
// the missing config is ever read.
func TestPruneExecuteRefusesUnsupportedStateFilesystemBeforePlanning(t *testing.T) {
	identity, _, target, _ := journalRig(t)
	bad := "/dev/spanwit-state"
	if _, err := os.Stat("/proc/self"); err == nil {
		bad = "/proc/spanwit-state"
	}
	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--config", "/nonexistent/spanwit.yaml", "--execute", "--state-dir", bad})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--state-dir") || !strings.Contains(err.Error(), "filesystem") ||
		(!strings.Contains(err.Error(), "devfs") && !strings.Contains(err.Error(), "unknown (0x")) {
		t.Fatalf("want a filesystem refusal naming the type and remedy, got %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target touched: %v", err)
	}
}

func TestPruneSortRefusedUnderApproval(t *testing.T) {
	identity, _, _, stateDir := journalRig(t)
	cmd := newPruneCmd(identity)
	cmd.SetArgs([]string{"--from-exact-plan", "/p.json", "--execute", "--approval", "a-1", "--sort", "idle", "--state-dir", stateDir})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--sort is not valid with --approval") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Lstat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("refused flag combination still opened the store: %v", err)
	}
}

func TestApprovalStatusCreatesNothing(t *testing.T) {
	_, _, _, stateDir := journalRig(t)
	if _, err := runApprovalCmd(t, "status", "--state-dir", stateDir, "missing"); err == nil {
		t.Fatal("missing approval reported")
	}
	if _, err := runApprovalCmd(t, "operation", "--state-dir", stateDir, "missing"); err == nil {
		t.Fatal("operation on absent state reported")
	}
	if _, err := os.Lstat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("status created the state dir: %v", err)
	}
}

func TestReplayRendersRecordedResult(t *testing.T) {
	r := &daemon.OperationReport{OperationID: "op-1", ApprovalID: "a-1", Started: true, ManifestKnown: true, Finished: true,
		Candidates: []daemon.CandidateResult{{Index: 0, Path: "/r/a", Outcome: "applied", Recorded: true}, {Index: 1, Path: "/r/b", Outcome: "applied", Recorded: true}}}
	var text bytes.Buffer
	if err := writeOperationReport(&text, r, "text"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "op-1") || !strings.Contains(text.String(), "/r/b") || !strings.Contains(text.String(), "nothing re-executed") {
		t.Fatalf("text %q", text.String())
	}
	var js bytes.Buffer
	if err := writeOperationReport(&js, r, "json"); err != nil {
		t.Fatal(err)
	}
	var back daemon.OperationReport
	if err := json.Unmarshal(js.Bytes(), &back); err != nil || len(back.Candidates) != 2 || back.OperationID != "op-1" {
		t.Fatalf("json %v %s", err, js.String())
	}
	if err := reportError(r); err != nil {
		t.Fatalf("complete result reported as error: %v", err)
	}
	gap := *r
	gap.Finished, gap.Unknown = false, 1
	gap.Candidates = []daemon.CandidateResult{r.Candidates[0], {Index: 1, Path: "/r/b", Outcome: "unknown"}}
	if err := reportError(&gap); err == nil || !strings.Contains(err.Error(), "1 unknown") {
		t.Fatalf("crash gap not surfaced: %v", err)
	}
	var gapText bytes.Buffer
	_ = writeOperationReport(&gapText, &gap, "text")
	if !strings.Contains(gapText.String(), "unknown      /r/b") {
		t.Fatalf("gap text %q", gapText.String())
	}
}

func TestApprovalInspectionRefusedUnderReadOnly(t *testing.T) {
	resetPolicyForTest(t)
	root := &cobra.Command{Use: "spanwit"}
	root.PersistentFlags().Bool(readOnlyFlagName, false, "")
	root.PersistentFlags().Bool(approvedOnlyFlagName, false, "")
	group := newApprovalCmd(&appidentity.Identity{BinaryName: "spanwit"})
	setCommandCapability(group, CapabilityMutating)
	root.AddCommand(group)
	t.Setenv("SPANWIT_READ_ONLY", "1")
	for _, name := range []string{"status", "operation"} {
		sub, _, err := group.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		if err := enforceMutationPolicy(sub); err == nil || !strings.Contains(err.Error(), "forbids mutating") {
			t.Fatalf("%s under read_only: %v", name, err)
		}
	}
}

func TestReplayKeepsOriginalNonSuccess(t *testing.T) {
	base := daemon.OperationReport{OperationID: "op", Started: true, ManifestKnown: true, Finished: true}
	for name, mutate := range map[string]func(*daemon.OperationReport){
		"limits exceeded": func(r *daemon.OperationReport) {
			r.FinishDetail = "limits_exceeded"
			r.Candidates = []daemon.CandidateResult{{Path: "/a", Outcome: "unattempted", Reason: "limits_exceeded", Recorded: true}}
		},
		"expired": func(r *daemon.OperationReport) {
			r.FinishDetail = "grant_expired_or_revoked"
			r.Candidates = []daemon.CandidateResult{{Path: "/a", Outcome: "applied", Recorded: true}, {Path: "/b", Outcome: "unattempted", Recorded: true}}
		},
		"cancelled": func(r *daemon.OperationReport) {
			r.FinishDetail = "cancelled"
			r.Candidates = []daemon.CandidateResult{{Path: "/a", Outcome: "unattempted", Recorded: true}}
		},
		"plan failed": func(r *daemon.OperationReport) { r.FinishDetail = "plan failed: x" },
	} {
		r := base
		mutate(&r)
		if err := reportError(&r); err == nil {
			t.Errorf("%s replayed as success", name)
		}
	}
	ok := base
	ok.Candidates = []daemon.CandidateResult{{Path: "/a", Outcome: "applied", Recorded: true}, {Path: "/b", Outcome: "withheld", Reason: "age", Recorded: true}}
	if err := reportError(&ok); err != nil {
		t.Fatalf("clean run with a withheld row reported as failure: %v", err)
	}
}
