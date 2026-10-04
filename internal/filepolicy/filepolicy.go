// Package filepolicy classifies neutral inventory file records under
// file-level diagnostic policies.
//
// Classification is diagnostic-only: a match records what a file looks like
// (installer, archive) plus age, size, and redownload narrative. It never
// authorizes deletion, never inspects file contents, and never enters the
// directory prune plane (engine.BuildPrunePlan only admits directories and
// knows nothing of this package).
package filepolicy

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/3leaps/spanwit/internal/inventory"
)

// Typed JSONL record names for review tooling. Withheld findings are
// included with their explicit class label (see the Report contract);
// consumers must key on class and never present withheld as found.
const (
	RecordFinding = "spanwit.diagnostic.finding.v1"
	RecordReport  = "spanwit.diagnostic.report.v1"
)

// EncodeJSONL writes one finding record per line followed by a terminal
// report record. Only relative paths are emitted; inventory absolute paths
// never enter the finding plane.
func EncodeJSONL(w io.Writer, report Report) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	for _, finding := range report.Findings {
		if err := encoder.Encode(struct {
			Type string  `json:"type"`
			Data Finding `json:"data"`
		}{Type: RecordFinding, Data: finding}); err != nil {
			return err
		}
	}
	return encoder.Encode(struct {
		Type string `json:"type"`
		Data Report `json:"data"`
	}{Type: RecordReport, Data: report})
}

// LifecycleUnknown marks coverage whose producer lifecycle is missing or
// unrecognized. Unknown coverage is incomplete coverage: it is never
// reported as a complete census.
const LifecycleUnknown = "unknown"

// Policy classes. The closed set is intentional: there is no prunable or
// safe-to-remove class in this plane. diagnostic-only findings are shown as
// found; withheld findings are recorded for later review tooling but excluded
// from diagnostic display.
const (
	ClassDiagnosticOnly = "diagnostic-only"
	ClassWithheld       = "withheld"
)

// ParseClass validates a policy class. Anything outside the closed set fails:
// extension alone must never become deletion authority.
func ParseClass(raw string) (string, error) {
	switch raw {
	case ClassDiagnosticOnly, ClassWithheld:
		return raw, nil
	default:
		return "", fmt.Errorf("unsupported file-policy class %q (use diagnostic-only|withheld)", raw)
	}
}

// Policy is one file-level diagnostic rule: an extension set plus optional
// location/context scoping over inventory relative paths.
type Policy struct {
	Name string
	// Description states what the class of files is, not what to do with it.
	Description string
	Class       string
	// Extensions are matched case-insensitively against the file basename.
	// Dotted entries match compound extensions ("tar.gz"). No leading dots.
	Extensions []string
	// PathPatterns optionally scopes the policy to a location/context
	// (doublestar syntax against the inventory relative path, e.g.
	// "Downloads/**"). Empty means any location; the containing directory is
	// still recorded as evidence.
	PathPatterns []string
	// Narrative is the rebuild/redownload story for matched files.
	Narrative string
}

// Validate normalizes a policy (lowercase extensions, trimmed dots) and
// rejects empty names, unknown classes, empty extension sets, and empty
// path patterns.
func (p *Policy) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("file policy name is required")
	}
	class, err := ParseClass(p.Class)
	if err != nil {
		return err
	}
	p.Class = class
	if len(p.Extensions) == 0 {
		return fmt.Errorf("file policy %q needs at least one extension", p.Name)
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(p.Extensions))
	for _, raw := range p.Extensions {
		ext := strings.ToLower(strings.Trim(strings.TrimSpace(raw), "."))
		if ext == "" {
			return fmt.Errorf("file policy %q has an empty extension", p.Name)
		}
		if _, ok := seen[ext]; ok {
			continue
		}
		seen[ext] = struct{}{}
		out = append(out, ext)
	}
	// Longest first so compound extensions win ("tar.gz" before "gz").
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	p.Extensions = out
	for _, pattern := range p.PathPatterns {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("file policy %q has an empty path pattern", p.Name)
		}
	}
	return nil
}

// matchExtension reports the policy extension matched by basename (already
// lowered). Longest-first order means compound extensions take precedence.
func (p Policy) matchExtension(loweredBase string) (string, bool) {
	for _, ext := range p.Extensions {
		if strings.HasSuffix(loweredBase, "."+ext) {
			return ext, true
		}
	}
	return "", false
}

// matchLocation reports whether the inventory relative path satisfies the
// policy's location scope. No patterns means any location matches.
func (p Policy) matchLocation(relativePath string) bool {
	if len(p.PathPatterns) == 0 {
		return true
	}
	rel := path.Clean(strings.TrimPrefix(relativePath, "./"))
	for _, pattern := range p.PathPatterns {
		if ok, _ := doublestar.PathMatch(pattern, rel); ok {
			return true
		}
	}
	return false
}

// Finding is one classified file: the policy, the evidence, and the neutral
// facts (size, age, narrative) a human or later review tooling needs.
type Finding struct {
	PolicyName   string   `json:"policy_name"`
	Class        string   `json:"class"`
	RelativePath string   `json:"relative_path"`
	Extension    string   `json:"extension"`
	Evidence     []string `json:"evidence"`
	SizeBytes    int64    `json:"size_bytes"`
	ModifiedAt   string   `json:"modified_at"`
	AgeDays      int64    `json:"age_days"`
	Narrative    string   `json:"narrative,omitempty"`
}

// Classify applies policies in order to one inventory entry and reports the
// first match. Non-file entries (including directories) never match: file
// policies classify file objects only. Classification reads the record's
// name, path, size, and mtime; it never opens the file.
func Classify(entry inventory.Entry, policies []Policy, now time.Time) (Finding, bool) {
	if entry.EntryType != "file" {
		return Finding{}, false
	}
	base := path.Base(entry.RelativePath)
	lowered := strings.ToLower(base)
	for _, policy := range policies {
		ext, ok := policy.matchExtension(lowered)
		if !ok {
			continue
		}
		if !policy.matchLocation(entry.RelativePath) {
			continue
		}
		location := path.Dir(entry.RelativePath)
		evidence := []string{"extension:" + ext, "location:" + location}
		for _, pattern := range policy.PathPatterns {
			if ok, _ := doublestar.PathMatch(pattern, path.Clean(entry.RelativePath)); ok {
				evidence = append(evidence, "path-pattern:"+pattern)
				break
			}
		}
		ageDays := int64(-1)
		if !entry.ModifiedAt.IsZero() {
			if age := now.Sub(entry.ModifiedAt); age >= 0 {
				ageDays = int64(age / (24 * time.Hour))
			} else {
				ageDays = 0
			}
		}
		return Finding{
			PolicyName:   policy.Name,
			Class:        policy.Class,
			RelativePath: entry.RelativePath,
			Extension:    ext,
			Evidence:     evidence,
			SizeBytes:    entry.ApparentSizeBytes,
			ModifiedAt:   entry.ModifiedAt.UTC().Format(time.RFC3339),
			AgeDays:      ageDays,
			Narrative:    policy.Narrative,
		}, true
	}
	return Finding{}, false
}

// Coverage carries the producer-side completeness of the inventory the
// findings were classified from. It is independent of how many files matched:
// a complete walk can leave files unclassified (normal), while a partial
// walk can match every file it saw (still incomplete).
type Coverage struct {
	// Lifecycle is the inventory terminal lifecycle (complete|partial|failed)
	// or LifecycleUnknown when the producer state is missing. Completeness
	// additionally requires zero affecting gaps (see IsComplete).
	Lifecycle string `json:"lifecycle"`
	// AffectingGapCount is the number of inventory gaps that affect
	// completeness (denied, vanished, canceled subtrees).
	AffectingGapCount int64 `json:"affecting_gap_count"`
	// GapKinds names the distinct affecting gap kinds observed, for evidence.
	GapKinds []string `json:"gap_kinds,omitempty"`
}

// CompleteCoverage returns Coverage for a fully observed inventory.
func CompleteCoverage() Coverage {
	return Coverage{Lifecycle: inventory.LifecycleComplete}
}

// CoverageFromGaps folds an inventory terminal lifecycle plus its gap stream
// into Coverage, counting only gaps that affect completeness.
func CoverageFromGaps(lifecycle string, gaps []inventory.Gap) Coverage {
	coverage := Coverage{Lifecycle: normalizeLifecycle(lifecycle)}
	seen := map[string]struct{}{}
	for _, gap := range gaps {
		if !gap.AffectsCompleteness {
			continue
		}
		coverage.AffectingGapCount++
		if _, ok := seen[gap.Kind]; !ok {
			seen[gap.Kind] = struct{}{}
			coverage.GapKinds = append(coverage.GapKinds, gap.Kind)
		}
	}
	sort.Strings(coverage.GapKinds)
	return coverage
}

// normalizeLifecycle passes through known inventory lifecycles and maps
// anything missing or unrecognized to LifecycleUnknown (fail-closed:
// unknown coverage is never reported as complete).
func normalizeLifecycle(lifecycle string) string {
	switch lifecycle {
	case inventory.LifecycleComplete, inventory.LifecyclePartial, inventory.LifecycleFailed:
		return lifecycle
	default:
		return LifecycleUnknown
	}
}

// IsComplete reports whether the coverage is a full census. Completeness
// requires both a complete producer lifecycle and zero affecting gaps: a
// complete lifecycle alongside known completeness-affecting gaps is
// contradictory input and fails closed to incomplete (the gap evidence is
// preserved on the report; only the completeness claim is withheld).
func (c Coverage) IsComplete() bool {
	return normalizeLifecycle(c.Lifecycle) == inventory.LifecycleComplete && c.AffectingGapCount == 0
}

// Report is the honest terminal account of one classification pass: what
// matched, how much it weighs, how many evaluated files carry no diagnostic
// class, and the producer coverage that bounds every count.
//
// Withheld contract (pinned): Report.Findings retains withheld findings with
// their explicit class label for later review tooling. Only human display
// (RenderText) filters them. Structured output must always carry the class
// field and must never present a withheld finding as found.
type Report struct {
	Findings          []Finding `json:"findings"`
	MatchedFiles      int       `json:"matched_files"`
	MatchedBytes      int64     `json:"matched_bytes"`
	Evaluated         int       `json:"evaluated_files"`
	Lifecycle         string    `json:"lifecycle"`
	AffectingGapCount int64     `json:"affecting_gap_count"`
	GapKinds          []string  `json:"gap_kinds,omitempty"`
	CoverageNote      string    `json:"coverage_note"`
}

// Summarize folds findings into a report over an evaluated file count and
// the producer coverage. Zero matches, unclassified files, and incomplete
// coverage are stated separately: unclassified counts describe the policy
// plane, lifecycle/gaps describe the walk. An unclassified count under
// partial coverage is a lower bound on unseen files, never a census.
func Summarize(findings []Finding, evaluated int, coverage Coverage) Report {
	coverage.Lifecycle = normalizeLifecycle(coverage.Lifecycle)
	report := Report{
		Findings:          findings,
		Evaluated:         evaluated,
		Lifecycle:         coverage.Lifecycle,
		AffectingGapCount: coverage.AffectingGapCount,
		GapKinds:          coverage.GapKinds,
	}
	for _, finding := range findings {
		report.MatchedFiles++
		report.MatchedBytes += finding.SizeBytes
	}
	unmatched := evaluated - report.MatchedFiles
	if unmatched < 0 {
		unmatched = 0
	}
	var scope string
	if coverage.IsComplete() {
		scope = fmt.Sprintf("%d of %d evaluated files matched a diagnostic policy; %d unclassified",
			report.MatchedFiles, evaluated, unmatched)
	} else {
		scope = fmt.Sprintf(
			"INCOMPLETE COVERAGE (lifecycle=%s, affecting_gaps=%d): %d of %d evaluated files matched; %d+ unclassified-or-unseen",
			coverage.Lifecycle, coverage.AffectingGapCount, report.MatchedFiles, evaluated, unmatched)
	}
	switch {
	case evaluated == 0 && coverage.IsComplete():
		report.CoverageNote = "no files evaluated; no diagnostic class assigned"
	case evaluated == 0:
		report.CoverageNote = "no files evaluated under " + scope
	case report.MatchedFiles == 0 && coverage.IsComplete():
		report.CoverageNote = fmt.Sprintf(
			"no files matched any diagnostic policy (%d evaluated, all unclassified)", evaluated)
	default:
		report.CoverageNote = scope
	}
	return report
}

// RenderText renders a report for humans. The banner states the plane's
// limit up front: found is classification, never safe-to-remove. A second
// banner states producer coverage whenever the walk was not complete, so a
// partial inventory can never read as a census.
func RenderText(report Report) string {
	var out strings.Builder
	out.WriteString("File diagnostics (found only — nothing here is safe to remove)\n")
	coverage := Coverage{Lifecycle: report.Lifecycle, AffectingGapCount: report.AffectingGapCount}
	if coverage.IsComplete() {
		out.WriteString("coverage: complete\n")
	} else {
		fmt.Fprintf(&out, "coverage: INCOMPLETE (lifecycle=%s, affecting_gaps=%d) — unclassified counts are lower bounds, not a census\n",
			sanitize(report.Lifecycle), report.AffectingGapCount)
	}
	shown := 0
	for _, finding := range report.Findings {
		if finding.Class == ClassWithheld {
			continue
		}
		shown++
		fmt.Fprintf(&out, "found  %-10s  %4dd old  %-22s  %s\n",
			humanSize(finding.SizeBytes), finding.AgeDays,
			sanitize(finding.PolicyName), sanitize(finding.RelativePath))
		if finding.Narrative != "" {
			fmt.Fprintf(&out, "       narrative: %s\n", sanitize(finding.Narrative))
		}
	}
	withheld := len(report.Findings) - shown
	if withheld > 0 {
		fmt.Fprintf(&out, "withheld: %d finding(s) recorded for review tooling, not shown\n", withheld)
	}
	if shown == 0 && withheld == 0 {
		out.WriteString("(no diagnostic findings)\n")
	}
	out.WriteString(report.CoverageNote + "\n")
	return out.String()
}

// sanitize strips terminal-hostile content from record-sourced strings before
// human rendering. Scanned filenames (and config-driven policy names or
// narratives) may carry CR/LF, ANSI/OSC escape sequences, or other control
// characters that could overwrite the found-only banner or inject spoofed
// lines into terminal or log output. Printable runes pass through (including
// non-ASCII names); anything else becomes '?'. Structural newlines are added
// only by the renderer, never by record content.
func sanitize(s string) string {
	if utf8.ValidString(s) {
		return strings.Map(func(r rune) rune {
			if r == utf8.RuneError {
				return '?'
			}
			if unicode.IsPrint(r) {
				return r
			}
			return '?'
		}, s)
	}
	var out strings.Builder
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError {
			out.WriteByte('?')
			if size <= 1 {
				s = s[1:]
			} else {
				s = s[size:]
			}
			continue
		}
		if unicode.IsPrint(r) {
			out.WriteRune(r)
		} else {
			out.WriteByte('?')
		}
		s = s[size:]
	}
	return out.String()
}

func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"K", "M", "G", "T"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f%s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1fP", value)
}

// DefaultPolicies returns the starter set: downloaded installers and
// archives. Every starter policy is diagnostic-only; extension alone never
// means safe to delete.
func DefaultPolicies() []Policy {
	return []Policy{
		{
			Name:        "downloaded-installer",
			Description: "Vendor installer or disk image (dmg, iso, pkg, exe, msi)",
			Class:       ClassDiagnosticOnly,
			Extensions:  []string{"dmg", "iso", "pkg", "mpkg", "exe", "msi"},
			Narrative:   "Typically redownloadable from the vendor; verify source and checksum before any action. Not safe to remove on extension alone.",
		},
		{
			Name:        "downloaded-archive",
			Description: "Compressed bundle (zip, tar.gz, tgz, rar, 7z)",
			Class:       ClassDiagnosticOnly,
			Extensions:  []string{"tar.gz", "tgz", "zip", "rar", "7z"},
			Narrative:   "Compressed bundle; contents are unknown without inspection (out of scope for this plane). Not safe to remove on extension alone.",
		},
	}
}
