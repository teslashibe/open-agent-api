package structured

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The supported subset is deliberately narrow. Anything outside it is rejected
// at admission with invalid_schema rather than being silently forwarded to a
// model that may or may not honour it:
//
//	type            object, array, string, number, integer, boolean, null
//	object          properties, required (must list every property),
//	                additionalProperties (must be present and false)
//	array           items (required), minItems, maxItems
//	any             enum, description, title
//
// Unsupported by design: $ref, $defs, oneOf, anyOf, allOf, not, if/then/else,
// pattern, format, patternProperties, propertyNames, const, numeric bounds,
// and union types. Callers that need those must pre-flatten the schema.
var supportedKeywords = map[string]bool{
	"type":                 true,
	"properties":           true,
	"required":             true,
	"additionalProperties": true,
	"items":                true,
	"minItems":             true,
	"maxItems":             true,
	"enum":                 true,
	"description":          true,
	"title":                true,
	// $schema is metadata only and is ignored.
	"$schema": true,
}

var supportedTypes = map[string]bool{
	"object":  true,
	"array":   true,
	"string":  true,
	"number":  true,
	"integer": true,
	"boolean": true,
	"null":    true,
}

// Schema is a compiled strict-subset JSON Schema node.
type Schema struct {
	Type          string
	Properties    map[string]*Schema
	PropertyOrder []string
	Required      map[string]bool
	Items         *Schema
	Enum          []any
	MinItems      *int
	MaxItems      *int
}

// CompileSchema parses and validates a strict JSON Schema subset document. The
// root must be an object schema so the response envelope always carries a JSON
// object in "data".
func CompileSchema(raw json.RawMessage) (*Schema, *Error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, NewError(CodeInvalidSchema, "schema is empty")
	}
	var node any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&node); err != nil {
		return nil, NewError(CodeInvalidSchema, "schema is not valid JSON", err.Error())
	}
	compiled, violations := compileNode(node, "$")
	if len(violations) > 0 {
		return nil, NewError(CodeInvalidSchema, "schema uses unsupported constructs", violations...)
	}
	if compiled.Type != "object" {
		return nil, NewError(CodeInvalidSchema, "schema uses unsupported constructs", "$: root schema type must be \"object\"")
	}
	return compiled, nil
}

func compileNode(node any, path string) (*Schema, []string) {
	object, ok := node.(map[string]any)
	if !ok {
		return nil, []string{path + ": schema node must be an object"}
	}

	violations := []string{}
	for _, keyword := range sortedKeys(object) {
		if !supportedKeywords[keyword] {
			violations = append(violations, fmt.Sprintf("%s: unsupported keyword %q", path, keyword))
		}
	}

	schema := &Schema{}
	rawType, ok := object["type"]
	if !ok {
		violations = append(violations, path+": \"type\" is required")
	} else {
		typeName, ok := rawType.(string)
		if !ok {
			violations = append(violations, path+": \"type\" must be a string (union types are unsupported)")
		} else if !supportedTypes[typeName] {
			violations = append(violations, fmt.Sprintf("%s: unsupported type %q", path, typeName))
		} else {
			schema.Type = typeName
		}
	}

	if rawEnum, ok := object["enum"]; ok {
		values, ok := rawEnum.([]any)
		if !ok || len(values) == 0 {
			violations = append(violations, path+": \"enum\" must be a non-empty array")
		} else {
			schema.Enum = values
		}
	}

	switch schema.Type {
	case "object":
		violations = append(violations, compileObject(schema, object, path)...)
	case "array":
		violations = append(violations, compileArray(schema, object, path)...)
	default:
		for _, keyword := range []string{"properties", "required", "additionalProperties", "items", "minItems", "maxItems"} {
			if _, present := object[keyword]; present {
				violations = append(violations, fmt.Sprintf("%s: %q is not valid for type %q", path, keyword, schema.Type))
			}
		}
	}

	return schema, violations
}

func compileObject(schema *Schema, object map[string]any, path string) []string {
	violations := []string{}

	additional, present := object["additionalProperties"]
	if !present {
		violations = append(violations, path+": object schemas must set \"additionalProperties\": false")
	} else if allowed, ok := additional.(bool); !ok || allowed {
		violations = append(violations, path+": \"additionalProperties\" must be false")
	}

	rawProperties, present := object["properties"]
	if !present {
		violations = append(violations, path+": object schemas must declare \"properties\"")
		return violations
	}
	properties, ok := rawProperties.(map[string]any)
	if !ok {
		violations = append(violations, path+": \"properties\" must be an object")
		return violations
	}

	schema.Properties = make(map[string]*Schema, len(properties))
	schema.PropertyOrder = sortedKeys(properties)
	for _, name := range schema.PropertyOrder {
		child, childViolations := compileNode(properties[name], path+"."+name)
		violations = append(violations, childViolations...)
		schema.Properties[name] = child
	}

	rawRequired, present := object["required"]
	if !present {
		violations = append(violations, path+": object schemas must declare \"required\"")
		return violations
	}
	required, ok := rawRequired.([]any)
	if !ok {
		violations = append(violations, path+": \"required\" must be an array")
		return violations
	}
	schema.Required = make(map[string]bool, len(required))
	for _, entry := range required {
		name, ok := entry.(string)
		if !ok {
			violations = append(violations, path+": \"required\" entries must be strings")
			continue
		}
		if _, known := properties[name]; !known {
			violations = append(violations, fmt.Sprintf("%s: required property %q is not declared in \"properties\"", path, name))
			continue
		}
		schema.Required[name] = true
	}
	// Strict mode: every declared property must be required. Optional fields
	// make model output ambiguous and upstream strict json_schema rejects them.
	for _, name := range schema.PropertyOrder {
		if !schema.Required[name] {
			violations = append(violations, fmt.Sprintf("%s: property %q must be listed in \"required\" (strict mode)", path, name))
		}
	}
	return violations
}

func compileArray(schema *Schema, object map[string]any, path string) []string {
	violations := []string{}

	rawItems, present := object["items"]
	if !present {
		violations = append(violations, path+": array schemas must declare \"items\"")
	} else {
		child, childViolations := compileNode(rawItems, path+"[]")
		violations = append(violations, childViolations...)
		schema.Items = child
	}

	for _, bound := range []struct {
		keyword string
		target  **int
	}{
		{"minItems", &schema.MinItems},
		{"maxItems", &schema.MaxItems},
	} {
		raw, present := object[bound.keyword]
		if !present {
			continue
		}
		value, ok := jsonInt(raw)
		if !ok || value < 0 {
			violations = append(violations, fmt.Sprintf("%s: %q must be a non-negative integer", path, bound.keyword))
			continue
		}
		*bound.target = &value
	}
	if schema.MinItems != nil && schema.MaxItems != nil && *schema.MinItems > *schema.MaxItems {
		violations = append(violations, path+": \"minItems\" must not exceed \"maxItems\"")
	}
	return violations
}

func jsonInt(raw any) (int, bool) {
	switch typed := raw.(type) {
	case json.Number:
		value, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(value), true
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
