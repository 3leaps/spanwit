package space

import (
	"os"
	"strings"
	"time"
)

// Temp residue families.
//
// Invariant: family labels are fixed product vocabulary, never raw names. A
// temp entry is classified by matching its basename against this closed table;
// the basename itself is never stored, echoed, or used to build a label.
// Anything that does not match, including names that look like session ids,
// usernames, or project codenames, is counted under "other". Only aggregates
// (count, bytes, oldest, newest) leave this file, so the report keeps the
// TempPlane rule that a plane never names its children.
const (
	TempFamilyCodex    = "codex"
	TempFamilyOpencode = "opencode"
	TempFamilyBunx     = "bunx"
	TempFamilyTsx      = "tsx"
	TempFamilyOther    = "other"
)

// tempFamilyRules maps a basename shape to its fixed label. Order matters only
// for readability; the shapes are disjoint.
var tempFamilyRules = []struct {
	label string
	match func(name string) bool
}{
	{TempFamilyCodex, func(n string) bool { return strings.HasPrefix(n, ".com.openai.codex.") }},
	{TempFamilyOpencode, func(n string) bool { return n == "opencode" }},
	{TempFamilyBunx, func(n string) bool { return strings.HasPrefix(n, "bunx-") || strings.HasPrefix(n, ".bun-") }},
	{TempFamilyTsx, func(n string) bool { return strings.HasPrefix(n, "tsx-") }},
}

// classifyTempEntry returns the fixed family label for a temp basename.
func classifyTempEntry(name string) string {
	for _, rule := range tempFamilyRules {
		if rule.match(name) {
			return rule.label
		}
	}
	return TempFamilyOther
}

// TempFamily is one aggregate row keyed by a fixed label. It carries no path.
type TempFamily struct {
	Family  string    `json:"family"`
	Entries int64     `json:"entries"`
	Bytes   int64     `json:"bytes"`
	Oldest  time.Time `json:"oldest"`
	Newest  time.Time `json:"newest"`
}

// TempResidueSummary aggregates the direct children of the temp planes by
// family. HiddenEntries counts dot-prefixed children inside the totals; hidden
// entries are never excluded. Bytes are each entry's own lstat size (for a
// directory, its record, not its contents): counts are the signal, and the
// plane probe owns content sizing.
type TempResidueSummary struct {
	EntriesTotal  int64        `json:"entries_total"`
	HiddenEntries int64        `json:"hidden_entries"`
	Families      []TempFamily `json:"families"`
}

// summarizeTempEntries folds direct children of a temp root into family rows.
// sizeOf reports the bytes attributed to one child; it is supplied by the
// caller so the walk stays bounded and symlink-strict.
func summarizeTempEntries(entries []os.DirEntry, sizeOf func(os.DirEntry) int64) TempResidueSummary {
	rows := map[string]*TempFamily{}
	var summary TempResidueSummary
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		summary.EntriesTotal++
		if strings.HasPrefix(entry.Name(), ".") {
			summary.HiddenEntries++
		}
		label := classifyTempEntry(entry.Name())
		row, ok := rows[label]
		if !ok {
			row = &TempFamily{Family: label}
			rows[label] = row
		}
		row.Entries++
		row.Bytes += sizeOf(entry)
		mtime := info.ModTime().UTC()
		if row.Oldest.IsZero() || mtime.Before(row.Oldest) {
			row.Oldest = mtime
		}
		if mtime.After(row.Newest) {
			row.Newest = mtime
		}
	}
	for _, label := range []string{TempFamilyCodex, TempFamilyOpencode, TempFamilyBunx, TempFamilyTsx, TempFamilyOther} {
		if row, ok := rows[label]; ok {
			summary.Families = append(summary.Families, *row)
		}
	}
	return summary
}

// summarizeTempPlanes lists only the direct children of each admitted temp
// root and folds them into family rows. It never descends into a child and
// never follows a symlink; bytes are the child's own lstat size, so the
// summary is a bounded entry census, not a size walk (the plane probe owns
// sizing). Returns nil when no root can be listed.
func summarizeTempPlanes(roots []string) *TempResidueSummary {
	var all []os.DirEntry
	listed := false
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		listed = true
		all = append(all, entries...)
	}
	if !listed {
		return nil
	}
	summary := summarizeTempEntries(all, func(e os.DirEntry) int64 {
		info, err := e.Info()
		if err != nil || e.Type()&os.ModeSymlink != 0 {
			return 0
		}
		return info.Size()
	})
	return &summary
}
