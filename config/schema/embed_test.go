package schema_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/contract"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "schema-validation", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func fixtureObject(t *testing.T, name string) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(fixture(t, name), &value); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return value
}

func objectAt(t *testing.T, value map[string]any, path ...string) map[string]any {
	t.Helper()
	current := value
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("%v is not an object", path)
		}
		current = next
	}
	return current
}

func arrayAt(t *testing.T, value map[string]any, path ...string) []any {
	t.Helper()
	if len(path) == 0 {
		t.Fatal("arrayAt requires a path")
	}
	parent := objectAt(t, value, path[:len(path)-1]...)
	result, ok := parent[path[len(path)-1]].([]any)
	if !ok {
		t.Fatalf("%v is not an array", path)
	}
	return result
}

func marshalObject(t *testing.T, value map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func requireValid(t *testing.T, schema, value []byte) {
	t.Helper()
	if err := contract.ValidateJSON(schema, value); err != nil {
		t.Fatalf("expected valid document: %v\n%s", err, value)
	}
}

func requireInvalid(t *testing.T, schema []byte, value map[string]any) {
	t.Helper()
	if err := contract.ValidateJSON(schema, marshalObject(t, value)); err == nil {
		t.Fatalf("expected schema rejection:\n%s", marshalObject(t, value))
	}
}

func TestEmbeddedCapacitySchemasValidateGoldenFixtures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		schema  []byte
		fixture string
	}{
		{
			name:    "capacity-only report",
			schema:  spanwitschema.SpanwitSpaceReportV2,
			fixture: "space-report-v2-capacity-only.json",
		},
		{
			name:    "full report",
			schema:  spanwitschema.SpanwitSpaceReportV2,
			fixture: "space-report-v2-full.json",
		},
		{
			name:    "compare result",
			schema:  spanwitschema.SpanwitSpaceCompareV1,
			fixture: "space-compare-v1.json",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requireValid(t, tt.schema, fixture(t, tt.fixture))
		})
	}
}

func TestObserveSchemasValidateStrictContracts(t *testing.T) {
	state := map[string]any{
		"$schema": "https://schemas.3leaps.dev/spanwit/observe-state/v1.json",
		"version": float64(1),
		"volumes": map[string]any{
			"vol-1": map[string]any{
				"volume": map[string]any{"id": "vol-1", "register_path": "/data"},
				"level":  "ok", "coverage": "incomparable", "reconcile_needed": false,
				"next_seq": float64(1), "foreground": false, "scan_in_flight": false,
			},
		},
	}
	requireValid(t, spanwitschema.SpanwitObserveStateV1, marshalObject(t, state))
	objectAt(t, state, "volumes", "vol-1")["invented"] = true
	requireInvalid(t, spanwitschema.SpanwitObserveStateV1, state)

	health := map[string]any{
		"$schema": "https://schemas.3leaps.dev/spanwit/observe-health/v1.json",
		"version": float64(1), "generated_at": "2026-08-27T12:00:00Z",
		"volumes": []any{}, "mutation_contract": "open",
		"foreground_active": false, "backlog_reconciles": float64(0),
	}
	requireValid(t, spanwitschema.SpanwitObserveHealthV1, marshalObject(t, health))
	health["mutation_contract"] = "execute"
	requireInvalid(t, spanwitschema.SpanwitObserveHealthV1, health)
}

func TestSpaceReportV2ModeAndDisclosureConstraints(t *testing.T) {
	t.Run("capacity-only forbids inventory tuning", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		report["top"] = float64(20)
		requireInvalid(t, spanwitschema.SpanwitSpaceReportV2, report)
	})

	t.Run("capacity-only requires every plane", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		planes := objectAt(t, report, "capacity_accounting", "planes")
		delete(planes, "snapshots")
		requireInvalid(t, spanwitschema.SpanwitSpaceReportV2, report)
	})

	t.Run("full requires v1 inventory shape", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-full.json")
		delete(report, "verified_reclaimable")
		requireInvalid(t, spanwitschema.SpanwitSpaceReportV2, report)
	})

	t.Run("not-requested held-open forbids collection data", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		heldOpen := objectAt(t, report, "capacity_accounting", "planes", "held_open")
		heldOpen["processes_examined"] = float64(0)
		requireInvalid(t, spanwitschema.SpanwitSpaceReportV2, report)
	})

	t.Run("not-requested held-open forbids coverage gaps", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		coverage := objectAt(t, report, "capacity_accounting", "planes", "held_open", "coverage")
		coverage["gaps"] = []any{
			map[string]any{"code": "permission_denied", "scope": "held_open"},
		}
		requireInvalid(t, spanwitschema.SpanwitSpaceReportV2, report)
	})

	t.Run("measured held-open accepts redacted bounded evidence", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		planes := objectAt(t, report, "capacity_accounting", "planes")
		planes["held_open"] = measuredHeldOpen()
		requireValid(t, spanwitschema.SpanwitSpaceReportV2, marshalObject(t, report))
	})

	t.Run("partial held-open can leave denied process quantity unclaimed", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		planes := objectAt(t, report, "capacity_accounting", "planes")
		heldOpen := measuredHeldOpen()
		heldOpen["collection_status"] = "partial"
		delete(heldOpen, "processes_denied")
		objectAt(t, heldOpen, "logical_bytes")["status"] = "partial"
		objectAt(t, heldOpen, "coverage")["gaps"] = []any{
			map[string]any{"code": "permission_denied", "scope": "held_open"},
		}
		planes["held_open"] = heldOpen
		requireValid(t, spanwitschema.SpanwitSpaceReportV2, marshalObject(t, report))
	})

	t.Run("measured held-open requires denied process quantity", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		planes := objectAt(t, report, "capacity_accounting", "planes")
		heldOpen := measuredHeldOpen()
		delete(heldOpen, "processes_denied")
		planes["held_open"] = heldOpen
		requireInvalid(t, spanwitschema.SpanwitSpaceReportV2, report)
	})

	t.Run("redacted held-open rejects entry path", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		planes := objectAt(t, report, "capacity_accounting", "planes")
		planes["held_open"] = measuredHeldOpen()
		entry := arrayAt(t, planes, "held_open", "entries")[0].(map[string]any)
		entry["path"] = "/workspace/client-name/deleted.db"
		requireInvalid(t, spanwitschema.SpanwitSpaceReportV2, report)
	})

	t.Run("redacted held-open rejects gap path", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		planes := objectAt(t, report, "capacity_accounting", "planes")
		heldOpen := measuredHeldOpen()
		objectAt(t, heldOpen, "coverage")["gaps"] = []any{
			map[string]any{
				"code":  "permission_denied",
				"scope": "held_open",
				"paths": []any{"/workspace/client-name/deleted.db"},
			},
		}
		planes["held_open"] = heldOpen
		requireInvalid(t, spanwitschema.SpanwitSpaceReportV2, report)
	})

	t.Run("held-open executable is basename only", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		planes := objectAt(t, report, "capacity_accounting", "planes")
		heldOpen := measuredHeldOpen()
		holder := arrayAt(t, heldOpen, "entries")[0].(map[string]any)["holders"].([]any)[0].(map[string]any)
		holder["executable"] = "/usr/bin/spanwit"
		planes["held_open"] = heldOpen
		requireInvalid(t, spanwitschema.SpanwitSpaceReportV2, report)
	})

	t.Run("held-open logical bytes are lower bounds", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		planes := objectAt(t, report, "capacity_accounting", "planes")
		heldOpen := measuredHeldOpen()
		objectAt(t, heldOpen, "logical_bytes")["bound"] = "exact"
		planes["held_open"] = heldOpen
		requireInvalid(t, spanwitschema.SpanwitSpaceReportV2, report)
	})

	t.Run("unsupported byte claim cannot serialize zero", func(t *testing.T) {
		report := fixtureObject(t, "space-report-v2-capacity-only.json")
		container := objectAt(t, report, "capacity_accounting", "planes", "container")
		container["capacity"] = map[string]any{
			"status": "unsupported",
			"bytes":  float64(0),
		}
		requireInvalid(t, spanwitschema.SpanwitSpaceReportV2, report)
	})
}

func measuredHeldOpen() map[string]any {
	return map[string]any{
		"collection_status": "measured",
		"source":            "lsof_plus_L1",
		"coverage": map[string]any{
			"gaps": []any{},
		},
		"disclosure":         "none",
		"privilege":          "unprivileged",
		"processes_examined": float64(4),
		"processes_denied":   float64(0),
		"unique_objects":     float64(1),
		"holder_processes":   float64(1),
		"relationships":      float64(1),
		"logical_bytes": map[string]any{
			"status": "measured",
			"bytes":  float64(4096),
			"basis":  "lsof_logical_size",
			"bound":  "lower",
		},
		"entries": []any{
			map[string]any{
				"device":       "1,2",
				"inode":        "42",
				"path_present": false,
				"root_class":   "target_tree",
				"logical_bytes": map[string]any{
					"status": "measured",
					"bytes":  float64(4096),
					"basis":  "lsof_logical_size",
					"bound":  "lower",
				},
				"holders": []any{
					map[string]any{
						"pid":        float64(123),
						"executable": "spanwit",
					},
				},
			},
		},
	}
}

func TestSpaceCompareV1ExactDeltaConstraints(t *testing.T) {
	t.Run("input schema identity is required", func(t *testing.T) {
		result := fixtureObject(t, "space-compare-v1.json")
		delete(objectAt(t, result, "left"), "report_schema")
		requireInvalid(t, spanwitschema.SpanwitSpaceCompareV1, result)
	})

	t.Run("exact delta requires exact endpoint bounds", func(t *testing.T) {
		result := fixtureObject(t, "space-compare-v1.json")
		delta := arrayAt(t, result, "plane_deltas")[0].(map[string]any)
		delta["left_bound"] = "lower"
		requireInvalid(t, spanwitschema.SpanwitSpaceCompareV1, result)
	})

	t.Run("exact delta requires source provenance", func(t *testing.T) {
		result := fixtureObject(t, "space-compare-v1.json")
		delta := arrayAt(t, result, "plane_deltas")[0].(map[string]any)
		delete(delta, "right_source")
		requireInvalid(t, spanwitschema.SpanwitSpaceCompareV1, result)
	})

	t.Run("indeterminate delta forbids numeric delta", func(t *testing.T) {
		result := fixtureObject(t, "space-compare-v1.json")
		delta := arrayAt(t, result, "plane_deltas")[0].(map[string]any)
		delta["delta_status"] = "indeterminate"
		requireInvalid(t, spanwitschema.SpanwitSpaceCompareV1, result)
	})
}

func TestSpaceReportV2PreservesV1InventoryContract(t *testing.T) {
	var v1, v2 map[string]any
	if err := json.Unmarshal(spanwitschema.SpanwitSpaceReportV1, &v1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(spanwitschema.SpanwitSpaceReportV2, &v2); err != nil {
		t.Fatal(err)
	}

	v1Defs := objectAt(t, v1, "$defs")
	v2Defs := objectAt(t, v2, "$defs")
	for name, want := range v1Defs {
		got, ok := v2Defs[name]
		if !ok {
			t.Errorf("v2 missing v1 $defs/%s", name)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("v2 changed v1 $defs/%s", name)
		}
	}

	v1Properties := objectAt(t, v1, "properties")
	v2Properties := objectAt(t, v2, "properties")
	for name, want := range v1Properties {
		switch name {
		case "$schema", "version", "pressure", "analysis_pressure":
			continue
		}
		got, ok := v2Properties[name]
		if !ok {
			t.Errorf("v2 missing v1 property %s", name)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("v2 changed v1 property %s", name)
		}
	}
}

func TestCapacitySchemasExcludeForbiddenContractFields(t *testing.T) {
	t.Parallel()
	forbidden := map[string]struct{}{
		"command_summary":     {},
		"explained_bytes":     {},
		"held_open_requested": {},
		"hostname":            {},
		"hostname_hash":       {},
		"reconciled_total":    {},
	}
	for name, schema := range map[string][]byte{
		"space-report-v2":  spanwitschema.SpanwitSpaceReportV2,
		"space-compare-v1": spanwitschema.SpanwitSpaceCompareV1,
	} {
		name, schema := name, schema
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var document any
			if err := json.Unmarshal(schema, &document); err != nil {
				t.Fatal(err)
			}
			walkSchemaProperties(document, func(property string) {
				if _, blocked := forbidden[property]; blocked {
					t.Errorf("forbidden contract property %q", property)
				}
			})
		})
	}
}

func walkSchemaProperties(value any, visit func(string)) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "properties" {
				if properties, ok := child.(map[string]any); ok {
					for property := range properties {
						visit(property)
					}
				}
			}
			walkSchemaProperties(child, visit)
		}
	case []any:
		for _, child := range value {
			walkSchemaProperties(child, visit)
		}
	}
}
