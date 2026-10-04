package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/filepolicy"
	"github.com/3leaps/spanwit/internal/inventory"
)

func diagnoseFixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"Go.dmg", "image.ISO", "backup.tar.gz", "notes.dmg.bak", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runDiagnoseBuffers(t *testing.T, opts diagnoseRunOpts) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts.now = time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	code := runDiagnose(context.Background(), inventoryTestIdentity(), &stdout, &stderr, opts)
	return code, stdout.String(), stderr.String()
}

func TestDiagnoseTextSeparatesFoundFromRemovable(t *testing.T) {
	root := diagnoseFixtureTree(t)
	code, stdout, _ := runDiagnoseBuffers(t, diagnoseRunOpts{roots: []string{root}, format: "text", quiet: true})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 on complete coverage", code)
	}
	for _, want := range []string{
		"File diagnostics (found only — nothing here is safe to remove)",
		"coverage: complete",
		"downloaded-installer",
		"Go.dmg",
		"backup.tar.gz",
		"3 of 5 evaluated files matched",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("text output lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "notes.dmg.bak") && strings.Contains(stdout, "found") {
		lines := strings.Split(stdout, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "found") && strings.Contains(line, "notes.dmg.bak") {
				t.Errorf("false positive rendered as found:\n%s", line)
			}
		}
	}
	// Disclosure bound: the temp dir's absolute path must not appear.
	if strings.Contains(stdout, root) {
		t.Errorf("text output leaks the absolute root:\n%s", stdout)
	}
}

func TestDiagnoseJSONLTypedRecords(t *testing.T) {
	root := diagnoseFixtureTree(t)
	code, stdout, _ := runDiagnoseBuffers(t, diagnoseRunOpts{roots: []string{root}, format: "jsonl", quiet: true})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 4 { // 3 findings + terminal report
		t.Fatalf("got %d JSONL lines, want 4:\n%s", len(lines), stdout)
	}
	for _, line := range lines[:3] {
		var record struct {
			Type string `json:"type"`
			Data struct {
				Class        string `json:"class"`
				RelativePath string `json:"relative_path"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Type != filepolicy.RecordFinding {
			t.Errorf("record type = %q, want %q", record.Type, filepolicy.RecordFinding)
		}
		if record.Data.Class != filepolicy.ClassDiagnosticOnly {
			t.Errorf("record class = %q, want diagnostic-only", record.Data.Class)
		}
		if filepath.IsAbs(record.Data.RelativePath) {
			t.Errorf("record carries an absolute path: %q", record.Data.RelativePath)
		}
	}
	var terminal struct {
		Type string `json:"type"`
		Data struct {
			Lifecycle    string `json:"lifecycle"`
			Evaluated    int    `json:"evaluated_files"`
			Matched      int    `json:"matched_files"`
			CoverageNote string `json:"coverage_note"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[3]), &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Type != filepolicy.RecordReport {
		t.Errorf("terminal type = %q, want %q", terminal.Type, filepolicy.RecordReport)
	}
	if terminal.Data.Lifecycle != inventory.LifecycleComplete || terminal.Data.Evaluated != 5 || terminal.Data.Matched != 3 {
		t.Errorf("terminal report = %+v, want complete/5/3", terminal.Data)
	}
	if strings.Contains(stdout, "local_absolute_path") || strings.Contains(stdout, root) {
		t.Errorf("JSONL output leaks absolute paths:\n%s", stdout)
	}
}

func TestDiagnoseWithheldContractEndToEnd(t *testing.T) {
	root := diagnoseFixtureTree(t)
	policies := filepolicy.DefaultPolicies()
	withheld := filepolicy.Policy{
		Name: "review-queue", Class: filepolicy.ClassWithheld,
		Extensions: []string{"bak"}, Narrative: "held",
	}
	policies = append([]filepolicy.Policy{withheld}, policies...)
	code, text, _ := runDiagnoseBuffers(t, diagnoseRunOpts{roots: []string{root}, format: "text", quiet: true, policies: policies})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.Contains(text, "notes.dmg.bak") {
		t.Errorf("text display leaks a withheld path:\n%s", text)
	}
	if !strings.Contains(text, "recorded for review tooling, not shown") {
		t.Errorf("text omits the withheld count:\n%s", text)
	}
	_, jsonOut, _ := runDiagnoseBuffers(t, diagnoseRunOpts{roots: []string{root}, format: "jsonl", quiet: true, policies: policies})
	if !strings.Contains(jsonOut, `"class":"withheld"`) || !strings.Contains(jsonOut, "notes.dmg.bak") {
		t.Errorf("JSONL must retain withheld findings with class label:\n%s", jsonOut)
	}
}

func TestDiagnoseCollectorPartialCoverage(t *testing.T) {
	now := time.Now()
	collector := &diagnoseCollector{policies: filepolicy.DefaultPolicies(), now: now}
	if err := collector.Entry(inventory.Entry{EntryType: "file", RelativePath: "Go.dmg", ApparentSizeBytes: 8, ModifiedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := collector.Entry(inventory.Entry{EntryType: "file", RelativePath: "plain.txt", ModifiedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := collector.Gap(inventory.Gap{Kind: "denied", AffectsCompleteness: true}); err != nil {
		t.Fatal(err)
	}
	if err := collector.Summary(inventory.Summary{Lifecycle: inventory.LifecyclePartial}); err != nil {
		t.Fatal(err)
	}
	report := collector.report()
	if report.Evaluated != 2 || report.MatchedFiles != 1 {
		t.Errorf("report = evaluated %d matched %d, want 2/1", report.Evaluated, report.MatchedFiles)
	}
	if report.Lifecycle != inventory.LifecyclePartial || report.AffectingGapCount != 1 {
		t.Errorf("report coverage = (%q, %d), want (partial, 1)", report.Lifecycle, report.AffectingGapCount)
	}
	if !strings.Contains(report.CoverageNote, "INCOMPLETE COVERAGE") {
		t.Errorf("note = %q, want incomplete", report.CoverageNote)
	}
}

// TestDiagnoseNoRootExecutablePath is the command-level regression for the
// usage exit: `diagnose` with no root must exit 3 on the real CLI path, not
// the exit-1 cobra arg rejection. It builds the binary and execs it because
// runDiagnose unit tests bypass cobra argument handling.
func TestDiagnoseNoRootExecutablePath(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "spanwit")
	build := exec.Command("go", "build", "-o", bin, "./cmd/spanwit")
	build.Dir = diagnoseModuleRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build spanwit: %v\n%s", err, out)
	}
	probe := exec.Command(bin, "diagnose")
	probe.Env = os.Environ()
	var stderr bytes.Buffer
	probe.Stderr = &stderr
	err := probe.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("diagnose with no root: err = %v, want exit error", err)
	}
	if exitErr.ExitCode() != 3 {
		t.Errorf("diagnose with no root: exit = %d, want 3", exitErr.ExitCode())
	}
	if !strings.Contains(stderr.String(), "at least one explicit root") {
		t.Errorf("stderr lacks the usage message:\n%s", stderr.String())
	}
}

func diagnoseModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above working directory")
		}
		dir = parent
	}
}

func TestDiagnoseUsageErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runDiagnose(context.Background(), inventoryTestIdentity(), &stdout, &stderr, diagnoseRunOpts{}); code != 3 {
		t.Errorf("no roots: exit = %d, want 3", code)
	}
	if code := runDiagnose(context.Background(), inventoryTestIdentity(), &stdout, &stderr,
		diagnoseRunOpts{roots: []string{"."}, format: "yaml"}); code != 3 {
		t.Errorf("bad format: exit = %d, want 3", code)
	}
	if code := runDiagnose(context.Background(), nil, &stdout, &stderr,
		diagnoseRunOpts{roots: []string{"."}}); code != 3 {
		t.Errorf("nil identity: exit = %d, want 3", code)
	}
	stderr.Reset()
	if code := runDiagnose(context.Background(), inventoryTestIdentity(), &stdout, &stderr,
		diagnoseRunOpts{roots: []string{t.TempDir()}, stallTimeout: -time.Second}); code != 3 {
		t.Errorf("negative stall timeout: exit = %d, want 3", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("usage rejection wrote payload: %q", stdout.String())
	}
}

func TestDiagnoseCommandExposesRemoteAndStallFlags(t *testing.T) {
	cmd := newDiagnoseCmd(inventoryTestIdentity())
	remote := cmd.Flags().Lookup("include-remote")
	if remote == nil || remote.DefValue != "false" {
		t.Fatalf("--include-remote missing or not off by default: %+v", remote)
	}
	stall := cmd.Flags().Lookup("stall-timeout")
	if stall == nil || stall.DefValue != inventory.DefaultStallTimeout.String() {
		t.Fatalf("--stall-timeout missing or wrong default: %+v", stall)
	}
}

// TestDiagnosePassesWalkGuardsAndAlertsUnderQuiet pins the wiring: flag values
// reach the walker (0 = never abandon), and a stall alert reaches stderr even
// with --quiet, carrying no path.
func TestDiagnosePassesWalkGuardsAndAlertsUnderQuiet(t *testing.T) {
	root := diagnoseFixtureTree(t)
	cases := []struct {
		name          string
		includeRemote bool
		flag          time.Duration
		want          time.Duration
	}{
		{"default", false, inventory.DefaultStallTimeout, inventory.DefaultStallTimeout},
		{"zero waits", true, 0, -1},
		{"explicit", false, 5 * time.Second, 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got inventory.Options
			orig := diagnoseInventoryRun
			t.Cleanup(func() { diagnoseInventoryRun = orig })
			diagnoseInventoryRun = func(ctx context.Context, opts inventory.Options, sink inventory.Sink) (inventory.Summary, error) {
				got = opts
				if opts.OnStall == nil {
					t.Fatal("diagnose did not wire OnStall")
				}
				opts.OnStall(inventory.StallAlert{Pending: 1, Waited: 10 * time.Second, Timeout: opts.StallTimeout})
				return orig(ctx, opts, sink)
			}
			_, _, stderr := runDiagnoseBuffers(t, diagnoseRunOpts{
				roots: []string{root}, quiet: true,
				includeRemote: tc.includeRemote, stallTimeout: tc.flag,
			})
			if got.IncludeRemote != tc.includeRemote || got.StallTimeout != tc.want {
				t.Fatalf("options include_remote=%v stall_timeout=%v, want %v/%v",
					got.IncludeRemote, got.StallTimeout, tc.includeRemote, tc.want)
			}
			if !strings.Contains(stderr, "directory open(s) not responding") {
				t.Fatalf("stall alert missing under --quiet: %q", stderr)
			}
			if strings.Contains(stderr, root) {
				t.Fatalf("stall alert disclosed a path: %q", stderr)
			}
		})
	}
}
