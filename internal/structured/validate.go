package structured

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// maxViolations bounds the detail list so a wildly wrong model response cannot
// produce an unbounded error body.
const maxViolations = 20

// ExtractJSON pulls the JSON object out of a model completion. Models
// occasionally wrap strict output in a ```json fence or add a sentence of
// preamble; anything else is an output_validation_failed.
func ExtractJSON(text string) (json.RawMessage, *Error) {
	candidate := strings.TrimSpace(text)
	if candidate == "" {
		return nil, NewError(CodeOutputValidation, "model returned an empty response")
	}
	candidate = stripCodeFence(candidate)
	if trimmed := trimToOutermostObject(candidate); trimmed != "" {
		candidate = trimmed
	}
	if !json.Valid([]byte(candidate)) {
		return nil, NewError(CodeOutputValidation, "model output is not valid JSON")
	}
	var probe any
	decoder := json.NewDecoder(strings.NewReader(candidate))
	decoder.UseNumber()
	if err := decoder.Decode(&probe); err != nil {
		return nil, NewError(CodeOutputValidation, "model output is not valid JSON", err.Error())
	}
	if _, ok := probe.(map[string]any); !ok {
		return nil, NewError(CodeOutputValidation, "model output is not a JSON object")
	}
	return json.RawMessage(candidate), nil
}

func stripCodeFence(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	rest := strings.TrimPrefix(text, "```")
	if newline := strings.IndexByte(rest, '\n'); newline >= 0 {
		// Drop an optional language tag such as ```json.
		if tag := strings.TrimSpace(rest[:newline]); tag == "" || !strings.ContainsAny(tag, "{ \t") {
			rest = rest[newline+1:]
		}
	}
	if end := strings.LastIndex(rest, "```"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// trimToOutermostObject returns the substring from the first "{" to the last
// "}" so a leading "Here is the JSON:" preamble does not fail the parse. It
// returns "" when no braces are present.
func trimToOutermostObject(text string) string {
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return ""
	}
	return text[start : end+1]
}

// Validate checks decoded model output against a compiled schema. It returns
// an output_validation_failed error listing the violations, or nil.
func Validate(schema *Schema, data json.RawMessage) *Error {
	if schema == nil {
		return NewError(CodeOutputValidation, "no compiled schema available")
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return NewError(CodeOutputValidation, "model output is not valid JSON", err.Error())
	}
	violations := validateNode(schema, value, "$", nil)
	if len(violations) == 0 {
		return nil
	}
	sort.Strings(violations)
	if len(violations) > maxViolations {
		violations = append(violations[:maxViolations], fmt.Sprintf("(%d further violations omitted)", len(violations)-maxViolations))
	}
	return NewError(CodeOutputValidation, "model output does not satisfy the requested schema", violations...)
}

func validateNode(schema *Schema, value any, path string, violations []string) []string {
	if schema == nil {
		return violations
	}
	if !matchesType(schema.Type, value) {
		return append(violations, fmt.Sprintf("%s: expected %s, got %s", path, schema.Type, jsonTypeName(value)))
	}
	if len(schema.Enum) > 0 && !inEnum(schema.Enum, value) {
		violations = append(violations, fmt.Sprintf("%s: value is not one of the allowed enum values", path))
	}

	switch schema.Type {
	case "object":
		object := value.(map[string]any)
		for name := range schema.Required {
			if _, present := object[name]; !present {
				violations = append(violations, fmt.Sprintf("%s.%s: required property is missing", path, name))
			}
		}
		for _, name := range sortedObjectKeys(object) {
			child, known := schema.Properties[name]
			if !known {
				violations = append(violations, fmt.Sprintf("%s.%s: property is not allowed (additionalProperties is false)", path, name))
				continue
			}
			violations = validateNode(child, object[name], path+"."+name, violations)
		}
	case "array":
		items := value.([]any)
		if schema.MinItems != nil && len(items) < *schema.MinItems {
			violations = append(violations, fmt.Sprintf("%s: expected at least %d items, got %d", path, *schema.MinItems, len(items)))
		}
		if schema.MaxItems != nil && len(items) > *schema.MaxItems {
			violations = append(violations, fmt.Sprintf("%s: expected at most %d items, got %d", path, *schema.MaxItems, len(items)))
		}
		for i, item := range items {
			violations = validateNode(schema.Items, item, fmt.Sprintf("%s[%d]", path, i), violations)
		}
	}
	return violations
}

func matchesType(schemaType string, value any) bool {
	switch schemaType {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	default:
		return false
	}
}

func jsonTypeName(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		if _, err := typed.Int64(); err == nil {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func inEnum(enum []any, value any) bool {
	for _, candidate := range enum {
		if enumEqual(candidate, value) {
			return true
		}
	}
	return false
}

// enumEqual compares by JSON encoding so json.Number and float64 candidates
// from the schema document compare equal to decoded output values.
func enumEqual(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	if errLeft != nil || errRight != nil {
		return false
	}
	return string(left) == string(right)
}

func sortedObjectKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
