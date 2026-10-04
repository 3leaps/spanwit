package space

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	spanwitschema "github.com/3leaps/spanwit/config/schema"
	"github.com/3leaps/spanwit/internal/capacity"
	"github.com/3leaps/spanwit/internal/contract"
)

const (
	MaxCompareInputBytes = 16 << 20
	MaxCompareArrayItems = 10_000
	MaxCompareJSONDepth  = 64
)

type compareCarrier struct {
	Schema             string              `json:"$schema"`
	Version            int                 `json:"version"`
	GeneratedAt        string              `json:"generated_at"`
	CaptureMode        string              `json:"capture_mode"`
	Pressure           Pressure            `json:"pressure"`
	CapacityAccounting capacity.Accounting `json:"capacity_accounting"`
}

// LoadCompareInputJSON validates one untrusted SpaceReport v2 capture for the
// compare core. Resource ceilings are checked before schema validation; no
// prune projection or implicit local state participates.
func LoadCompareInputJSON(raw []byte, sourcePath string) (capacity.CompareInput, error) {
	if len(raw) == 0 {
		return capacity.CompareInput{}, fmt.Errorf("compare input is empty")
	}
	if len(raw) > MaxCompareInputBytes {
		return capacity.CompareInput{}, fmt.Errorf(
			"compare input exceeds %d-byte limit",
			MaxCompareInputBytes,
		)
	}
	if err := validateCompareJSONLimits(raw); err != nil {
		return capacity.CompareInput{}, err
	}

	var envelope reportEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return capacity.CompareInput{}, fmt.Errorf("parse compare input envelope: %w", err)
	}
	if envelope.Schema != SchemaV2ID || envelope.Version != 2 ||
		(envelope.CaptureMode != CaptureModeFull && envelope.CaptureMode != CaptureModeCapacityOnly) {
		return capacity.CompareInput{}, fmt.Errorf(
			"compare input must be a SpaceReport v2 capture",
		)
	}
	if err := contract.ValidateJSON(spanwitschema.SpanwitSpaceReportV2, raw); err != nil {
		return capacity.CompareInput{}, fmt.Errorf("compare input schema validation failed: %w", err)
	}

	var carrier compareCarrier
	if err := json.Unmarshal(raw, &carrier); err != nil {
		return capacity.CompareInput{}, fmt.Errorf("parse compare input carrier: %w", err)
	}
	if err := validateCapacityCarrier(
		carrier.GeneratedAt,
		carrier.Pressure,
		carrier.CapacityAccounting,
	); err != nil {
		return capacity.CompareInput{}, fmt.Errorf("compare input capacity invariant: %w", err)
	}
	if err := validateCompareCapacityTopology(carrier.CapacityAccounting); err != nil {
		return capacity.CompareInput{}, fmt.Errorf("compare input capacity topology: %w", err)
	}
	return capacity.CompareInput{
		Path:          sourcePath,
		ReportSchema:  carrier.Schema,
		ReportVersion: carrier.Version,
		CaptureMode:   carrier.CaptureMode,
		CapturedAt:    carrier.GeneratedAt,
		Accounting:    carrier.CapacityAccounting,
	}, nil
}

func validateCompareCapacityTopology(accounting capacity.Accounting) error {
	target := accounting.Target
	container := accounting.Planes.Container
	if (container.Capacity != nil || container.Free != nil) &&
		(target.ContainerID == "" || container.ID == "" || container.ID != target.ContainerID) {
		return fmt.Errorf("numeric container claims require plane and target container identity agreement")
	}
	targetEntries := 0
	for _, entry := range accounting.Planes.Volumes.Entries {
		if !entry.IsTarget {
			continue
		}
		targetEntries++
	}
	if targetEntries > 1 {
		return fmt.Errorf("capacity volumes contain multiple target entries")
	}
	return nil
}

type jsonLimitFrame struct {
	kind         json.Delim
	items        int
	expectingKey bool
}

func validateCompareJSONLimits(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	stack := make([]jsonLimitFrame, 0, 8)
	rootValues := 0

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("compare input is not valid JSON: %w", err)
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{', '[':
				if err := noteJSONValue(&stack, &rootValues); err != nil {
					return err
				}
				if len(stack)+1 > MaxCompareJSONDepth {
					return fmt.Errorf("compare input exceeds JSON depth limit %d", MaxCompareJSONDepth)
				}
				stack = append(stack, jsonLimitFrame{
					kind:         delimiter,
					expectingKey: delimiter == '{',
				})
			case '}', ']':
				if len(stack) == 0 {
					return fmt.Errorf("compare input has an unmatched JSON delimiter")
				}
				top := stack[len(stack)-1]
				if (delimiter == '}' && top.kind != '{') || (delimiter == ']' && top.kind != '[') {
					return fmt.Errorf("compare input has mismatched JSON delimiters")
				}
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if len(stack) > 0 && stack[len(stack)-1].kind == '{' &&
			stack[len(stack)-1].expectingKey {
			if _, ok := token.(string); !ok {
				return fmt.Errorf("compare input object key is not a string")
			}
			stack[len(stack)-1].expectingKey = false
			continue
		}
		if err := noteJSONValue(&stack, &rootValues); err != nil {
			return err
		}
	}
	if len(stack) != 0 || rootValues != 1 {
		return fmt.Errorf("compare input must contain exactly one complete JSON value")
	}
	return nil
}

func noteJSONValue(stack *[]jsonLimitFrame, rootValues *int) error {
	if len(*stack) == 0 {
		(*rootValues)++
		if *rootValues > 1 {
			return fmt.Errorf("compare input contains multiple JSON values")
		}
		return nil
	}
	index := len(*stack) - 1
	frame := &(*stack)[index]
	if frame.kind == '[' {
		frame.items++
		if frame.items > MaxCompareArrayItems {
			return fmt.Errorf(
				"compare input array exceeds %d-item limit",
				MaxCompareArrayItems,
			)
		}
		return nil
	}
	if frame.expectingKey {
		return fmt.Errorf("compare input object is missing a property value")
	}
	frame.expectingKey = true
	return nil
}
