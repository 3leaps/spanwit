//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

func TestResponseContracts(t *testing.T) {
	dir := privateDir(t)
	c, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTest(t, c)
	for _, state := range []string{"awaiting_sample", "observing", "sample_unavailable", "journal_unavailable", "journal_capacity_reached"} {
		if state == "observing" {
			if _, err := c.Sample(context.Background(), dir); err != nil {
				t.Fatal(err)
			}
		} else {
			c.setState(state)
		}
		for _, path := range []string{"/v1/status", "/v1/capabilities", "/v1/proposals", "/v999/status", "/v1/status?bad=1"} {
			w := httptest.NewRecorder()
			Handler(c).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if err := contract.ValidateJSON(spanwitschema.SpanwitDaemonResponseV1, w.Body.Bytes()); err != nil {
				t.Fatalf("%s %s: %v", state, path, err)
			}
		}
	}
	for _, bad := range []string{
		`{"protocol":1,"state":"observing"}`,
		`{"protocol":1,"state":"awaiting_sample","record_id":"a"}`,
		`{"protocol":2,"state":"awaiting_sample"}`,
		`{"protocol":1,"state":"not_evaluated","items":[{}],"next_cursor":null}`,
		`{"protocol":1,"state":"awaiting_sample","approve":true}`,
	} {
		if contract.ValidateJSON(spanwitschema.SpanwitDaemonResponseV1, []byte(bad)) == nil {
			t.Fatal("accepted invalid response", bad)
		}
	}
}

func TestAttributionRejectsContradictoryStates(t *testing.T) {
	for _, kind := range []string{"complete-active", "deferred-unknown", "partial-active", "false-complete", "true-deferred", "unbound-delta"} {
		t.Run(kind, func(t *testing.T) {
			n := int64(0)
			id := "a3df2fb7-6b94-4fef-8b7b-3b4283cc2393"
			a := Attribution{Schema: AttributionSchema, RecordID: id, CapacityRecordID: id, CapturedAt: time.Now().UTC(), Complete: true, Reasons: []DeepReason{DeepInitialBaseline}, Rows: []AttributionRow{{Observation: RootObservation{Path: "/root", DeviceID: "1", FileID: "2", Identity: id, Basis: AttributionBasis, Coverage: "complete", Activity: "unknown", CapturedAt: time.Now().UTC(), AllocatedBytes: &n}}}}
			validate := func() error {
				raw, err := json.Marshal(a)
				if err != nil {
					return err
				}
				return contract.ValidateJSON(spanwitschema.SpanwitRootAttributionV0, raw)
			}
			if err := validate(); err != nil {
				t.Fatal("invalid baseline fixture", err)
			}
			r := &a.Rows[0]
			switch kind {
			case "complete-active":
				r.Observation.Activity = "active"
			case "deferred-unknown":
				a.Complete = false
				r.Observation.Coverage = "deferred"
				r.Observation.AllocatedBytes = nil
			case "partial-active":
				a.Complete = false
				r.Observation.Coverage = "partial"
				r.Observation.Activity = "active"
				r.Observation.AllocatedBytes = nil
			case "false-complete":
				a.Complete = false
			case "true-deferred":
				r.Observation.Coverage = "deferred"
				r.Observation.Activity = "active"
				r.Observation.AllocatedBytes = nil
			case "unbound-delta":
				r.DeltaBytes = &n
			}
			if err := validate(); err == nil {
				t.Fatal("accepted contradictory state")
			}
		})
	}
}

func TestSampleContractParity(t *testing.T) {
	var growth, response map[string]any
	if err := json.Unmarshal(spanwitschema.SpanwitGrowthSampleV0, &growth); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(spanwitschema.SpanwitDaemonResponseV1, &response); err != nil {
		t.Fatal(err)
	}
	sample := growth["properties"].(map[string]any)["sample"]
	if !reflect.DeepEqual(sample, response["$defs"].(map[string]any)["sample"]) {
		t.Fatal("API and journal sample contracts diverged")
	}
}

func TestAttributionContractParity(t *testing.T) {
	var attribution, response map[string]any
	if err := json.Unmarshal(spanwitschema.SpanwitRootAttributionV0, &attribution); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(spanwitschema.SpanwitDaemonResponseV1, &response); err != nil {
		t.Fatal(err)
	}
	delete(attribution, "$schema")
	delete(attribution, "$id")
	delete(attribution, "title")
	if !reflect.DeepEqual(attribution, response["$defs"].(map[string]any)["attribution"]) {
		t.Fatal("attribution API/journal contract divergence")
	}
}
