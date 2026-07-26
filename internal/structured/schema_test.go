package structured

import (
	"encoding/json"
	"strings"
	"testing"
)

const personSchema = `{
	"type": "object",
	"additionalProperties": false,
	"required": ["name", "age", "tags", "address", "status"],
	"properties": {
		"name": {"type": "string"},
		"age": {"type": "integer"},
		"status": {"type": "string", "enum": ["draft", "final"]},
		"tags": {"type": "array", "minItems": 1, "maxItems": 3, "items": {"type": "string"}},
		"address": {
			"type": "object",
			"additionalProperties": false,
			"required": ["city"],
			"properties": {"city": {"type": "string"}}
		}
	}
}`

func TestCompileSchemaAcceptsStrictSubset(t *testing.T) {
	schema, err := CompileSchema(json.RawMessage(personSchema))
	if err != nil {
		t.Fatalf("CompileSchema() error = %v (%v)", err, err.Details)
	}
	if schema.Type != "object" {
		t.Fatalf("root type = %q", schema.Type)
	}
	if got := len(schema.Properties); got != 5 {
		t.Fatalf("properties = %d, want 5", got)
	}
	if !schema.Required["name"] || !schema.Required["address"] {
		t.Fatalf("required = %#v", schema.Required)
	}
	tags := schema.Properties["tags"]
	if tags.Items == nil || tags.Items.Type != "string" {
		t.Fatalf("tags items = %#v", tags.Items)
	}
	if tags.MinItems == nil || *tags.MinItems != 1 || tags.MaxItems == nil || *tags.MaxItems != 3 {
		t.Fatalf("tags bounds = %v %v", tags.MinItems, tags.MaxItems)
	}
	if len(schema.Properties["status"].Enum) != 2 {
		t.Fatalf("status enum = %#v", schema.Properties["status"].Enum)
	}
	if schema.Properties["address"].Properties["city"].Type != "string" {
		t.Fatalf("nested city = %#v", schema.Properties["address"].Properties["city"])
	}
}

func TestCompileSchemaRejectsUnsupportedConstructs(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":                  `{"type":`,
		"empty":                     ``,
		"non object root":           `{"type":"string"}`,
		"missing type":              `{"properties":{},"required":[],"additionalProperties":false}`,
		"union type":                `{"type":["object","null"],"properties":{},"required":[],"additionalProperties":false}`,
		"ref":                       `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"$ref":"#/$defs/x"}}}`,
		"oneOf":                     `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"oneOf":[{"type":"string"}]}}}`,
		"allOf":                     `{"type":"object","additionalProperties":false,"required":["a"],"allOf":[],"properties":{"a":{"type":"string"}}}`,
		"pattern":                   `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"string","pattern":"^x$"}}}`,
		"format":                    `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"string","format":"email"}}}`,
		"open additionalProperties": `{"type":"object","additionalProperties":true,"required":["a"],"properties":{"a":{"type":"string"}}}`,
		"missing additional":        `{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`,
		"optional property":         `{"type":"object","additionalProperties":false,"required":[],"properties":{"a":{"type":"string"}}}`,
		"unknown required":          `{"type":"object","additionalProperties":false,"required":["b"],"properties":{"a":{"type":"string"}}}`,
		"array without items":       `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"array"}}}`,
		"negative minItems":         `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"array","items":{"type":"string"},"minItems":-1}}}`,
		"inverted bounds":           `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"array","items":{"type":"string"},"minItems":3,"maxItems":1}}}`,
		"unsupported type":          `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"date"}}}`,
		"items on string":           `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"string","items":{"type":"string"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			schema, err := CompileSchema(json.RawMessage(raw))
			if err == nil {
				t.Fatalf("CompileSchema(%s) accepted %#v, want invalid_schema", raw, schema)
			}
			if err.Code != CodeInvalidSchema {
				t.Fatalf("code = %q, want %q", err.Code, CodeInvalidSchema)
			}
		})
	}
}

func TestCompileSchemaReportsEveryViolation(t *testing.T) {
	_, err := CompileSchema(json.RawMessage(`{
		"type": "object",
		"required": ["a"],
		"properties": {"a": {"type": "string", "pattern": "x"}}
	}`))
	if err == nil {
		t.Fatal("CompileSchema() accepted an invalid schema")
	}
	joined := strings.Join(err.Details, "\n")
	for _, want := range []string{"additionalProperties", "pattern"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("details %q missing %q", joined, want)
		}
	}
}
