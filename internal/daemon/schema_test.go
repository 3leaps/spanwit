//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

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
	for _, state := range []string{"awaiting_sample", "observing", "sample_unavailable", "journal_unavailable"} {
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
