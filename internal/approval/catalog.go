package approval

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/3leaps/spanwit/internal/canonjson"
	"github.com/3leaps/spanwit/internal/config"
)

// DomainSignatureCatalog is the digest domain for the effective built-in
// signature catalog an approval binds.
const DomainSignatureCatalog = "spanwit/signature-catalog/v1"

// SignatureCatalogDigest digests the built-in signature catalog the planner
// merges into every exact plan. It projects every exported field by
// reflection, including the built-in-only safety fields that JSON
// serialisation hides, so a change to any field (present or added later)
// changes the digest. An unsupported field kind is an error, not a skip.
func SignatureCatalogDigest() (string, error) {
	return catalogDigest(config.BuiltInSignatures())
}

func catalogDigest(c config.SignatureCatalog) (string, error) {
	v, err := project(reflect.ValueOf(c), 0)
	if err != nil {
		return "", fmt.Errorf("signature catalog: %w", err)
	}
	d, err := canonjson.Digest(DomainSignatureCatalog, v)
	if err != nil {
		return "", fmt.Errorf("signature catalog: %w", err)
	}
	return "sha256:" + d, nil
}

func project(v reflect.Value, depth int) (canonjson.Value, error) {
	if depth > 16 {
		return nil, fmt.Errorf("nesting too deep")
	}
	switch v.Kind() {
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, nil
		}
		return project(v.Elem(), depth+1)
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil, nil
		}
		out := make([]canonjson.Value, v.Len())
		for i := range out {
			e, err := project(v.Index(i), depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = e
		}
		return out, nil
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map key kind %s", v.Type().Key().Kind())
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		out := canonjson.Object{}
		for _, k := range keys {
			e, err := project(v.MapIndex(k), depth+1)
			if err != nil {
				return nil, err
			}
			out[k.String()] = e
		}
		return out, nil
	case reflect.Struct:
		t := v.Type()
		out := canonjson.Object{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				return nil, fmt.Errorf("unexported field %s.%s", t.Name(), f.Name)
			}
			e, err := project(v.Field(i), depth+1)
			if err != nil {
				return nil, err
			}
			out[f.Name] = e
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported kind %s", v.Kind())
	}
}
