package capacity

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

func parsePlist(data []byte) (map[string]any, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("plist has no root value")
			}
			return nil, fmt.Errorf("decode plist: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "plist" {
			continue
		}
		value, err := parsePlistValue(decoder, start)
		if err != nil {
			return nil, err
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("plist root must be a dictionary")
		}
		return object, nil
	}
}

func parsePlistValue(decoder *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		result := make(map[string]any)
		for {
			token, err := decoder.Token()
			if err != nil {
				return nil, fmt.Errorf("decode plist dict: %w", err)
			}
			switch token := token.(type) {
			case xml.EndElement:
				if token.Name.Local == "dict" {
					return result, nil
				}
			case xml.StartElement:
				if token.Name.Local != "key" {
					return nil, fmt.Errorf("plist dict expected key, got %s", token.Name.Local)
				}
				var key string
				if err := decoder.DecodeElement(&key, &token); err != nil {
					return nil, fmt.Errorf("decode plist key: %w", err)
				}
				valueStart, err := nextStart(decoder)
				if err != nil {
					return nil, fmt.Errorf("plist key %q: %w", key, err)
				}
				value, err := parsePlistValue(decoder, valueStart)
				if err != nil {
					return nil, fmt.Errorf("plist key %q: %w", key, err)
				}
				result[key] = value
			}
		}
	case "array":
		var result []any
		for {
			token, err := decoder.Token()
			if err != nil {
				return nil, fmt.Errorf("decode plist array: %w", err)
			}
			switch token := token.(type) {
			case xml.EndElement:
				if token.Name.Local == "array" {
					return result, nil
				}
			case xml.StartElement:
				value, err := parsePlistValue(decoder, token)
				if err != nil {
					return nil, err
				}
				result = append(result, value)
			}
		}
	case "string", "date", "data":
		var value string
		if err := decoder.DecodeElement(&value, &start); err != nil {
			return nil, err
		}
		return value, nil
	case "integer":
		var raw string
		if err := decoder.DecodeElement(&raw, &start); err != nil {
			return nil, err
		}
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 0, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid plist integer %q", raw)
		}
		return value, nil
	case "real":
		var raw string
		if err := decoder.DecodeElement(&raw, &start); err != nil {
			return nil, err
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid plist real %q", raw)
		}
		return value, nil
	case "true":
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return true, nil
	case "false":
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return false, nil
	default:
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

func nextStart(decoder *xml.Decoder) (xml.StartElement, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return xml.StartElement{}, err
		}
		if start, ok := token.(xml.StartElement); ok {
			return start, nil
		}
	}
}
