package space

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

// colimaRuntime is a measured colima document shaped like the 2026-10-12
// dogfood: guest probe on, aggregate disclosure, cost-ordered recipe.
func colimaRuntime() *ContainerRuntime {
	return &ContainerRuntime{
		Engine: EngineColima,
		Detection: EngineDetection{
			Status: DetectionIdentified,
			Basis:  []string{DetectionBasisEndpointPath, DetectionBasisEngineInfo},
		},
		EngineArch:    "arm64",
		APIVersion:    "1.53",
		RefDisclosure: RefDisclosureAggregate,
		GuestFilesystem: GuestFilesystem{
			Status: ProbeMeasured, Basis: GuestBasisGuestStatfs,
			TotalBytes: i64p(105_089_261_568), UsedBytes: i64p(90_194_313_216),
			AvailBytes: i64p(10_415_349_760), Level: PressureCritical,
		},
		Buckets: []RuntimeBucket{
			{Kind: BucketImages, Status: ProbeMeasured, Count: i64p(36), Bytes: i64p(83_700_000_000),
				UniqueBytes: i64p(60_100_000_000), SharedBytes: i64p(23_600_000_000), InUse: i64p(0)},
			{Kind: BucketBuildCache, Status: ProbeMeasured, Count: i64p(151), Bytes: i64p(26_100_000_000),
				ReclaimableBytes: i64p(2_698_000_000)},
			{Kind: BucketForeignPlatform, Status: ProbeMeasured, Count: i64p(1), Bytes: i64p(1_630_340_394)},
			{Kind: BucketSupersededTags, Status: ProbeMeasured, Count: i64p(1), Bytes: i64p(6_671_000_000),
				UniqueBytes: i64p(6_671_000_000)},
			{Kind: BucketVolumesUnreferenced, Status: ProbeMeasured, Count: i64p(15), Bytes: i64p(3_636_000_000)},
		},
		Recipe: []RuntimeRecipeStep{
			{Step: StepBuildCachePrune, CostClass: CostRebuild,
				Estimate: RuntimeMeasure{Status: ProbeMeasured, Bytes: i64p(2_698_000_000)},
				Command:  []string{"docker", "builder", "prune"}},
			{Step: StepVolumePrune, CostClass: CostDataLossRisk,
				Estimate: RuntimeMeasure{Status: ProbeMeasured, Bytes: i64p(3_636_000_000)},
				Command:  []string{"docker", "volume", "prune"},
				Warning:  "Unreferenced volumes can still hold data (databases, caches)."},
			{Step: StepGuestTrim, CostClass: CostNone,
				Estimate: RuntimeMeasure{Status: ProbeNotProbed, Reason: ReasonProbeNotRequested},
				Command:  []string{"colima", "ssh", "--", "sudo", "fstrim", "-v", "<data-root>"}},
		},
	}
}

// runtimeDoc wraps a runtime section in a valid minimal v1 report and returns
// it as a generic map so tests can mutate fields the Go types cannot express.
func runtimeDoc(t *testing.T, rt *ContainerRuntime) map[string]any {
	t.Helper()
	report := validMinimalReport(t)
	report.ContainerRuntime = rt
	var doc map[string]any
	if err := json.Unmarshal(mustJSON(t, report), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// validateBoth checks the section under v1 (full report) and under v2 (the
// capacity-only golden fixture), since container_runtime is valid in both.
func validateBoth(t *testing.T, doc map[string]any) (v1, v2 error) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	v1 = contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV1, raw)

	capRaw, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "schema-validation", "space-report-v2-capacity-only.json"))
	if err != nil {
		t.Fatal(err)
	}
	var capDoc map[string]any
	if err := json.Unmarshal(capRaw, &capDoc); err != nil {
		t.Fatal(err)
	}
	capDoc["container_runtime"] = doc["container_runtime"]
	raw2, err := json.Marshal(capDoc)
	if err != nil {
		t.Fatal(err)
	}
	v2 = contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV2, raw2)
	return v1, v2
}

func section(doc map[string]any) map[string]any {
	return doc["container_runtime"].(map[string]any)
}

func TestContainerRuntimeSchema_AcceptsEngineClasses(t *testing.T) {
	native := colimaRuntime()
	native.Engine = EngineNative
	native.GuestFilesystem.Basis = GuestBasisHostStatfs
	native.Recipe = native.Recipe[:2]

	none := &ContainerRuntime{
		Engine:        EngineNone,
		Detection:     EngineDetection{Status: DetectionUnavailable, Basis: []string{}, Reason: ReasonEndpointUnresolved},
		RefDisclosure: RefDisclosureAggregate,
		GuestFilesystem: GuestFilesystem{Status: ProbeUnavailable, Basis: GuestBasisNone,
			Reason: ReasonEndpointUnresolved},
		Buckets: []RuntimeBucket{},
		Recipe:  []RuntimeRecipeStep{},
	}

	orbstack := colimaRuntime()
	orbstack.Engine = EngineOrbStack
	orbstack.GuestFilesystem = GuestFilesystem{Status: ProbeNotProbed, Basis: GuestBasisNone,
		Reason: ReasonUnsupportedEngine}

	notRequested := colimaRuntime()
	notRequested.GuestFilesystem = GuestFilesystem{Status: ProbeNotProbed, Basis: GuestBasisNone,
		Reason: ReasonProbeNotRequested}

	local := colimaRuntime()
	local.RefDisclosure = RefDisclosureLocal
	local.ImageRefs = []RuntimeImageRef{{Ref: "example.test/org/tool:v1", Bucket: BucketSupersededTags,
		UniqueBytes: 6_671_000_000}}

	for name, rt := range map[string]*ContainerRuntime{
		"colima measured": colimaRuntime(), "native host statfs": native, "no engine": none,
		"orbstack not probed": orbstack, "probe not requested": notRequested, "local refs": local,
	} {
		t.Run(name, func(t *testing.T) {
			v1, v2 := validateBoth(t, runtimeDoc(t, rt))
			if v1 != nil || v2 != nil {
				t.Fatalf("expected valid: v1=%v v2=%v", v1, v2)
			}
		})
	}
}

func TestContainerRuntimeSchema_RejectsContractViolations(t *testing.T) {
	cases := map[string]func(rt map[string]any){
		"unknown engine": func(rt map[string]any) { rt["engine"] = "lima-custom" },
		"unknown bucket kind": func(rt map[string]any) {
			rt["buckets"].([]any)[0].(map[string]any)["kind"] = "x-private-cache"
		},
		"unknown cost class": func(rt map[string]any) {
			rt["recipe"].([]any)[0].(map[string]any)["cost_class"] = "cheap"
		},
		"unknown recipe step": func(rt map[string]any) {
			rt["recipe"].([]any)[0].(map[string]any)["step"] = "system_prune_all"
		},
		"invented field": func(rt map[string]any) { rt["vm_profile"] = "default" },
		"invented bucket field": func(rt map[string]any) {
			rt["buckets"].([]any)[0].(map[string]any)["names"] = []any{"a"}
		},
		"refs under aggregate disclosure": func(rt map[string]any) {
			rt["image_refs"] = []any{map[string]any{"ref": "a:b", "bucket": "superseded_tags", "unique_bytes": 1}}
		},
		"ref bucket outside the table": func(rt map[string]any) {
			rt["ref_disclosure"] = "local"
			rt["image_refs"] = []any{map[string]any{"ref": "a:b", "bucket": "images", "unique_bytes": 1}}
		},
		"guest measured without bytes": func(rt map[string]any) {
			delete(rt["guest_filesystem"].(map[string]any), "used_bytes")
		},
		"guest not probed carrying bytes": func(rt map[string]any) {
			g := rt["guest_filesystem"].(map[string]any)
			g["status"], g["basis"], g["reason"] = "not_probed", "none", "probe_not_requested"
		},
		"guest unavailable without reason": func(rt map[string]any) {
			rt["guest_filesystem"] = map[string]any{"status": "unavailable", "basis": "none"}
		},
		"guest measured on unknown engine": func(rt map[string]any) { rt["engine"] = "unknown" },
		"host statfs on a VM engine": func(rt map[string]any) {
			rt["guest_filesystem"].(map[string]any)["basis"] = "host_statfs"
		},
		"guest statfs on native": func(rt map[string]any) { rt["engine"] = "native" },
		"bucket unavailable with zero bytes": func(rt map[string]any) {
			b := rt["buckets"].([]any)[0].(map[string]any)
			b["status"], b["reason"] = "unavailable", "engine_unreachable"
		},
		"estimate measured without bytes": func(rt map[string]any) {
			delete(rt["recipe"].([]any)[0].(map[string]any)["estimate"].(map[string]any), "bytes")
		},
		"x- prefixed reason": func(rt map[string]any) {
			rt["guest_filesystem"] = map[string]any{"status": "unavailable", "basis": "none", "reason": "x-vm-off"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := runtimeDoc(t, colimaRuntime())
			mutate(section(doc))
			v1, v2 := validateBoth(t, doc)
			if v1 == nil || v2 == nil {
				raw, _ := json.MarshalIndent(section(doc), "", " ")
				t.Fatalf("schema must reject %s (v1=%v v2=%v):\n%s", name, v1, v2, raw)
			}
		})
	}
}

// The v1 and v2 schemas must carry identical container_runtime definitions so
// a consumer sees one contract regardless of capture mode.
func TestContainerRuntimeSchema_DefinitionsIdenticalAcrossVersions(t *testing.T) {
	var v1, v2 struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(spanwitschema.SpanwitSpaceReportV1, &v1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(spanwitschema.SpanwitSpaceReportV2, &v2); err != nil {
		t.Fatal(err)
	}
	for _, def := range []string{"container_runtime", "container_runtime_bucket",
		"container_runtime_recipe_step", "container_runtime_image_ref"} {
		a, b := v1.Defs[def], v2.Defs[def]
		if len(a) == 0 || string(a) != string(b) {
			t.Fatalf("$defs/%s differs between v1 and v2 (or is missing)", def)
		}
	}
}

// ContainerRuntime must not grow fields that could carry identity (profile,
// socket path, VM name) without a schema change and a review of the leak rule.
func TestContainerRuntime_FieldAllowlist(t *testing.T) {
	rt := colimaRuntime()
	rt.RefDisclosure = RefDisclosureLocal
	rt.ImageRefs = []RuntimeImageRef{{Ref: "a:b", Bucket: BucketDanglingImages, UniqueBytes: 1}}
	var fields map[string]any
	if err := json.Unmarshal(mustJSON(t, rt), &fields); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"engine": true, "detection": true, "engine_arch": true, "api_version": true,
		"ref_disclosure": true, "guest_filesystem": true, "buckets": true, "recipe": true, "image_refs": true}
	for key := range fields {
		if !allowed[key] {
			t.Fatalf("ContainerRuntime gained field %q; identity-bearing data needs a leak review", key)
		}
	}
}
