package filepolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3leaps/spanwit/internal/inventory"
)

func testPolicies(t *testing.T) []Policy {
	t.Helper()
	policies := DefaultPolicies()
	for i := range policies {
		if err := policies[i].Validate(); err != nil {
			t.Fatalf("default policy %q invalid: %v", policies[i].Name, err)
		}
	}
	return policies
}

func fileEntry(rel string, size int64, modTime time.Time) inventory.Entry {
	return inventory.Entry{
		RootID:            "root0",
		RelativePath:      rel,
		LocalAbsolutePath: "/tmp/fake/" + rel,
		EntryType:         "file",
		ApparentSizeBytes: size,
		ModifiedAt:        modTime,
		MetadataSource:    "lstat",
	}
}

func TestDefaultPoliciesValidate(t *testing.T) {
	for _, policy := range testPolicies(t) {
		if policy.Class != ClassDiagnosticOnly {
			t.Errorf("starter policy %q class = %q, want diagnostic-only", policy.Name, policy.Class)
		}
	}
}

func TestParseClassClosedSet(t *testing.T) {
	for _, class := range []string{ClassDiagnosticOnly, ClassWithheld} {
		if _, err := ParseClass(class); err != nil {
			t.Errorf("ParseClass(%q) = %v, want nil", class, err)
		}
	}
	// There is no prunable/safe class in this plane; extension alone must
	// never become deletion authority.
	for _, bad := range []string{"", "prunable", "safe", "safe_to_prune", "DIAGNOSTIC-ONLY"} {
		if _, err := ParseClass(bad); err == nil {
			t.Errorf("ParseClass(%q) = nil, want error", bad)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []Policy{
		{Name: "", Class: ClassDiagnosticOnly, Extensions: []string{"dmg"}},
		{Name: "x", Class: "prunable", Extensions: []string{"dmg"}},
		{Name: "x", Class: ClassDiagnosticOnly},
		{Name: "x", Class: ClassDiagnosticOnly, Extensions: []string{"."}},
		{Name: "x", Class: ClassDiagnosticOnly, Extensions: []string{"dmg"}, PathPatterns: []string{""}},
	}
	for i, policy := range cases {
		if err := policy.Validate(); err == nil {
			t.Errorf("case %d: Validate() = nil, want error", i)
		}
	}
}

func TestClassifyInstallersAndArchives(t *testing.T) {
	policies := testPolicies(t)
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	modTime := now.Add(-30 * 24 * time.Hour)

	cases := []struct {
		rel     string
		policy  string
		ext     string
		matched bool
	}{
		{"Downloads/Go.dmg", "downloaded-installer", "dmg", true},
		// Case-insensitive extension match.
		{"Downloads/image.ISO", "downloaded-installer", "iso", true},
		{"Downloads/setup.pkg", "downloaded-installer", "pkg", true},
		{"Downloads/tool.zip", "downloaded-archive", "zip", true},
		// Compound extensions resolve to the longest form.
		{"Downloads/backup.tar.gz", "downloaded-archive", "tar.gz", true},
		{"Downloads/small.tgz", "downloaded-archive", "tgz", true},
		{"Downloads/sub/nested.iso", "downloaded-installer", "iso", true},
		// False positives: the diagnostic extension is not the final suffix.
		{"Downloads/notes.dmg.bak", "", "", false},
		{"Downloads/dmg-notes.txt", "", "", false},
		{"Downloads/README", "", "", false},
		{"Downloads/noext", "", "", false},
	}
	for _, tc := range cases {
		finding, ok := Classify(fileEntry(tc.rel, 1<<20, modTime), policies, now)
		if ok != tc.matched {
			t.Errorf("Classify(%q) matched = %v, want %v", tc.rel, ok, tc.matched)
			continue
		}
		if !ok {
			continue
		}
		if finding.PolicyName != tc.policy || finding.Extension != tc.ext {
			t.Errorf("Classify(%q) = (%q, %q), want (%q, %q)",
				tc.rel, finding.PolicyName, finding.Extension, tc.policy, tc.ext)
		}
		if finding.Class != ClassDiagnosticOnly {
			t.Errorf("Classify(%q) class = %q, want diagnostic-only", tc.rel, finding.Class)
		}
		if finding.AgeDays != 30 {
			t.Errorf("Classify(%q) age_days = %d, want 30", tc.rel, finding.AgeDays)
		}
		if len(finding.Evidence) == 0 {
			t.Errorf("Classify(%q) has no evidence", tc.rel)
		}
	}
}

func TestDirectoriesCannotSatisfyFilePolicies(t *testing.T) {
	policies := testPolicies(t)
	now := time.Now()
	// A directory shaped like an installer is still not a file object.
	entry := fileEntry("Downloads/faked.dmg", 1<<20, now)
	entry.EntryType = "directory"
	if _, ok := Classify(entry, policies, now); ok {
		t.Error("directory entry matched a file policy; file policies classify files only")
	}
	entry.EntryType = ""
	if _, ok := Classify(entry, policies, now); ok {
		t.Error("typeless entry matched a file policy")
	}
}

func TestLocationScoping(t *testing.T) {
	now := time.Now()
	modTime := now.Add(-time.Hour)
	policy := Policy{
		Name: "scoped-installers", Class: ClassDiagnosticOnly,
		Extensions: []string{"dmg"}, PathPatterns: []string{"Downloads/**"},
		Narrative: "scoped",
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	policies := []Policy{policy}
	if _, ok := Classify(fileEntry("Downloads/Go.dmg", 1, modTime), policies, now); !ok {
		t.Error("in-scope file did not match scoped policy")
	}
	if _, ok := Classify(fileEntry("Desktop/Go.dmg", 1, modTime), policies, now); ok {
		t.Error("out-of-scope file matched scoped policy")
	}
}

func TestSummarizeHonestCoverage(t *testing.T) {
	zero := Summarize(nil, 0, CompleteCoverage())
	if !strings.Contains(zero.CoverageNote, "no files evaluated") {
		t.Errorf("empty report note = %q, want no-files-evaluated honesty", zero.CoverageNote)
	}
	none := Summarize(nil, 4, CompleteCoverage())
	if !strings.Contains(none.CoverageNote, "no files matched") {
		t.Errorf("zero-match note = %q, want explicit zero-match", none.CoverageNote)
	}
	findings := []Finding{
		{PolicyName: "downloaded-installer", Class: ClassDiagnosticOnly, SizeBytes: 10},
		{PolicyName: "downloaded-archive", Class: ClassDiagnosticOnly, SizeBytes: 20},
	}
	partial := Summarize(findings, 5, CompleteCoverage())
	if partial.MatchedFiles != 2 || partial.MatchedBytes != 30 {
		t.Errorf("partial totals = (%d, %d), want (2, 30)", partial.MatchedFiles, partial.MatchedBytes)
	}
	if !strings.Contains(partial.CoverageNote, "2 of 5") || !strings.Contains(partial.CoverageNote, "3 unclassified") {
		t.Errorf("partial note = %q, want matched/unclassified counts", partial.CoverageNote)
	}
	if strings.Contains(partial.CoverageNote, "INCOMPLETE") {
		t.Errorf("complete-walk note = %q, must not claim incomplete coverage", partial.CoverageNote)
	}
}

func TestCoverageFromGaps(t *testing.T) {
	gaps := []inventory.Gap{
		{Kind: "denied", AffectsCompleteness: true},
		{Kind: "denied", AffectsCompleteness: true},
		{Kind: "vanished", AffectsCompleteness: true},
		{Kind: "advisory", AffectsCompleteness: false},
	}
	coverage := CoverageFromGaps(inventory.LifecyclePartial, gaps)
	if coverage.Lifecycle != inventory.LifecyclePartial {
		t.Errorf("lifecycle = %q, want partial", coverage.Lifecycle)
	}
	if coverage.AffectingGapCount != 3 {
		t.Errorf("affecting gap count = %d, want 3 (advisory excluded)", coverage.AffectingGapCount)
	}
	if len(coverage.GapKinds) != 2 || coverage.GapKinds[0] != "denied" || coverage.GapKinds[1] != "vanished" {
		t.Errorf("gap kinds = %v, want sorted deduped [denied vanished]", coverage.GapKinds)
	}
	if coverage.IsComplete() {
		t.Error("partial coverage reports IsComplete")
	}
	// Unknown or missing lifecycles fail closed to unknown, never complete.
	for _, raw := range []string{"", "bogus"} {
		unknown := CoverageFromGaps(raw, nil)
		if unknown.Lifecycle != LifecycleUnknown {
			t.Errorf("lifecycle %q normalized to %q, want unknown", raw, unknown.Lifecycle)
		}
		if unknown.IsComplete() {
			t.Errorf("lifecycle %q reports IsComplete", raw)
		}
	}
	if !CompleteCoverage().IsComplete() {
		t.Error("CompleteCoverage does not report IsComplete")
	}
	// Conflicting input: a complete lifecycle with known affecting gaps is
	// contradictory and must fail closed to incomplete (devrev regression).
	conflicted := CoverageFromGaps(inventory.LifecycleComplete,
		[]inventory.Gap{{Kind: "denied", AffectsCompleteness: true}})
	if conflicted.IsComplete() {
		t.Error("lifecycle=complete with an affecting gap reports IsComplete")
	}
	conflictReport := Summarize(nil, 2, conflicted)
	if !strings.Contains(conflictReport.CoverageNote, "INCOMPLETE COVERAGE") {
		t.Errorf("conflicted note = %q, want INCOMPLETE COVERAGE", conflictReport.CoverageNote)
	}
	if text := RenderText(conflictReport); !strings.Contains(text, "coverage: INCOMPLETE") {
		t.Errorf("conflicted render lacks the incomplete banner:\n%s", text)
	}
}

func TestSummarizePartialCoverageIsNotACensus(t *testing.T) {
	// A canceled/denied/vanished partial walk that matched everything it saw
	// must still read as incomplete: unclassified-or-unseen is a lower bound.
	findings := []Finding{
		{PolicyName: "downloaded-installer", Class: ClassDiagnosticOnly, SizeBytes: 10},
	}
	gaps := []inventory.Gap{{Kind: "denied", AffectsCompleteness: true}}
	report := Summarize(findings, 1, CoverageFromGaps(inventory.LifecyclePartial, gaps))
	if report.Lifecycle != inventory.LifecyclePartial {
		t.Errorf("report lifecycle = %q, want partial", report.Lifecycle)
	}
	if report.AffectingGapCount != 1 {
		t.Errorf("affecting gap count = %d, want 1", report.AffectingGapCount)
	}
	for _, want := range []string{"INCOMPLETE COVERAGE", "lifecycle=partial", "affecting_gaps=1", "unclassified-or-unseen"} {
		if !strings.Contains(report.CoverageNote, want) {
			t.Errorf("partial note = %q, missing %q", report.CoverageNote, want)
		}
	}
	text := RenderText(report)
	if !strings.Contains(text, "coverage: INCOMPLETE") {
		t.Errorf("rendered text lacks the incomplete-coverage banner:\n%s", text)
	}
	// Unknown producer state is incomplete too.
	unknown := Summarize(nil, 0, Coverage{})
	if !strings.Contains(unknown.CoverageNote, "INCOMPLETE COVERAGE") || !strings.Contains(unknown.CoverageNote, "lifecycle=unknown") {
		t.Errorf("unknown-coverage note = %q, want incomplete/unknown", unknown.CoverageNote)
	}
}

func TestRenderTextSeparatesFoundFromRemovable(t *testing.T) {
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	finding, ok := Classify(
		fileEntry("Downloads/Go.dmg", 2<<20, now.Add(-10*24*time.Hour)),
		testPolicies(t), now)
	if !ok {
		t.Fatal("expected a finding to render")
	}
	withheld := finding
	withheld.Class = ClassWithheld
	withheld.PolicyName = "review-queue"
	report := Summarize([]Finding{finding, withheld}, 3, CompleteCoverage())
	text := RenderText(report)
	if !strings.Contains(text, "nothing here is safe to remove") {
		t.Errorf("rendered text lacks the found-vs-removable banner:\n%s", text)
	}
	if !strings.Contains(text, "Downloads/Go.dmg") {
		t.Errorf("rendered text lacks the matched path:\n%s", text)
	}
	if strings.Contains(text, "prunable") {
		t.Errorf("rendered text leaks prune-plane language:\n%s", text)
	}
	if !strings.Contains(text, "1 finding(s) recorded for review tooling, not shown") {
		t.Errorf("rendered text hides the withheld count:\n%s", text)
	}
	empty := RenderText(Summarize(nil, 2, CompleteCoverage()))
	if !strings.Contains(empty, "(no diagnostic findings)") {
		t.Errorf("empty render lacks the honest zero block:\n%s", empty)
	}
	if !strings.Contains(empty, "coverage: complete") {
		t.Errorf("complete render lacks the coverage banner:\n%s", empty)
	}
	// Withheld contract (pinned): retained in structured output with an
	// explicit class label, filtered from human display only.
	if len(report.Findings) != 2 {
		t.Fatalf("structured report dropped a finding; withheld must be retained")
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(report) = %v", err)
	}
	for _, want := range []string{`"class":"withheld"`, `"class":"diagnostic-only"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("structured output lacks %s; class must always be labeled", want)
		}
	}
	if strings.Contains(text, "review-queue") {
		t.Errorf("rendered text leaks a withheld policy name:\n%s", text)
	}
}

func TestRenderTextSanitizesHostileNames(t *testing.T) {
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	hostile := "a\r\x1b[2J\x1b[H\nspoofed: safe to remove\n.dmg"
	finding, ok := Classify(
		fileEntry("Downloads/"+hostile, 1<<20, now),
		testPolicies(t), now)
	if !ok {
		t.Fatal("hostile fixture did not classify; the injection test needs a match")
	}
	finding.Narrative = "narrative with \x1b]0;title\x07 escape"
	text := RenderText(Summarize([]Finding{finding}, 1, CompleteCoverage()))
	// Record content must not contribute control characters or escapes:
	// structural newlines come from the renderer only.
	if strings.Contains(text, "\r") || strings.Contains(text, "\x1b") || strings.Contains(text, "\x07") {
		t.Errorf("rendered text carries terminal-hostile bytes:\n%q", text)
	}
	// The injected words may survive inline (sanitized), but must never
	// start their own line: that is the spoofed-line shape.
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "spoofed:") {
			t.Errorf("rendered text preserves an injected line:\n%q", text)
			break
		}
	}
	// Banner survives on its own first line.
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || lines[0] != "File diagnostics (found only — nothing here is safe to remove)" {
		t.Errorf("banner line corrupted:\n%q", text)
	}
	// Legitimate non-ASCII names still pass through.
	if got := sanitize("Téléchargement/Übungs.iso"); got != "Téléchargement/Übungs.iso" {
		t.Errorf("sanitize mangled printable non-ASCII: %q", got)
	}
	if got := sanitize("plain.dmg"); got != "plain.dmg" {
		t.Errorf("sanitize altered a clean name: %q", got)
	}
}

func TestStructuredOutputNeverCarriesAbsolutePaths(t *testing.T) {
	now := time.Now()
	entry := fileEntry("Downloads/Go.dmg", 1<<20, now)
	entry.LocalAbsolutePath = "/Users/someone/Downloads/Go.dmg"
	finding, ok := Classify(entry, testPolicies(t), now)
	if !ok {
		t.Fatal("expected a finding")
	}
	raw, err := json.Marshal(Summarize([]Finding{finding}, 1, CompleteCoverage()))
	if err != nil {
		t.Fatalf("json.Marshal = %v", err)
	}
	serial := string(raw)
	if strings.Contains(serial, "local_absolute_path") {
		t.Errorf("structured output carries an absolute-path field:\n%s", serial)
	}
	if strings.Contains(serial, "/Users/someone") {
		t.Errorf("structured output leaks the absolute path value:\n%s", serial)
	}
}

func TestFixtureFilesExist(t *testing.T) {
	// Fixtures use realistic installer/archive names (case, compound
	// extensions, false positives); archive names may carry sensitive
	// material in the wild, so tests use synthetic names only.
	dir := filepath.Join("testdata", "downloads")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(testdata/downloads) = %v", err)
	}
	names := map[string]bool{}
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	for _, want := range []string{"Go.dmg", "image.ISO", "setup.pkg", "tool.zip",
		"backup.tar.gz", "small.tgz", "notes.dmg.bak", "dmg-notes.txt", "faked.dmg"} {
		if !names[want] {
			t.Errorf("fixture %q missing from testdata/downloads", want)
		}
	}
}
