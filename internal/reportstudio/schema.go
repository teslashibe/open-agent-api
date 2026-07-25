package reportstudio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	MaxSchemaBytes      = 64 * 1024
	maxSchemaDepth      = 5
	maxSchemaNodes      = 2048
	maxSchemaProperties = 100
	maxSchemaEnumValues = 1000
	maxLargeEnumValues  = 250
	maxLargeEnumChars   = 15000
)

var schemaNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Validator struct {
	schema *jsonschema.Schema
}

type blockedLoader struct{}

func (blockedLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference is not allowed: %s", url)
}

func CompileSchema(req SchemaRequest) (*Validator, error) {
	if !req.Strict {
		return nil, errors.New("schema.strict must be true")
	}
	if !schemaNamePattern.MatchString(req.Name) {
		return nil, errors.New("schema.name must contain 1-64 letters, digits, underscores, or hyphens")
	}
	if strings.TrimSpace(req.Version) == "" {
		return nil, errors.New("schema.version is required")
	}
	if len(req.Schema) == 0 || len(req.Schema) > MaxSchemaBytes {
		return nil, fmt.Errorf("json_schema must be between 1 and %d bytes", MaxSchemaBytes)
	}

	var doc any
	dec := json.NewDecoder(bytes.NewReader(req.Schema))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid json_schema: %w", err)
	}
	if err := ensureEOF(dec); err != nil {
		return nil, errors.New("json_schema must contain one JSON value")
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return nil, errors.New("json_schema root must be an object")
	}
	if dialect, _ := root["$schema"].(string); dialect != "" && dialect != "https://json-schema.org/draft/2020-12/schema" {
		return nil, errors.New("only JSON Schema draft 2020-12 is supported")
	}
	nodes := 0
	properties := 0
	enumValues := 0
	if err := inspectSchema(doc, 0, &nodes, &properties, &enumValues, true); err != nil {
		return nil, err
	}
	if typ, _ := root["type"].(string); typ != "object" {
		return nil, errors.New("json_schema root type must be object")
	}
	if additional, ok := root["additionalProperties"].(bool); !ok || additional {
		return nil, errors.New("json_schema root must set additionalProperties to false")
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	compiler.UseLoader(blockedLoader{})
	const resource = "https://codex-chat-api.invalid/report-studio/schema.json"
	if err := compiler.AddResource(resource, doc); err != nil {
		return nil, fmt.Errorf("load json_schema: %w", err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		return nil, fmt.Errorf("compile json_schema: %w", err)
	}
	return &Validator{schema: schema}, nil
}

func inspectSchema(value any, depth int, nodes, propertyCount, enumValueCount *int, root bool) error {
	*nodes = *nodes + 1
	if depth > maxSchemaDepth {
		return errors.New("json_schema exceeds maximum nesting depth")
	}
	if *nodes > maxSchemaNodes {
		return errors.New("json_schema exceeds maximum node count")
	}
	typed, ok := value.(map[string]any)
	if !ok {
		return errors.New("every json_schema node must be an object")
	}
	allowedKeywords := map[string]bool{
		"$defs": true, "$ref": true, "additionalProperties": true, "anyOf": true,
		"description": true, "enum": true, "items": true,
		"properties": true, "required": true, "title": true, "type": true,
	}
	if root {
		allowedKeywords["$schema"] = true
	}
	keys := make([]string, 0, len(typed))
	for key := range typed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !allowedKeywords[key] {
			return fmt.Errorf("json_schema keyword %q is not supported for strict structured output", key)
		}
	}
	if root {
		if _, ok := typed["anyOf"]; ok {
			return errors.New("json_schema root cannot use anyOf")
		}
	}
	if rawRef, ok := typed["$ref"]; ok {
		ref, ok := rawRef.(string)
		if !ok {
			return errors.New("$ref must be a string")
		}
		parsed, err := url.Parse(ref)
		if err != nil || parsed.Scheme != "" || parsed.Host != "" {
			return errors.New("external schema references are not allowed")
		}
		if !strings.HasPrefix(ref, "#") {
			return errors.New("only local schema references are allowed")
		}
	}
	schemaTypes, err := inspectSchemaTypes(typed["type"])
	if err != nil {
		return err
	}
	objectType := schemaTypes["object"]
	arrayType := schemaTypes["array"]
	if objectType {
		properties, ok := typed["properties"].(map[string]any)
		if !ok {
			return errors.New("object schemas must define properties")
		}
		if additional, ok := typed["additionalProperties"].(bool); !ok || additional {
			return errors.New("every object schema must set additionalProperties to false")
		}
		if err := inspectRequiredProperties(properties, typed["required"]); err != nil {
			return err
		}
		*propertyCount += len(properties)
		if *propertyCount > maxSchemaProperties {
			return fmt.Errorf("json_schema exceeds maximum property count of %d", maxSchemaProperties)
		}
		propertyNames := make([]string, 0, len(properties))
		for name := range properties {
			propertyNames = append(propertyNames, name)
		}
		sort.Strings(propertyNames)
		for _, name := range propertyNames {
			if err := inspectSchema(properties[name], depth+1, nodes, propertyCount, enumValueCount, false); err != nil {
				return fmt.Errorf("property %q: %w", name, err)
			}
		}
	} else {
		for _, keyword := range []string{"additionalProperties", "properties", "required"} {
			if _, ok := typed[keyword]; ok {
				return fmt.Errorf("%s is only supported for object schemas", keyword)
			}
		}
	}
	if rawItems, ok := typed["items"]; ok {
		if !arrayType {
			return errors.New("items is only supported for array schemas")
		}
		if err := inspectSchema(rawItems, depth+1, nodes, propertyCount, enumValueCount, false); err != nil {
			return fmt.Errorf("items: %w", err)
		}
	} else if arrayType {
		return errors.New("array schemas must define items")
	}
	if rawAnyOf, ok := typed["anyOf"]; ok {
		alternatives, ok := rawAnyOf.([]any)
		if !ok || len(alternatives) == 0 {
			return errors.New("anyOf must be a non-empty array")
		}
		for i, alternative := range alternatives {
			if err := inspectSchema(alternative, depth+1, nodes, propertyCount, enumValueCount, false); err != nil {
				return fmt.Errorf("anyOf[%d]: %w", i, err)
			}
		}
	}
	if rawDefs, ok := typed["$defs"]; ok {
		defs, ok := rawDefs.(map[string]any)
		if !ok {
			return errors.New("$defs must be an object")
		}
		names := make([]string, 0, len(defs))
		for name := range defs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := inspectSchema(defs[name], depth+1, nodes, propertyCount, enumValueCount, false); err != nil {
				return fmt.Errorf("$defs.%s: %w", name, err)
			}
		}
	}
	if rawEnum, ok := typed["enum"]; ok {
		values, ok := rawEnum.([]any)
		if !ok || len(values) == 0 {
			return errors.New("enum must be a non-empty array")
		}
		*enumValueCount += len(values)
		if *enumValueCount > maxSchemaEnumValues {
			return fmt.Errorf("json_schema exceeds maximum enum value count of %d", maxSchemaEnumValues)
		}
		if len(values) > maxLargeEnumValues {
			stringChars := 0
			for _, value := range values {
				if text, ok := value.(string); ok {
					stringChars += len([]rune(text))
				}
			}
			if stringChars > maxLargeEnumChars {
				return fmt.Errorf(
					"string enum with more than %d values exceeds maximum aggregate length of %d characters",
					maxLargeEnumValues,
					maxLargeEnumChars,
				)
			}
		}
	}
	for _, keyword := range []string{"description", "title"} {
		if value, ok := typed[keyword]; ok {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s must be a string", keyword)
			}
		}
	}
	return nil
}

func inspectSchemaTypes(raw any) (map[string]bool, error) {
	if raw == nil {
		return map[string]bool{}, nil
	}
	allowed := map[string]bool{
		"array": true, "boolean": true, "integer": true, "null": true,
		"number": true, "object": true, "string": true,
	}
	var types []string
	switch typed := raw.(type) {
	case string:
		types = []string{typed}
	case []any:
		for _, value := range typed {
			valueType, ok := value.(string)
			if !ok {
				return nil, errors.New("json_schema type array must contain only strings")
			}
			types = append(types, valueType)
		}
	default:
		return nil, errors.New("json_schema type must be a string or string array")
	}
	if len(types) == 0 || len(types) > 2 {
		return nil, errors.New("json_schema type must contain one type plus optional null")
	}
	seen := map[string]bool{}
	for _, value := range types {
		if !allowed[value] || seen[value] {
			return nil, fmt.Errorf("json_schema type %q is invalid", value)
		}
		seen[value] = true
	}
	if len(types) == 2 && !seen["null"] {
		return nil, errors.New("multiple json_schema types are only supported for nullable fields")
	}
	return seen, nil
}

func inspectRequiredProperties(properties map[string]any, rawRequired any) error {
	required, ok := rawRequired.([]any)
	if !ok {
		return errors.New("object schemas must require every property")
	}
	seen := make(map[string]bool, len(required))
	for _, rawName := range required {
		name, ok := rawName.(string)
		if !ok || seen[name] {
			return errors.New("required must contain unique property names")
		}
		if _, ok := properties[name]; !ok {
			return fmt.Errorf("required property %q is not defined", name)
		}
		seen[name] = true
	}
	if len(seen) != len(properties) {
		return errors.New("strict structured output requires every property")
	}
	return nil
}

func (v *Validator) ValidateRaw(raw []byte) (json.RawMessage, error) {
	if v == nil || v.schema == nil {
		return nil, errors.New("schema validator is not configured")
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("model output is not valid JSON: %w", err)
	}
	if err := ensureEOF(dec); err != nil {
		return nil, errors.New("model output contains trailing content")
	}
	if err := v.schema.Validate(value); err != nil {
		return nil, fmt.Errorf("model output failed schema validation: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode validated output: %w", err)
	}
	return canonical, nil
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	err := dec.Decode(&extra)
	if err == nil {
		return errors.New("trailing JSON")
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
