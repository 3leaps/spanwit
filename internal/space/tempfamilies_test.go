package space

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

func TestClassifyTempEntry_ClosedTable(t *testing.T) {
	cases := map[string]string{
		".com.openai.codex.aBcDeF":         TempFamilyCodex,
		"opencode":                         TempFamilyOpencode,
		"bunx-1000-example@latest":         TempFamilyBunx,
		".bun-1000-0123456789abcdef.dylib": TempFamilyBunx,
		"tsx-1000":                         TempFamilyTsx,
		"opencode-client-falcon":           TempFamilyOther,
		"widgetrunner-checkpoint-xYz123":   TempFamilyOther,
		"":                                 TempFamilyOther,
	}
	for name, want := range cases {
		if got := classifyTempEntry(name); got != want {
			t.Errorf("classifyTempEntry(%q) = %q, want %q", name, got, want)
		}
	}
}

// Adversarial: names that look like codenames, session ids and usernames must
// collapse into "other", and none of them may appear anywhere in the JSON.
func TestTempResidueSummary_NeverEchoesRawNames(t *testing.T) {
	root := t.TempDir()
	sensitive := []string{
		"projectkestrel-harness-7f3a9c",      // codename-like
		"session-01J9Z8Q4K2M7N6P5R3S1T0V9W8", // session-id-like
		"alice.smith-workspace",              // username-like
		".zq-engagement-redwood",             // hidden + codename-like
		"sprocket-spawn-ab12cd",              // harness <name>-<random>
	}
	known := []string{".com.openai.codex.abc123", "opencode", "tsx-1000"}
	for _, name := range append(append([]string{}, sensitive...), known...) {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeTempEntries(entries, func(os.DirEntry) int64 { return 1 })

	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, name := range sensitive {
		if strings.Contains(out, name) {
			t.Fatalf("raw name %q leaked into the summary JSON: %s", name, out)
		}
		// Also no distinctive fragment of it.
		for _, frag := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '.' }) {
			if len(frag) >= 5 && strings.Contains(out, frag) {
				t.Fatalf("fragment %q of %q leaked into the summary JSON: %s", frag, name, out)
			}
		}
	}

	if summary.EntriesTotal != int64(len(sensitive)+len(known)) {
		t.Errorf("entries_total = %d, want %d", summary.EntriesTotal, len(sensitive)+len(known))
	}
	if summary.HiddenEntries != 2 {
		t.Errorf("hidden_entries = %d, want 2 (one codex, one sensitive)", summary.HiddenEntries)
	}
	labels := map[string]int64{}
	for _, f := range summary.Families {
		labels[f.Family] = f.Entries
	}
	if labels[TempFamilyOther] != int64(len(sensitive)) {
		t.Errorf("other = %d, want %d: %+v", labels[TempFamilyOther], len(sensitive), summary.Families)
	}
	for label := range labels {
		switch label {
		case TempFamilyCodex, TempFamilyOpencode, TempFamilyBunx, TempFamilyTsx, TempFamilyOther:
		default:
			t.Fatalf("label %q is outside the closed table", label)
		}
	}
}

func TestTempPlane_StillHasNoChildFields(t *testing.T) {
	raw, err := json.Marshal(TempPlane{Root: "/tmp", Kind: "tmp", SizeStatus: TempPlaneSizeMeasured})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		switch key {
		case "root", "kind", "size_status", "size_bytes", "size_basis", "follow_up":
		default:
			t.Fatalf("TempPlane gained field %q; child data belongs in the family summary", key)
		}
	}
}

func TestTempResidueSchema_RejectsLabelsOutsideTheTable(t *testing.T) {
	report := validMinimalReport(t)
	n := int64(5)
	report.TempPlaneCoverage = &TempPlaneCoverage{Trigger: PressureWarn, Planes: []TempPlane{{
		Root: "/private/tmp", Kind: TempPlaneKindShared, SizeStatus: TempPlaneSizeMeasured,
		SizeBytes: &n, SizeBasis: "apparent", FollowUp: "spanwit space /private/tmp",
	}}, Residue: &TempResidueSummary{EntriesTotal: 1, HiddenEntries: 0, Families: []TempFamily{{
		Family: TempFamilyOther, Entries: 1, Bytes: 1, Oldest: time.Unix(1, 0).UTC(), Newest: time.Unix(1, 0).UTC(),
	}}}}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, mustJSON(t, report)); err != nil {
		t.Fatalf("a closed-table label must validate: %v", err)
	}
	report.TempPlaneCoverage.Residue.Families[0].Family = "projectkestrel"
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, mustJSON(t, report)); err == nil {
		t.Fatal("schema must reject a family label outside the closed table")
	}
}
