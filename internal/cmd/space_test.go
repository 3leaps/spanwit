package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fulmenhq/gofulmen/appidentity"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/space"
)

func TestRunSpaceCapacityOnlyJSONSkipsInventorySurface(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	samples := 0
	collector := capacity.Collector{
		Platform:     "linux",
		Architecture: "amd64",
		Now: func() time.Time {
			return time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)
		},
		FilesystemSampler: func(_ context.Context, path string) (capacity.FilesystemSample, error) {
			samples++
			return capacity.FilesystemSample{
				Path: path, Mount: "/", VolumeID: "fs:1", FSType: "ext4",
				Total: 1000, Used: 600, Available: 400,
			}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := runSpace(&appidentity.Identity{BinaryName: "spanwit"}, &stdout, &stderr, spaceRunOpts{
		path: home, format: "json", capacityOnly: true, capacityCollector: &collector,
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if samples != 1 {
		t.Fatalf("filesystem samples=%d want 1", samples)
	}
	raw := stdout.Bytes()
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV2, raw); err != nil {
		t.Fatalf("schema: %v\n%s", err, raw)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"verified_reclaimable", "prune_handoff", "hotspots", "unknown"} {
		if _, present := document[key]; present {
			t.Fatalf("capacity-only serialized inventory surface %q", key)
		}
	}
}

func TestRunSpaceCapacityOnlyRejectsExplicitInventoryFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runSpace(&appidentity.Identity{BinaryName: "spanwit"}, &stdout, &stderr, spaceRunOpts{
		path: ".", format: "json", capacityOnly: true,
		changedInventoryFlags: []string{"--top", "--class"},
	})
	if code != 3 || !strings.Contains(stderr.String(), "--top, --class") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunSpaceHeldOpenJSONHonorsDefaultDisclosure(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	collector := capacity.Collector{
		Platform: "darwin",
		Now: func() time.Time {
			return time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
		},
		FilesystemSampler: func(_ context.Context, path string) (capacity.FilesystemSample, error) {
			return capacity.FilesystemSample{
				Path: path, Mount: "/System/Volumes/Data", VolumeID: "fs:1", FSType: "apfs",
				Total: 1000, Used: 600, Available: 400,
			}, nil
		},
		DarwinRunner: capacity.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
			return nil, &capacity.RunError{Code: "command_failed"}
		}),
		HeldOpenRunner: capacity.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
			return []byte(
				"p10\x00cCodex\x00u501\x00\n" +
					"f7\x00D0x1\x00i99\x00k0\x00s100\x00n/Users/example/private.bin\x00\n",
			), nil
		}),
		ManagedRoots: []capacity.ManagedRoot{},
	}
	var stdout, stderr bytes.Buffer
	code := runSpace(
		&appidentity.Identity{BinaryName: "spanwit"},
		&stdout,
		&stderr,
		spaceRunOpts{
			path: home, format: "json", capacityOnly: true, heldOpen: true,
			sessionBoundary: "before-running", capacityCollector: &collector,
		},
	)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV2, stdout.Bytes()); err != nil {
		t.Fatalf("schema: %v\n%s", err, stdout.String())
	}
	if strings.Contains(stdout.String(), "private.bin") ||
		strings.Contains(stdout.String(), `"path": "/Users/example`) {
		t.Fatalf("default disclosure leaked held-open path:\n%s", stdout.String())
	}
	var report space.CapacityOnlyReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	held := report.CapacityAccounting.Planes.HeldOpen
	if held.CollectionStatus != capacity.StatusMeasured || held.Disclosure != "none" ||
		held.UniqueObjects == nil || *held.UniqueObjects != 1 ||
		report.CapacityAccounting.Capture.SessionBoundary != "before-running" {
		t.Fatalf("held=%#v capture=%#v", held, report.CapacityAccounting.Capture)
	}

	stdout.Reset()
	stderr.Reset()
	code = runSpace(
		&appidentity.Identity{BinaryName: "spanwit"},
		&stdout,
		&stderr,
		spaceRunOpts{
			path: home, format: "json", capacityOnly: true, heldOpen: true,
			disclose: "paths", disclosureChanged: true, capacityCollector: &collector,
		},
	)
	if code != 0 || !strings.Contains(stdout.String(), `"disclosure": "full"`) ||
		!strings.Contains(stdout.String(), "private.bin") {
		t.Fatalf("explicit disclosure code=%d stderr=%s\n%s", code, stderr.String(), stdout.String())
	}
}

func TestRunSpaceHeldOpenAndCompareFlagGuards(t *testing.T) {
	identity := &appidentity.Identity{BinaryName: "spanwit"}
	tests := []struct {
		name string
		opts spaceRunOpts
		want string
	}{
		{
			name: "held open requires capacity",
			opts: spaceRunOpts{path: ".", format: "text", heldOpen: true},
			want: "--held-open currently requires --capacity-only",
		},
		{
			name: "disclosure requires held open",
			opts: spaceRunOpts{
				path: ".", format: "text", capacityOnly: true,
				disclose: "paths", disclosureChanged: true,
			},
			want: "--disclose requires --held-open",
		},
		{
			name: "two stdin inputs refused",
			opts: spaceRunOpts{
				format: "json", comparePaths: []string{"-", "-"},
				compareStdin: strings.NewReader("{}"),
			},
			want: "stdin for at most one input",
		},
		{
			name: "compare capture flags refused",
			opts: spaceRunOpts{
				format: "json", comparePaths: []string{"left", "right"},
				changedCompareFlags: []string{"--held-open"},
			},
			want: "--compare does not accept capture or inventory flags",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runSpace(identity, &stdout, &stderr, tt.opts)
			if code != 3 || !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestRunSpaceCompareExplicitFileAndStdin(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	collector := capacity.Collector{
		Platform:     "linux",
		Architecture: "amd64",
		Now: func() time.Time {
			return time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
		},
		FilesystemSampler: func(_ context.Context, path string) (capacity.FilesystemSample, error) {
			return capacity.FilesystemSample{
				Path: path, Mount: "/", VolumeID: "fs:1", FSType: "ext4",
				Total: 1000, Used: 600, Available: 400,
			}, nil
		},
	}
	var capture, captureErr bytes.Buffer
	code := runSpace(
		&appidentity.Identity{BinaryName: "spanwit"},
		&capture,
		&captureErr,
		spaceRunOpts{
			path: home, format: "json", capacityOnly: true, capacityCollector: &collector,
		},
	)
	if code != 0 {
		t.Fatalf("capture code=%d stderr=%s", code, captureErr.String())
	}
	right := filepath.Join(t.TempDir(), "right.json")
	if err := os.WriteFile(right, capture.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code = runSpace(
		&appidentity.Identity{BinaryName: "spanwit"},
		&stdout,
		&stderr,
		spaceRunOpts{
			format:       "json",
			comparePaths: []string{"-", right},
			compareStdin: strings.NewReader(capture.String()),
			compareNow: func() time.Time {
				return time.Date(2026, 7, 31, 13, 0, 0, 0, time.UTC)
			},
		},
	)
	if code != 0 {
		t.Fatalf("compare code=%d stderr=%s", code, stderr.String())
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceCompareV1, stdout.Bytes()); err != nil {
		t.Fatalf("schema: %v\n%s", err, stdout.String())
	}
	var report capacity.CompareReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Left.Path != "-" || report.Right.Path != right ||
		report.GeneratedAt != "2026-07-31T13:00:00Z" {
		t.Fatalf("report=%#v", report)
	}
}

func TestSanitizeTerminalTextRemovesFormatAndControlCharacters(t *testing.T) {
	got := sanitizeTerminalText("safe\u202Espoof\x1b[31m\nnext")
	for _, forbidden := range []rune{'\u202E', '\x1b', '\n'} {
		if strings.ContainsRune(got, forbidden) {
			t.Fatalf("sanitizer retained %U in %q", forbidden, got)
		}
	}
}

func TestRunSpaceCapacityOnlyTextAndJSONExposeCoverageStory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	omitted := 3
	collector := capacity.Collector{
		Platform: "darwin",
		FilesystemSampler: func(_ context.Context, path string) (capacity.FilesystemSample, error) {
			return capacity.FilesystemSample{
				Path: path, Mount: "/System/Volumes/Data", VolumeID: "fs:1", FSType: "apfs",
				Total: 10_000, Used: 6_000, Available: 4_000,
			}, nil
		},
		DarwinRunner: capacity.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
			return nil, &capacity.RunError{Code: "command_failed"}
		}),
		ManagedRootMeasurer: func(context.Context, string, int) capacity.ManagedMeasurement {
			return capacity.ManagedMeasurement{
				Bytes: 4096, Present: true, Complete: false,
				Gaps: []capacity.CoverageGap{{
					Code:           "permission_denied",
					Scope:          "system_managed",
					Detail:         "curated denied\x1b[31m\nspoof",
					OmittedObjects: &omitted,
				}},
			}
		},
	}
	identity := &appidentity.Identity{BinaryName: "spanwit"}

	var stdout, stderr bytes.Buffer
	code := runSpace(identity, &stdout, &stderr, spaceRunOpts{
		path: home, format: "text", capacityOnly: true, capacityCollector: &collector,
	})
	if code != 0 {
		t.Fatalf("text code=%d stderr=%s", code, stderr.String())
	}
	text := stdout.String()
	for _, want := range []string{
		"Accounting story (planes are not summed)",
		"Held-open collection is not_requested",
		"Known:",
		"system_managed.apple_assets_v2.allocated: 4.0 KiB",
		"status=partial; basis=allocated_blocks; bound=lower",
		"Unavailable:",
		"container coverage gap: command_failed",
		"system_managed coverage gap: permission_denied",
		"omitted_objects=3",
		"Unexplained hints:",
		"(none; no numeric residual is computed)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q:\n%s", want, text)
		}
	}
	if strings.ContainsRune(text, '\x1b') || strings.Contains(text, "\nspoof") {
		t.Fatalf("terminal control characters were not sanitized:\n%q", text)
	}

	stdout.Reset()
	stderr.Reset()
	code = runSpace(identity, &stdout, &stderr, spaceRunOpts{
		path: home, format: "json", capacityOnly: true, capacityCollector: &collector,
	})
	if code != 0 {
		t.Fatalf("json code=%d stderr=%s", code, stderr.String())
	}
	raw := stdout.Bytes()
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV2, raw); err != nil {
		t.Fatalf("schema: %v\n%s", err, raw)
	}
	for _, want := range []string{
		`"status": "partial"`,
		`"bound": "lower"`,
		`"code": "permission_denied"`,
		`"omitted_objects": 3`,
		`"unexplained_hints": []`,
	} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("json missing %q:\n%s", want, raw)
		}
	}
}

func TestRunSpaceCapacityOnlyCreatesNoApplicationFiles(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	tmp := filepath.Join(base, "tmp")
	root := filepath.Join(home, "patient")
	for _, dir := range []string{home, tmp, root} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "data.bin"), 128)
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmp)
	before := listInventoryTree(t, base)

	collector := capacity.Collector{
		Platform: "linux",
		FilesystemSampler: func(_ context.Context, path string) (capacity.FilesystemSample, error) {
			return capacity.FilesystemSample{
				Path: path, Mount: "/", VolumeID: "fs:1", FSType: "ext4",
				Total: 1000, Used: 600, Available: 400,
			}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := runSpace(&appidentity.Identity{BinaryName: "spanwit"}, &stdout, &stderr, spaceRunOpts{
		path: root, format: "json", capacityOnly: true, capacityCollector: &collector,
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	after := listInventoryTree(t, base)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("capacity-only changed application/patient tree:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestRunSpace_TextAndJSON(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "rust", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "rust", "target", "debug", "a.o"), 2048)

	identity := &appidentity.Identity{BinaryName: "spanwit", Description: "test"}

	var stdout, stderr bytes.Buffer
	code := runSpace(identity, &stdout, &stderr, spaceRunOpts{
		path:              tmp,
		minSize:           "1K",
		format:            "text",
		top:               10,
		includeHomeCaches: false,
	})
	if code != 0 && code != 1 {
		t.Fatalf("exit %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Primary write volume pressure") {
		t.Fatalf("missing pressure section: %s", out)
	}
	if !strings.Contains(out, "Can free now") {
		t.Fatalf("missing can-free-now section: %s", out)
	}
	if !strings.Contains(out, "Withheld") {
		t.Fatalf("missing withheld section: %s", out)
	}
	if !strings.Contains(out, "Prune handoff") {
		t.Fatalf("missing prune handoff section: %s", out)
	}
	// space itself must not offer an execute mode; educational notes about prune are OK.
	if strings.Contains(out, "Mode:") && strings.Contains(out, "EXECUTE") {
		t.Fatalf("space must not present an execute mode: %s", out)
	}

	stdout.Reset()
	stderr.Reset()
	code = runSpace(identity, &stdout, &stderr, spaceRunOpts{
		path:              tmp,
		minSize:           "1K",
		format:            "json",
		top:               10,
		includeHomeCaches: false,
	})
	if code != 0 && code != 1 {
		t.Fatalf("json exit %d stderr=%s", code, stderr.String())
	}
	raw := stdout.Bytes()
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
		t.Fatalf("schema: %v\n%s", err, raw)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["execute"]; ok {
		t.Fatal("json must not contain execute field")
	}
	if payload["$schema"] != "https://schemas.3leaps.dev/spanwit/space-report/v1.json" {
		t.Fatalf("schema id: %v", payload["$schema"])
	}
}

func TestRunSpace_BadFormat(t *testing.T) {
	identity := &appidentity.Identity{BinaryName: "spanwit"}
	var stdout, stderr bytes.Buffer
	code := runSpace(identity, &stdout, &stderr, spaceRunOpts{
		path:   t.TempDir(),
		format: "yaml",
	})
	if code != 3 {
		t.Fatalf("want usage exit 3, got %d", code)
	}
}

func TestRunSpace_BadClassFilter(t *testing.T) {
	identity := &appidentity.Identity{BinaryName: "spanwit"}
	var stdout, stderr bytes.Buffer
	code := runSpace(identity, &stdout, &stderr, spaceRunOpts{
		path:         t.TempDir(),
		format:       "text",
		classFilters: []string{"diagnostic"}, // alias rejected; exact diagnostic-only required
	})
	if code != 3 {
		t.Fatalf("want usage exit 3 for bad class, got %d stderr=%s", code, stderr.String())
	}
}

func TestRunSpace_ClassFilterTextEcho(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, filepath.Join(tmp, "rust", "Cargo.toml"), 32)
	writeFile(t, filepath.Join(tmp, "rust", "target", "debug", "a.o"), 2048)

	identity := &appidentity.Identity{BinaryName: "spanwit", Description: "test"}
	var stdout, stderr bytes.Buffer
	code := runSpace(identity, &stdout, &stderr, spaceRunOpts{
		path:              tmp,
		minSize:           "1K",
		format:            "text",
		top:               10,
		includeHomeCaches: false,
		classFilters:      []string{"prunable"},
	})
	if code != 0 && code != 1 {
		t.Fatalf("exit %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "filters: class=prunable") {
		t.Fatalf("missing filter echo:\n%s", out)
	}
	// Actionability order: Can free now before Withheld before Notes.
	iFree := strings.Index(out, "Can free now")
	iWith := strings.Index(out, "Withheld")
	if iFree < 0 || iWith < 0 || iFree > iWith {
		t.Fatalf("actionability order wrong: free=%d withheld=%d\n%s", iFree, iWith, out)
	}
}

func TestRunSpace_BadDomainSegmentRejectedBeforeAnalyze(t *testing.T) {
	identity := &appidentity.Identity{BinaryName: "spanwit"}
	var stdout, stderr bytes.Buffer
	code := runSpace(identity, &stdout, &stderr, spaceRunOpts{
		path:          t.TempDir(),
		format:        "json",
		domainFilters: []string{"development.-rust"},
	})
	if code != 3 {
		t.Fatalf("want usage exit 3 for schema-illegal domain, got %d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "Diagnosing space") {
		t.Fatalf("must reject before analysis: %s", stderr.String())
	}
}

func TestWriteSpaceText_ActionabilityOrderCompleted(t *testing.T) {
	var buf bytes.Buffer
	report := space.Report{
		Root:     "/tmp/root",
		MinSize:  "none",
		Top:      5,
		MaxDepth: 2,
		Pressure: space.Pressure{Level: "ok", TotalHuman: "1G", UsedHuman: "1G", AvailHuman: "0B"},
		Verified: space.VerifiedSection{
			Candidates: []space.Entry{
				{Path: "/tmp/root/app/target", SizeHuman: "1B", State: "prunable"},
				{Path: "/tmp/root/old/target", SizeHuman: "1B", State: "withheld", WithheldReason: "min_size"},
			},
			PrunableCandidates: 1, PrunableHuman: "1B",
			WithheldCandidates: 1, WithheldHuman: "1B",
		},
		Hotspots: []space.Entry{{
			Path: "/tmp/cache", SizeHuman: "2B", State: "diagnostic-only",
			Label: "test", RebuildExpectation: "high",
		}},
		Unverified: space.UnverifiedSection{
			Count: 1, TotalHuman: "3B",
			Entries: []space.Entry{{Path: "/tmp/root/x/target", SizeHuman: "3B", State: "unverified"}},
		},
		Unknown: []space.Entry{{
			Path: "/tmp/root/misc", SizeHuman: "4B", State: "unknown",
		}},
		Notes: []string{"Placement advice example"},
	}
	writeSpaceText(&buf, report, false)
	out := buf.String()
	markers := []string{
		"Primary write volume pressure",
		"Can free now",
		"Prune handoff",
		"Withheld",
		"Named caches",
		"Name-shaped unverified",
		"Top unknown directories",
		"Notes",
	}
	prev := -1
	for _, m := range markers {
		idx := strings.Index(out, m)
		if idx < 0 {
			t.Fatalf("missing section %q in:\n%s", m, out)
		}
		if idx < prev {
			t.Fatalf("section %q out of order (idx=%d prev=%d):\n%s", m, idx, prev, out)
		}
		prev = idx
	}
	// Early path should not duplicate pressure when earlyTriageAlreadyPrinted.
	var early bytes.Buffer
	writeSpaceText(&early, report, true)
	earlyOut := early.String()
	if strings.Count(earlyOut, "Primary write volume pressure") != 0 {
		t.Fatalf("completed report after early triage must not re-print primary pressure:\n%s", earlyOut)
	}
	if !strings.Contains(earlyOut, "Can free now") {
		t.Fatalf("early-completed path still needs can free now:\n%s", earlyOut)
	}
}

func TestWriteSpaceText_PartialMarkersOnHotspotAndUnknown(t *testing.T) {
	var buf bytes.Buffer
	report := space.Report{
		Root:     "/tmp/root",
		MinSize:  "none",
		Top:      5,
		MaxDepth: 2,
		Pressure: space.Pressure{Level: "ok", TotalHuman: "1G", UsedHuman: "1G", AvailHuman: "0B"},
		Verified: space.VerifiedSection{
			Candidates: []space.Entry{{
				Path: "/tmp/root/app/target", SizeHuman: "1B", State: "prunable", SizeIncomplete: true,
			}},
			PrunableHuman: "1B", WithheldHuman: "0B", SizeIncomplete: true,
			PrunableSizeIncomplete: true, WithheldSizeIncomplete: false,
		},
		Hotspots: []space.Entry{{
			Path: "/tmp/cache", SizeHuman: "2B", State: "diagnostic-only",
			Label: "test", RebuildExpectation: "high", SizeIncomplete: true,
		}},
		Unverified: space.UnverifiedSection{
			Count: 1, TotalHuman: "3B", SizeIncomplete: true,
			Entries: []space.Entry{{Path: "/tmp/root/x/target", SizeHuman: "3B", State: "unverified", SizeIncomplete: true}},
		},
		Unknown: []space.Entry{{
			Path: "/tmp/root/misc", SizeHuman: "4B", State: "unknown", SizeIncomplete: true,
		}},
	}
	writeSpaceText(&buf, report, false)
	out := buf.String()
	if !strings.Contains(out, "total is an observed partial lower bound") {
		t.Fatalf("missing unverified aggregate partial note:\n%s", out)
	}
	// Incomplete sizes must render as lower bounds (≥), never bare exact totals.
	for _, want := range []string{"≥1B", "≥2B", "≥3B", "≥4B"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing lower-bound size %q in:\n%s", want, out)
		}
	}
	// Incomplete prunable must not mark the exact withheld total as a lower bound.
	if strings.Contains(out, "Withheld total: ≥0B") {
		t.Fatalf("withheld exact zero must not render as lower bound:\n%s", out)
	}
	if !strings.Contains(out, "Withheld total: 0B") {
		t.Fatalf("want exact withheld total 0B:\n%s", out)
	}
	// Hotspot and unknown lines: path present and size column uses ≥ prefix.
	for _, path := range []string{"/tmp/cache", "/tmp/root/misc"} {
		idx := strings.Index(out, path)
		if idx < 0 {
			t.Fatalf("missing path %s in:\n%s", path, out)
		}
		// Walk back to start of line for the size column.
		lineStart := strings.LastIndex(out[:idx], "\n") + 1
		lineEnd := strings.Index(out[idx:], "\n")
		if lineEnd < 0 {
			lineEnd = len(out) - idx
		}
		line := out[lineStart : idx+lineEnd]
		if !strings.Contains(line, "≥") {
			t.Fatalf("line for %s missing lower-bound marker: %q\nfull:\n%s", path, line, out)
		}
	}
}

func TestWriteSpaceText_IndependentSubtotalBounds(t *testing.T) {
	// Incomplete withheld must not mark complete prunable total as a lower bound.
	var buf bytes.Buffer
	report := space.Report{
		Root: "/tmp/root", MinSize: "none", Top: 5, MaxDepth: 2,
		Pressure: space.Pressure{Level: "ok", TotalHuman: "1G", UsedHuman: "1G", AvailHuman: "0B"},
		Verified: space.VerifiedSection{
			Candidates: []space.Entry{
				{Path: "/p", SizeHuman: "100B", State: "prunable", SizeIncomplete: false},
				{Path: "/w", SizeHuman: "50B", State: "withheld", WithheldReason: "min_size", SizeIncomplete: true},
			},
			PrunableCandidates: 1, PrunableHuman: "100B",
			WithheldCandidates: 1, WithheldHuman: "50B",
			SizeIncomplete: true, PrunableSizeIncomplete: false, WithheldSizeIncomplete: true,
		},
	}
	writeSpaceText(&buf, report, false)
	out := buf.String()
	if !strings.Contains(out, "Prunable total: 100B") || strings.Contains(out, "Prunable total: ≥100B") {
		t.Fatalf("complete prunable total must stay exact:\n%s", out)
	}
	if !strings.Contains(out, "Withheld total: ≥50B") {
		t.Fatalf("incomplete withheld total must be lower bound:\n%s", out)
	}

	// Reverse direction: incomplete prunable, complete withheld.
	buf.Reset()
	report.Verified = space.VerifiedSection{
		Candidates: []space.Entry{
			{Path: "/p", SizeHuman: "100B", State: "prunable", SizeIncomplete: true},
			{Path: "/w", SizeHuman: "50B", State: "withheld", WithheldReason: "age", SizeIncomplete: false},
		},
		PrunableCandidates: 1, PrunableHuman: "100B",
		WithheldCandidates: 1, WithheldHuman: "50B",
		SizeIncomplete: true, PrunableSizeIncomplete: true, WithheldSizeIncomplete: false,
	}
	writeSpaceText(&buf, report, false)
	out = buf.String()
	if !strings.Contains(out, "Prunable total: ≥100B") {
		t.Fatalf("incomplete prunable total must be lower bound:\n%s", out)
	}
	if !strings.Contains(out, "Withheld total: 50B") || strings.Contains(out, "Withheld total: ≥50B") {
		t.Fatalf("complete withheld total must stay exact:\n%s", out)
	}
}

func TestBoundAwareSizeHuman(t *testing.T) {
	if got := boundAwareSizeHuman("1.2G", true); got != "≥1.2G" {
		t.Fatalf("incomplete: got %q", got)
	}
	if got := boundAwareSizeHuman("1.2G", false); got != "1.2G" {
		t.Fatalf("complete: got %q", got)
	}
	if got := boundAwareSizeHuman("≥1.2G", true); got != "≥1.2G" {
		t.Fatalf("already marked: got %q", got)
	}
}

func TestWriteTempPlanesText_RootsAndBoundsOnly(t *testing.T) {
	var buf bytes.Buffer
	writeTempPlanesText(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("nil coverage must print nothing, got %q", buf.String())
	}
	n := int64(3 << 30)
	m := int64(512 << 20)
	writeTempPlanesText(&buf, &space.TempPlaneCoverage{Trigger: space.PressureCritical, Planes: []space.TempPlane{
		{Root: "/private/tmp", Kind: space.TempPlaneKindShared, SizeStatus: space.TempPlaneSizePartial,
			SizeBytes: &n, SizeBasis: "apparent", FollowUp: "spanwit space /private/tmp"},
		{Root: "/private/var/folders/x/T", Kind: space.TempPlaneKindUser, SizeStatus: space.TempPlaneSizeMeasured,
			SizeBytes: &m, SizeBasis: "apparent", FollowUp: "spanwit space /private/var/folders/x/T"},
		{Root: "/srv/tmp", Kind: space.TempPlaneKindShared, SizeStatus: space.TempPlaneSizeUnavailable,
			SizeBasis: "apparent", FollowUp: "spanwit space /srv/tmp"},
	}})
	out := buf.String()
	for _, want := range []string{
		"Temp planes (not walked", "≥3.0G", "partial", "→ spanwit space /private/tmp",
		"512.0M", "measured", "?", "unavailable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "≥512") {
		t.Errorf("measured size must not render as a lower bound:\n%s", out)
	}
}

// TMPDIR is environment-controlled, so a temp-plane root can carry terminal
// control sequences; the text block must neutralize them.
func TestWriteTempPlanesText_SanitizesControlCharacters(t *testing.T) {
	var buf bytes.Buffer
	n := int64(1)
	hostile := "/tmp/x\x1b[2J\x1b]0;pwned\x07\u202e"
	writeTempPlanesText(&buf, &space.TempPlaneCoverage{Trigger: space.PressureWarn, Planes: []space.TempPlane{{
		Root: hostile, Kind: space.TempPlaneKindUser, SizeStatus: space.TempPlaneSizeMeasured,
		SizeBytes: &n, SizeBasis: "apparent", FollowUp: "spanwit space '" + hostile + "'",
	}}})
	out := buf.String()
	for _, bad := range []string{"\x1b", "\x07", "\u202e"} {
		if strings.Contains(out, bad) {
			t.Fatalf("control sequence %q reached terminal output: %q", bad, out)
		}
	}
	if !strings.Contains(out, "/tmp/x") {
		t.Fatalf("sanitized root lost its readable prefix: %q", out)
	}
}

// Completed-with-gaps keeps exit 1 (Exit-Codes: partial success) but says so
// explicitly in the report and on stderr; a clean run is exit 0 / complete.
func TestRunSpace_CompletionMirrorsExitContract(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	clean := t.TempDir()
	gappy := t.TempDir()
	locked := filepath.Join(gappy, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	for _, tc := range []struct {
		name      string
		path      string
		wantCode  int
		wantLife  string
		wantText  string
		wantStErr bool
	}{
		{"complete", clean, 0, "complete", "Result: complete", false},
		{"partial", gappy, 1, "partial", "Result: partial — ", true},
	} {
		for _, format := range []string{"text", "json"} {
			var stdout, stderr bytes.Buffer
			code := runSpace(&appidentity.Identity{BinaryName: "spanwit"}, &stdout, &stderr, spaceRunOpts{
				path: tc.path, format: format, maxDepth: -1, includeHomeCaches: false,
			})
			if code != tc.wantCode {
				t.Fatalf("%s/%s: code=%d want %d stderr=%s", tc.name, format, code, tc.wantCode, stderr.String())
			}
			summary := strings.Contains(stderr.String(), "partial (exit 1). The report is usable; exit status is not prune authorization.")
			if summary != tc.wantStErr {
				t.Fatalf("%s/%s: stderr summary present=%v want %v: %s", tc.name, format, summary, tc.wantStErr, stderr.String())
			}
			if format == "text" {
				if !strings.Contains(stdout.String(), tc.wantText) {
					t.Fatalf("%s: text missing %q", tc.name, tc.wantText)
				}
				continue
			}
			raw := stdout.Bytes()
			if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw); err != nil {
				t.Fatalf("%s: schema: %v", tc.name, err)
			}
			var doc struct {
				Completion struct {
					Lifecycle    string `json:"lifecycle"`
					WarningCount int    `json:"warning_count"`
				} `json:"completion"`
				Warnings []string `json:"warnings"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Completion.Lifecycle != tc.wantLife || doc.Completion.WarningCount != len(doc.Warnings) {
				t.Fatalf("%s: completion=%+v warnings=%d", tc.name, doc.Completion, len(doc.Warnings))
			}
		}
	}
}
