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

// Issue 130 AC4: the schema bound is explicit and exact. At the cap the schema
// still compiles; one byte over is invalid_schema, and the whole point of the
// pre-decode check is that the over-cap document is never parsed.
func TestCompileSchemaBoundsSchemaSize(t *testing.T) {
	if MaxSchemaBytes > 256<<10 {
		t.Fatalf("MaxSchemaBytes = %d, want no more than 256 KiB", MaxSchemaBytes)
	}
	if MaxSchemaBytes >= maxInputBytes {
		t.Fatalf("MaxSchemaBytes = %d, want it below the %d-byte input cap", MaxSchemaBytes, maxInputBytes)
	}

	atCap := paddedSchema(t, MaxSchemaBytes)
	if _, err := CompileSchema(json.RawMessage(atCap)); err != nil {
		t.Fatalf("CompileSchema() at the cap error = %v (%v)", err, err.Details)
	}

	overCap := paddedSchema(t, MaxSchemaBytes+1)
	err := mustRejectSchema(t, overCap)
	if err.Code != CodeInvalidSchema {
		t.Fatalf("code = %q, want %q", err.Code, CodeInvalidSchema)
	}
	if err.Message != "schema exceeds the maximum size" {
		t.Fatalf("message = %q, want the size message", err.Message)
	}

	// A syntactically broken document over the cap reports the size, not a
	// parse error: it is rejected before it is decoded.
	broken := strings.Repeat("{", MaxSchemaBytes+1)
	if got := mustRejectSchema(t, broken); got.Message != "schema exceeds the maximum size" {
		t.Fatalf("over-cap invalid JSON = %q, want the size message (it must not be parsed)", got.Message)
	}
}

// mustRejectSchema compiles raw and fails the test unless it was rejected.
func mustRejectSchema(t *testing.T, raw string) *Error {
	t.Helper()
	schema, err := CompileSchema(json.RawMessage(raw))
	if err == nil {
		t.Fatalf("CompileSchema() accepted %#v, want a rejection", schema)
	}
	return err
}

// paddedSchema builds a valid strict-subset schema whose encoding is exactly
// size bytes, padded through the supported "description" keyword.
func paddedSchema(t *testing.T, size int) string {
	t.Helper()
	const (
		prefix = `{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"string"}},"description":"`
		suffix = `"}`
	)
	pad := size - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("size %d is smaller than the schema skeleton (%d bytes)", size, len(prefix)+len(suffix))
	}
	schema := prefix + strings.Repeat("d", pad) + suffix
	if len(schema) != size {
		t.Fatalf("padded schema = %d bytes, want %d", len(schema), size)
	}
	return schema
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
