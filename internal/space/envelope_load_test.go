package space

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
	"github.com/3leaps/spanwit/internal/envelope"
)

// A v0.2.1 report has no tool, completion or disclosure. It loads with tool
// unknown and the most disclosed level for its kind, never a narrower guess.
func TestEnvelope_LegacyReportLoadsMostDisclosed(t *testing.T) {
	raw := readSchemaFixture(t, "space-report-v2-full-v0.2.1.json")
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	// capacity_accounting.capture.tool is the older nested copy; only the
	// top-level envelope fields must be absent.
	for _, k := range []string{"tool", "disclosure", "completion"} {
		if _, ok := top[k]; ok {
			t.Fatalf("legacy fixture must not carry top-level %s", k)
		}
	}
	r, err := LoadSpaceReportJSON(raw)
	if err != nil {
		t.Fatalf("v0.2.1 report must load: %v", err)
	}
	if r.Tool == nil || *r.Tool != envelope.UnknownTool {
		t.Fatalf("legacy tool must load as unknown, got %+v", r.Tool)
	}
	if r.Disclosure == nil || *r.Disclosure != LegacySpaceDisclosure {
		t.Fatalf("legacy disclosure must be full/local/process_identity, got %+v", r.Disclosure)
	}
	if LegacySpaceDisclosure.Paths != envelope.PathsFull || LegacySpaceDisclosure.ImageRefs != envelope.ImageRefsLocal ||
		!LegacySpaceDisclosure.ProcessIdentity {
		t.Fatalf("the legacy level must be the widest: %+v", LegacySpaceDisclosure)
	}
}

// A stored summary narrower than the report's content fails to load.
func TestEnvelope_NarrowSummaryFailsLoad(t *testing.T) {
	raw := readSchemaFixture(t, "space-report-v2-full-v0.2.1.json")
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["disclosure"] = map[string]any{"paths": "none", "image_refs": "aggregate", "process_identity": false}
	narrow, _ := json.Marshal(doc)
	if _, err := LoadSpaceReportJSON(narrow); err == nil || !strings.Contains(err.Error(), "understates") {
		t.Fatalf("a narrower summary must fail to load, got %v", err)
	}
	doc["disclosure"] = map[string]any{"paths": "full", "image_refs": "local", "process_identity": true}
	wide, _ := json.Marshal(doc)
	if _, err := LoadSpaceReportJSON(wide); err != nil {
		t.Fatalf("a wider summary must load: %v", err)
	}
}

// An exact plan loads only when every candidate is inside its analysis_root.
func TestEnvelope_ExactPlanOutsideRootFails(t *testing.T) {
	ep := validExactPlanFixture(t, "/tmp/analysis/crate/target")
	raw, _ := json.Marshal(ep)
	if _, err := ParseAndValidateExactPlanJSON(raw); err != nil {
		t.Fatalf("an in-root plan must load: %v", err)
	}
	for _, p := range []string{"/elsewhere/target", ep.AnalysisRoot, filepath.Join(ep.AnalysisRoot, "..", "x", "target")} {
		bad := validExactPlanFixture(t, p)
		raw, _ := json.Marshal(bad)
		if _, err := ParseAndValidateExactPlanJSON(raw); err == nil {
			t.Errorf("candidate %s must fail: not strictly inside analysis_root %s", p, ep.AnalysisRoot)
		}
	}
}

// The standalone exact-prune-plan schema exists at the URL the plan's
// $schema claims, and validates an emitted plan.
func TestEnvelope_StandaloneExactPlanSchema(t *testing.T) {
	schema, err := os.ReadFile(filepath.Join("..", "..", "config", "schema", "spanwit-exact-prune-plan.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatal(err)
	}
	if s["$id"] != ExactPlanSchemaID {
		t.Fatalf("schema $id %v must equal ExactPlanSchemaID %s", s["$id"], ExactPlanSchemaID)
	}
	if !bytesEqual(schema, spanwitschema.SpanwitExactPrunePlanV1) {
		t.Fatal("embedded exact-plan schema drifted from the file")
	}
	raw, _ := json.Marshal(validExactPlanFixture(t, "/tmp/analysis/crate/target"))
	if err := contract.ValidateJSON(spanwitschema.SpanwitExactPrunePlanV1, raw); err != nil {
		t.Fatalf("an emitted exact plan must validate: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	doc["invented"] = true
	bad, _ := json.Marshal(doc)
	if err := contract.ValidateJSON(spanwitschema.SpanwitExactPrunePlanV1, bad); err == nil {
		t.Fatal("the standalone schema must reject unknown fields")
	}
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }
