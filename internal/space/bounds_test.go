package space

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/contract"
)

// byteFigureBoundExceptions lists byte figures that carry their bound inside
// the referenced object rather than in a sibling property. Each needs a reason.
var byteFigureBoundExceptions = map[string]string{
	"/$defs/snapshot_entry/properties/bytes":           "byte_claim: the claim object carries its own bound",
	"/$defs/snapshots_plane/properties/bytes":          "byte_claim: the claim object carries its own bound",
	"/$defs/held_open_object/properties/logical_bytes": "held_open_byte_claim: bound is const lower inside the claim",
	"/$defs/held_open_group/properties/logical_bytes":  "held_open_byte_claim: bound is const lower inside the claim",
	"/$defs/held_open_plane/properties/logical_bytes":  "held_open_byte_claim: bound is const lower inside the claim",
}

func loadSchemaDoc(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// walkByteFigures reports every property whose name contains "bytes" and
// whose object has no sibling property whose name contains "bound".
func walkByteFigures(node any, path string, found map[string]bool, bare func(string)) {
	switch n := node.(type) {
	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok {
			hasBound := false
			for k := range props {
				if strings.Contains(k, "bound") {
					hasBound = true
				}
			}
			for k, v := range props {
				if !strings.Contains(k, "bytes") {
					continue
				}
				spec, _ := v.(map[string]any)
				_, isRef := spec["$ref"]
				_, isAllOf := spec["allOf"]
				if spec["type"] != "integer" && !isRef && !isAllOf {
					continue
				}
				p := path + "/properties/" + k
				found[p] = true
				if !hasBound {
					bare(p)
				}
			}
		}
		for k, v := range n {
			walkByteFigures(v, path+"/"+k, found, bare)
		}
	case []any:
		for i, v := range n {
			walkByteFigures(v, path+"/"+strconv.Itoa(i), found, bare)
		}
	}
}

func TestSpaceReportSchema_EveryByteFigureHasABound(t *testing.T) {
	for name, raw := range map[string][]byte{
		"v1": spanwitschema.SpanwitSpaceReportV1,
		"v2": spanwitschema.SpanwitSpaceReportV2,
	} {
		found := map[string]bool{}
		var bare []string
		walkByteFigures(loadSchemaDoc(t, raw), "", found, func(p string) {
			if _, ok := byteFigureBoundExceptions[p]; !ok {
				bare = append(bare, p)
			}
		})
		sort.Strings(bare)
		if len(bare) > 0 {
			t.Errorf("%s: byte figures without a bound (add a sibling *bound property or an exception with a reason):\n  %s",
				name, strings.Join(bare, "\n  "))
		}
		if len(found) < 20 {
			t.Errorf("%s: walk found only %d byte figures; the walker is broken", name, len(found))
		}
		if name == "v2" {
			for p := range byteFigureBoundExceptions {
				if !found[p] {
					t.Errorf("stale exception %s: no such byte figure in v2", p)
				}
			}
		}
	}
}

// v2FullDoc splices the report sections of a marshaled v1 report into the v2
// full golden fixture, so the shared $defs are checked under both schemas.
func v2FullDoc(t *testing.T, v1 map[string]any) map[string]any {
	t.Helper()
	doc := loadSchemaDoc(t, readSchemaFixture(t, "space-report-v2-full.json"))
	for _, k := range []string{"verified_reclaimable", "hotspots", "unverified", "unknown", "prune_handoff", "temp_plane_coverage", "container_runtime"} {
		if v, ok := v1[k]; ok {
			doc[k] = v
		}
	}
	return doc
}

func validateDocBoth(t *testing.T, v1 map[string]any) (error, error) {
	t.Helper()
	raw1, err := json.Marshal(v1)
	if err != nil {
		t.Fatal(err)
	}
	raw2, err := json.Marshal(v2FullDoc(t, v1))
	if err != nil {
		t.Fatal(err)
	}
	return contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw1),
		contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV2, raw2)
}

func reportDoc(t *testing.T, r Report) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(mustJSON(t, r), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func setIncomplete(r *Report, incomplete bool) {
	for _, list := range [][]Entry{r.Verified.Candidates, r.Hotspots, r.Unverified.Entries, r.Unknown} {
		for i := range list {
			list[i].SizeIncomplete = incomplete
		}
	}
	r.Verified.SizeIncomplete = incomplete
	r.Verified.PrunableSizeIncomplete = incomplete
	r.Verified.WithheldSizeIncomplete = false
	r.Unverified.SizeIncomplete = incomplete
	if h := r.PruneHandoff; h != nil {
		h.SizeIncomplete = incomplete
		for i := range h.Candidates {
			h.Candidates[i].SizeIncomplete = incomplete
		}
		if h.ExactPlan != nil {
			for i := range h.ExactPlan.Candidates {
				h.ExactPlan.Candidates[i].SizeIncomplete = incomplete
			}
		}
	}
}

func TestSizeBound_AgreesWithSizeIncomplete(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		report := validMinimalReport(t)
		if report.PruneHandoff == nil || len(report.PruneHandoff.Candidates) == 0 || report.PruneHandoff.ExactPlan == nil {
			t.Fatal("fixture needs a prune handoff with an exact plan")
		}
		setIncomplete(&report, incomplete)
		want := BoundExact
		if incomplete {
			want = BoundLower
		}
		doc := reportDoc(t, report)

		var rows []map[string]any
		collect := func(v any) {
			for _, e := range v.([]any) {
				rows = append(rows, e.(map[string]any))
			}
		}
		vr := doc["verified_reclaimable"].(map[string]any)
		collect(vr["candidates"])
		collect(doc["hotspots"])
		collect(doc["unknown"])
		un := doc["unverified"].(map[string]any)
		collect(un["entries"])
		ph := doc["prune_handoff"].(map[string]any)
		collect(ph["candidates"])
		collect(ph["exact_plan"].(map[string]any)["candidates"])
		rows = append(rows, un, ph)
		for _, row := range rows {
			if row["size_bound"] != want {
				t.Fatalf("incomplete=%v: size_bound=%v want %s in %v", incomplete, row["size_bound"], want, row)
			}
		}
		if vr["prunable_bound"] != want || vr["withheld_bound"] != BoundExact {
			t.Fatalf("incomplete=%v: verified bounds prunable=%v withheld=%v", incomplete, vr["prunable_bound"], vr["withheld_bound"])
		}
		if v1, v2 := validateDocBoth(t, doc); v1 != nil || v2 != nil {
			t.Fatalf("incomplete=%v: emitted bounds must validate: v1=%v v2=%v", incomplete, v1, v2)
		}
	}
}

func TestSizeBound_SchemaRejectsDisagreement(t *testing.T) {
	report := validMinimalReport(t)
	setIncomplete(&report, true)
	cases := map[string]func(doc map[string]any){
		"verified entry exact while incomplete": func(doc map[string]any) {
			vr := doc["verified_reclaimable"].(map[string]any)
			vr["candidates"].([]any)[0].(map[string]any)["size_bound"] = BoundExact
		},
		"hotspot lower while complete": func(doc map[string]any) {
			h := doc["hotspots"].([]any)[0].(map[string]any)
			delete(h, "size_incomplete")
			h["size_bound"] = BoundLower
		},
		"unverified section exact while incomplete": func(doc map[string]any) {
			doc["unverified"].(map[string]any)["size_bound"] = BoundExact
		},
		"handoff exact while incomplete": func(doc map[string]any) {
			doc["prune_handoff"].(map[string]any)["size_bound"] = BoundExact
		},
		"exact candidate exact while incomplete": func(doc map[string]any) {
			ep := doc["prune_handoff"].(map[string]any)["exact_plan"].(map[string]any)
			ep["candidates"].([]any)[0].(map[string]any)["size_bound"] = BoundExact
		},
		"verified section both exact while incomplete": func(doc map[string]any) {
			vr := doc["verified_reclaimable"].(map[string]any)
			vr["prunable_bound"], vr["withheld_bound"] = BoundExact, BoundExact
		},
		"verified section lower while complete": func(doc map[string]any) {
			vr := doc["verified_reclaimable"].(map[string]any)
			vr["size_incomplete"] = false
			for _, c := range vr["candidates"].([]any) {
				delete(c.(map[string]any), "size_incomplete")
				c.(map[string]any)["size_bound"] = BoundExact
			}
		},
		"pressure lower": func(doc map[string]any) {
			doc["pressure"].(map[string]any)["bytes_bound"] = BoundLower
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := reportDoc(t, report)
			mutate(doc)
			v1, v2 := validateDocBoth(t, doc)
			if v1 == nil {
				t.Error("v1 schema accepted a disagreeing bound")
			}
			if v2 == nil && name != "pressure lower" {
				t.Error("v2 schema accepted a disagreeing bound")
			}
		})
	}
	// pressure is checked under v2 directly (v2FullDoc keeps the fixture pressure).
	v2 := loadSchemaDoc(t, readSchemaFixture(t, "space-report-v2-full.json"))
	v2["pressure"].(map[string]any)["bytes_bound"] = BoundLower
	raw, _ := json.Marshal(v2)
	if contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV2, raw) == nil {
		t.Error("v2 schema accepted pressure bytes_bound lower")
	}
}

func TestTempBounds_FollowStatusAndSchema(t *testing.T) {
	report := validMinimalReport(t)
	n := int64(5)
	report.TempPlaneCoverage = &TempPlaneCoverage{Trigger: PressureCritical, Planes: []TempPlane{
		{Root: "/a", Kind: TempPlaneKindShared, SizeStatus: TempPlaneSizeMeasured, SizeBytes: &n, SizeBasis: "apparent", FollowUp: "spanwit space /a"},
		{Root: "/b", Kind: TempPlaneKindShared, SizeStatus: TempPlaneSizePartial, SizeBytes: &n, SizeBasis: "apparent", FollowUp: "spanwit space /b"},
		{Root: "/c", Kind: TempPlaneKindShared, SizeStatus: TempPlaneSizeUnavailable, SizeBound: BoundExact, SizeBasis: "apparent", FollowUp: "spanwit space /c"},
	}, Residue: &TempResidueSummary{EntriesTotal: 1, Families: []TempFamily{{Family: TempFamilyOther, Entries: 1, Bytes: 9}}}}
	doc := reportDoc(t, report)
	tpc := doc["temp_plane_coverage"].(map[string]any)
	planes := tpc["planes"].([]any)
	for i, want := range []any{BoundExact, BoundLower, nil} {
		if got := planes[i].(map[string]any)["size_bound"]; got != want {
			t.Fatalf("plane %d size_bound=%v want %v", i, got, want)
		}
	}
	fam := tpc["residue"].(map[string]any)["families"].([]any)[0].(map[string]any)
	if fam["bytes_bound"] != BoundExact {
		t.Fatalf("family bytes_bound=%v", fam["bytes_bound"])
	}
	if v1, v2 := validateDocBoth(t, doc); v1 != nil || v2 != nil {
		t.Fatalf("temp bounds must validate: v1=%v v2=%v", v1, v2)
	}
	for name, mutate := range map[string]func(){
		"bound on unavailable": func() { planes[2].(map[string]any)["size_bound"] = BoundLower },
		"exact on partial":     func() { planes[1].(map[string]any)["size_bound"] = BoundExact },
		"lower on measured":    func() { planes[0].(map[string]any)["size_bound"] = BoundLower },
	} {
		doc = reportDoc(t, report)
		tpc = doc["temp_plane_coverage"].(map[string]any)
		planes = tpc["planes"].([]any)
		mutate()
		if v1, v2 := validateDocBoth(t, doc); v1 == nil || v2 == nil {
			t.Errorf("%s: schema must reject (v1=%v v2=%v)", name, v1, v2)
		}
	}
}

func TestContainerBounds_ExactWhenMeasuredAbsentOtherwise(t *testing.T) {
	rt := colimaRuntime()
	rt.RefDisclosure = RefDisclosureLocal
	rt.ImageRefs = []RuntimeImageRef{{Ref: "a:b", Bucket: BucketDanglingImages, UniqueBytes: 1}}
	rt.Buckets = append(rt.Buckets, RuntimeBucket{Kind: BucketDanglingImages, Status: ProbeUnavailable, Reason: ReasonParseFailed, Bound: BoundExact})
	doc := runtimeDoc(t, rt)
	sec := section(doc)
	if sec["guest_filesystem"].(map[string]any)["bound"] != BoundExact {
		t.Fatalf("measured guest needs bound exact: %v", sec["guest_filesystem"])
	}
	buckets := sec["buckets"].([]any)
	for _, b := range buckets {
		m := b.(map[string]any)
		switch m["status"] {
		case ProbeMeasured:
			if m["bound"] != BoundExact {
				t.Fatalf("measured bucket needs bound exact: %v", m)
			}
		default:
			if _, ok := m["bound"]; ok {
				t.Fatalf("unavailable bucket must not carry a bound: %v", m)
			}
		}
	}
	if sec["image_refs"].([]any)[0].(map[string]any)["bound"] != BoundExact {
		t.Fatalf("image ref needs bound exact")
	}
	if v1, v2 := validateBoth(t, doc); v1 != nil || v2 != nil {
		t.Fatalf("v1=%v v2=%v", v1, v2)
	}

	// Schema rejects a bound on an unavailable bucket and on an unmeasured guest.
	last := buckets[len(buckets)-1].(map[string]any)
	last["bound"] = BoundExact
	if v1, v2 := validateBoth(t, doc); v1 == nil || v2 == nil {
		t.Errorf("schema must reject bound on an unavailable bucket (v1=%v v2=%v)", v1, v2)
	}
	delete(last, "bound")

	unmeasured := colimaRuntime()
	unmeasured.GuestFilesystem = guestUnavailable(ProbeNotProbed, ReasonProbeNotRequested)
	doc = runtimeDoc(t, unmeasured)
	g := section(doc)["guest_filesystem"].(map[string]any)
	if _, ok := g["bound"]; ok {
		t.Fatalf("not-probed guest must not carry a bound: %v", g)
	}
	g["bound"] = BoundExact
	if v1, v2 := validateBoth(t, doc); v1 == nil || v2 == nil {
		t.Errorf("schema must reject bound on a not-probed guest (v1=%v v2=%v)", v1, v2)
	}
}

// boundKeys are the keys this change added. "bound" is only stripped where it
// is new (container guest/buckets/image refs), never from byte claims or
// recipe estimates that already carried one.
var boundKeys = map[string]bool{"size_bound": true, "prunable_bound": true, "withheld_bound": true, "bytes_bound": true}

func stripNewBounds(node any) {
	switch n := node.(type) {
	case map[string]any:
		for k := range boundKeys {
			delete(n, k)
		}
		if cr, ok := n["container_runtime"].(map[string]any); ok {
			delete(cr["guest_filesystem"].(map[string]any), "bound")
			for _, key := range []string{"buckets", "image_refs"} {
				if list, ok := cr[key].([]any); ok {
					for _, b := range list {
						delete(b.(map[string]any), "bound")
					}
				}
			}
		}
		for _, v := range n {
			stripNewBounds(v)
		}
	case []any:
		for _, v := range n {
			stripNewBounds(v)
		}
	}
}

// Reports written before bounds existed (v0.2.1 shape) must still load, and
// re-emit with every bound stated.
func TestBounds_PreBoundReportsStillLoad(t *testing.T) {
	legacyV2 := readSchemaFixture(t, "space-report-v2-full-v0.2.1.json")
	if strings.Contains(string(legacyV2), "_bound") {
		t.Fatal("legacy fixture must not carry bound fields")
	}
	if _, err := LoadSpaceReportJSON(legacyV2); err != nil {
		t.Fatalf("v0.2.1 v2 report: %v", err)
	}

	report := validMinimalReport(t)
	setIncomplete(&report, true)
	report.ContainerRuntime = colimaRuntime()
	doc := reportDoc(t, report)
	stripNewBounds(doc)
	raw, _ := json.Marshal(doc)
	for k := range boundKeys {
		if strings.Contains(string(raw), `"`+k+`"`) {
			t.Fatalf("strip left %s", k)
		}
	}
	loaded, err := LoadSpaceReportJSON(raw)
	if err != nil {
		t.Fatalf("pre-bound v1 report: %v", err)
	}
	// Re-emitted, the loaded report states every bound; the verified split is
	// unknown after load, so both subtotals are stated as lower bounds.
	re := reportDoc(t, loaded)
	vr := re["verified_reclaimable"].(map[string]any)
	if vr["prunable_bound"] != BoundLower || vr["withheld_bound"] != BoundLower {
		t.Fatalf("re-emitted verified bounds: %v / %v", vr["prunable_bound"], vr["withheld_bound"])
	}
	requireEveryByteFigureBounded(t, re)
}

// requireEveryByteFigureBounded is the emit-side half of the optional-in-schema
// choice: every emitted object with a numeric *bytes* figure states a bound.
func requireEveryByteFigureBounded(t *testing.T, doc map[string]any) {
	t.Helper()
	var walk func(node any, path string)
	walk = func(node any, path string) {
		switch n := node.(type) {
		case map[string]any:
			hasBound := false
			for k := range n {
				if strings.Contains(k, "bound") {
					hasBound = true
				}
			}
			for k, v := range n {
				if _, num := v.(float64); num && strings.Contains(k, "bytes") && !hasBound {
					t.Errorf("emitted %s/%s has no bound", path, k)
				}
				walk(v, path+"/"+k)
			}
		case []any:
			for i, v := range n {
				walk(v, path+"/"+strconv.Itoa(i))
			}
		}
	}
	walk(doc, "")
}

func TestBounds_EmittedReportsCarryEveryBound(t *testing.T) {
	report := validMinimalReport(t)
	n := int64(7)
	report.TempPlaneCoverage = &TempPlaneCoverage{Trigger: PressureWarn, Planes: []TempPlane{
		{Root: "/a", Kind: TempPlaneKindShared, SizeStatus: TempPlaneSizePartial, SizeBytes: &n, SizeBasis: "apparent", FollowUp: "spanwit space /a"},
	}, Residue: &TempResidueSummary{EntriesTotal: 1, Families: []TempFamily{{Family: TempFamilyBunx, Entries: 1, Bytes: 3}}}}
	rt := colimaRuntime()
	rt.RefDisclosure = RefDisclosureLocal
	rt.ImageRefs = []RuntimeImageRef{{Ref: "a:b", Bucket: BucketDanglingImages, UniqueBytes: 1}}
	report.ContainerRuntime = rt
	analysis := report.Pressure
	report.AnalysisPressure = &analysis
	doc := reportDoc(t, report)
	requireEveryByteFigureBounded(t, doc)
	if v1, v2 := validateDocBoth(t, doc); v1 != nil || v2 != nil {
		t.Fatalf("v1=%v v2=%v", v1, v2)
	}

	// The checked-in golden fixtures represent current output.
	for _, name := range []string{"space-report-v2-full.json", "space-report-v2-capacity-only.json"} {
		requireEveryByteFigureBounded(t, loadSchemaDoc(t, readSchemaFixture(t, name)))
	}
}

func TestBoundFor(t *testing.T) {
	if BoundFor(false) != BoundExact || BoundFor(true) != BoundLower {
		t.Fatal("BoundFor must map complete→exact, incomplete→lower")
	}
}

func TestCollectContainerRuntime_BoundsOnMeasuredFigures(t *testing.T) {
	eng := fakeColimaEngine()
	rt := CollectContainerRuntime(context.Background(), ContainerRuntimeOptions{
		ProbeVM: true, Env: colimaEnv(t), RefDisclosure: RefDisclosureLocal,
		API: func(string) EngineAPI { return eng },
		Runner: capacity.RunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
			return []byte(fakeDFOutput), nil
		}),
	})
	sec := section(runtimeDoc(t, rt))
	if sec["guest_filesystem"].(map[string]any)["bound"] != BoundExact {
		t.Fatalf("measured guest: %v", sec["guest_filesystem"])
	}
	for _, b := range sec["buckets"].([]any) {
		if m := b.(map[string]any); m["status"] == ProbeMeasured && m["bound"] != BoundExact {
			t.Fatalf("measured bucket without bound exact: %v", m)
		}
	}
	refs := sec["image_refs"].([]any)
	if len(refs) == 0 {
		t.Fatal("fixture needs image refs")
	}
	for _, r := range refs {
		if r.(map[string]any)["bound"] != BoundExact {
			t.Fatalf("image ref without bound exact: %v", r)
		}
	}
	requireRuntimeSchemaValid(t, rt)

	fallback := section(runtimeDoc(t, ContainerRuntimeFallback(RefDisclosureAggregate)))
	if _, ok := fallback["guest_filesystem"].(map[string]any)["bound"]; ok {
		t.Fatalf("unavailable guest must not carry a bound: %v", fallback)
	}
}

// errDirEntry is a directory entry whose Info call fails (for example the
// entry vanished or is unreadable between listing and stat).
type errDirEntry struct{ name string }

func (e errDirEntry) Name() string               { return e.name }
func (e errDirEntry) IsDir() bool                { return false }
func (e errDirEntry) Type() os.FileMode          { return 0 }
func (e errDirEntry) Info() (os.FileInfo, error) { return nil, os.ErrPermission }

// A family whose entries could not all be stat'd states a lower bound: the
// skipped entry is missing from the sum, so the bytes are a minimum.
func TestTempFamilyBound_LowerWhenAnEntryIsSkipped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tsx-1234"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	entries := append(listed, errDirEntry{name: "tsx-5678"}, errDirEntry{name: ".com.openai.codex.session1"})
	summary := summarizeTempEntries(entries, func(e os.DirEntry) int64 {
		info, err := e.Info()
		if err != nil {
			return 0
		}
		return info.Size()
	})
	bounds := map[string]string{}
	for _, f := range summary.Families {
		raw, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Family     string `json:"family"`
			BytesBound string `json:"bytes_bound"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		bounds[out.Family] = out.BytesBound
	}
	if bounds[TempFamilyTsx] != BoundLower {
		t.Fatalf("tsx had a skipped entry and must be lower, got %q (%v)", bounds[TempFamilyTsx], bounds)
	}
	// A family with only skipped entries has no row; the skips are counted at
	// summary level instead of as a zero-entry row with fabricated dates.
	if _, ok := bounds[TempFamilyCodex]; ok {
		t.Fatalf("a family with no measured entry must have no row, got %v", bounds)
	}
	if summary.SkippedEntries != 2 {
		t.Fatalf("skipped entries = %d, want 2", summary.SkippedEntries)
	}
	for _, f := range summary.Families {
		if f.Entries < 1 || f.Oldest.IsZero() {
			t.Fatalf("every row must be an observed entry with real dates: %+v", f)
		}
	}
	clean := summarizeTempEntries(listed, func(e os.DirEntry) int64 { info, _ := e.Info(); return info.Size() })
	for _, f := range clean.Families {
		raw, _ := json.Marshal(f)
		if !strings.Contains(string(raw), `"bytes_bound":"exact"`) {
			t.Fatalf("fully measured family must be exact: %s", raw)
		}
	}
}

// A saved family that said lower must still say lower after load and
// re-emit: the in-memory flag is lost on unmarshal, the loaded bound is not.
func TestTempFamilyBound_RoundTripKeepsLower(t *testing.T) {
	for _, in := range []string{
		`{"family":"tsx","entries":1,"bytes":3,"bytes_bound":"lower","oldest":"2026-10-13T00:00:00Z","newest":"2026-10-13T00:00:00Z"}`,
		`{"family":"tsx","entries":1,"bytes":3,"bytes_bound":"exact","oldest":"2026-10-13T00:00:00Z","newest":"2026-10-13T00:00:00Z"}`,
	} {
		var f TempFamily
		if err := json.Unmarshal([]byte(in), &f); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		wantLower := strings.Contains(in, `"bytes_bound":"lower"`)
		gotLower := strings.Contains(string(out), `"bytes_bound":"lower"`)
		if wantLower != gotLower {
			t.Fatalf("round trip changed the bound:\n in  %s\n out %s", in, out)
		}
	}
}

// Loading an emitted report and emitting it again never changes any bound in
// the space-owned sections, so derived bounds that rest on unexported state
// cannot drift on round trip.
func TestBounds_FullReportRoundTripIsStable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "schema-validation", "space-report-v2-full.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	once, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var r2 Report
	if err := json.Unmarshal(once, &r2); err != nil {
		t.Fatal(err)
	}
	twice, err := json.Marshal(r2)
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Fatalf("report changed across a second load and emit")
	}
	bounds := func(b []byte) []string {
		var out []string
		var walk func(any)
		walk = func(n any) {
			switch v := n.(type) {
			case map[string]any:
				for k, x := range v {
					if k == "capacity_accounting" {
						// Not carried by Report; its byte_claim bounds are owned
						// by the capacity types and round-trip there.
						continue
					}
					if strings.Contains(k, "bound") {
						if s, ok := x.(string); ok {
							out = append(out, k+"="+s)
						}
					}
					walk(x)
				}
			case []any:
				for _, x := range v {
					walk(x)
				}
			}
		}
		var doc any
		_ = json.Unmarshal(b, &doc)
		walk(doc)
		sort.Strings(out)
		return out
	}
	in, out := bounds(raw), bounds(once)
	if strings.Join(in, ",") != strings.Join(out, ",") {
		t.Fatalf("bounds changed on load and emit:\n in  %v\n out %v", in, out)
	}
}

// The same round trip over a report whose figures are lower bounds wherever
// spanwit can state one: no lower bound may come back as exact.
func TestBounds_LowerBoundsSurviveRoundTrip(t *testing.T) {
	r := validMinimalReport(t)
	inc := Entry{Path: "/x/a", SizeBytes: 10, SizeHuman: "10B", State: StatePrunable, SizeIncomplete: true,
		Signature: "development.rust.cargo-target", RebuildExpectation: RebuildHigh,
		EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none"}
	r.Verified.Candidates = []Entry{inc}
	AnnotateVerifiedBoundState(&r.Verified)
	r.Unverified.SizeIncomplete = true
	sz := int64(5)
	r.TempPlaneCoverage = &TempPlaneCoverage{Trigger: PressureWarn, Planes: []TempPlane{{
		Root: "/private/tmp", Kind: TempPlaneKindShared, SizeStatus: TempPlaneSizePartial,
		SizeBytes: &sz, SizeBasis: "apparent", FollowUp: "spanwit space /private/tmp",
	}}, Residue: &TempResidueSummary{EntriesTotal: 1, Families: []TempFamily{{
		Family: TempFamilyOther, Entries: 1, Bytes: 1, BytesIncomplete: true,
		Oldest: time.Unix(1, 0).UTC(), Newest: time.Unix(1, 0).UTC(),
	}}}}

	first, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Report
	if err := json.Unmarshal(first, &loaded); err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	countLower := func(b []byte) int { return strings.Count(string(b), `_bound":"lower"`) }
	if countLower(first) == 0 {
		t.Fatal("fixture should carry lower bounds")
	}
	if countLower(first) != countLower(second) {
		t.Fatalf("lower bounds changed on round trip: %d before, %d after\nbefore %s\nafter  %s",
			countLower(first), countLower(second), first, second)
	}
}

// Each side of the verified section keeps its own loaded bound: one case per
// side, so reverting either side's fix fails its own case.
func TestBounds_VerifiedPerSideBoundsSurviveRoundTrip(t *testing.T) {
	cases := []struct {
		name               string
		state              string
		wantPrunable, want string
	}{
		{"prunable lower, withheld exact", StatePrunable, BoundLower, BoundExact},
		{"withheld lower, prunable exact", StateWithheld, BoundExact, BoundLower},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := VerifiedSection{Candidates: []Entry{{
				Path: "/x/a", SizeBytes: 10, SizeHuman: "10B", State: c.state, SizeIncomplete: true,
				Signature: "development.rust.cargo-target", RebuildExpectation: RebuildHigh,
				EffectiveMinSize: "none", EffectiveMinAge: "none", EffectiveMaxAge: "none",
			}}}
			AnnotateVerifiedBoundState(&v)
			first, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var loaded VerifiedSection
			if err := json.Unmarshal(first, &loaded); err != nil {
				t.Fatal(err)
			}
			second, err := json.Marshal(loaded)
			if err != nil {
				t.Fatal(err)
			}
			for _, doc := range [][]byte{first, second} {
				var out struct {
					Prunable string `json:"prunable_bound"`
					Withheld string `json:"withheld_bound"`
				}
				if err := json.Unmarshal(doc, &out); err != nil {
					t.Fatal(err)
				}
				if out.Prunable != c.wantPrunable || out.Withheld != c.want {
					t.Fatalf("bounds prunable=%s withheld=%s, want %s/%s\n%s", out.Prunable, out.Withheld, c.wantPrunable, c.want, doc)
				}
			}
		})
	}
}

// An empty listable temp root, or one whose entries are all unstat-able,
// gives "families":[] (required array), never null, so the summary validates
// and a pressured report still emits.
func TestTempResidue_EmptyFamiliesIsArrayNotNull(t *testing.T) {
	empty := summarizeTempPlanes([]string{t.TempDir()})
	if empty == nil {
		t.Fatal("a listable empty root should yield a summary")
	}
	allSkipped := summarizeTempEntries([]os.DirEntry{errDirEntry{name: "tsx-1"}, errDirEntry{name: "x"}},
		func(os.DirEntry) int64 { return 0 })

	var v1 map[string]any
	if err := json.Unmarshal(spanwitschema.SpanwitSpaceReportV1, &v1); err != nil {
		t.Fatal(err)
	}
	probe, err := json.Marshal(map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$defs":   v1["$defs"],
		"$ref":    "#/$defs/temp_residue_summary",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]TempResidueSummary{"empty root": *empty, "all skipped": allSkipped} {
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"families":[]`) {
			t.Fatalf("%s: families must be [] not null: %s", name, raw)
		}
		if err := contract.ValidateJSON(probe, raw); err != nil {
			t.Fatalf("%s: summary must validate: %v\n%s", name, err, raw)
		}
	}

	// Full report path: an empty temp plane under pressure still emits a
	// schema-valid report.
	r := validMinimalReport(t)
	sz := int64(0)
	r.TempPlaneCoverage = &TempPlaneCoverage{Trigger: PressureWarn, Planes: []TempPlane{{
		Root: "/private/tmp", Kind: TempPlaneKindShared, SizeStatus: TempPlaneSizeMeasured,
		SizeBytes: &sz, SizeBasis: "apparent", FollowUp: "spanwit space /private/tmp",
	}}, Residue: empty}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, mustJSON(t, r)); err != nil {
		t.Fatalf("a report with an empty temp root must validate: %v", err)
	}
}
