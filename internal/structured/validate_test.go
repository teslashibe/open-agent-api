package structured

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustCompile(t *testing.T, raw string) *Schema {
	t.Helper()
	schema, err := CompileSchema(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("CompileSchema() error = %v (%v)", err, err.Details)
	}
	return schema
}

func TestValidateAcceptsConformingOutput(t *testing.T) {
	schema := mustCompile(t, personSchema)
	data := json.RawMessage(`{
		"name": "Ada",
		"age": 36,
		"status": "final",
		"tags": ["a", "b"],
		"address": {"city": "London"}
	}`)
	if err := Validate(schema, data); err != nil {
		t.Fatalf("Validate() error = %v (%v)", err, err.Details)
	}
}

func TestValidateRejectsNonConformingOutput(t *testing.T) {
	schema := mustCompile(t, personSchema)
	for name, payload := range map[string]string{
		"missing required":     `{"name":"Ada","status":"final","tags":["a"],"address":{"city":"L"}}`,
		"extra property":       `{"name":"Ada","age":1,"status":"final","tags":["a"],"address":{"city":"L"},"extra":1}`,
		"wrong scalar type":    `{"name":1,"age":1,"status":"final","tags":["a"],"address":{"city":"L"}}`,
		"float for integer":    `{"name":"Ada","age":1.5,"status":"final","tags":["a"],"address":{"city":"L"}}`,
		"enum violation":       `{"name":"Ada","age":1,"status":"archived","tags":["a"],"address":{"city":"L"}}`,
		"too few items":        `{"name":"Ada","age":1,"status":"final","tags":[],"address":{"city":"L"}}`,
		"too many items":       `{"name":"Ada","age":1,"status":"final","tags":["a","b","c","d"],"address":{"city":"L"}}`,
		"wrong item type":      `{"name":"Ada","age":1,"status":"final","tags":[1],"address":{"city":"L"}}`,
		"nested wrong type":    `{"name":"Ada","age":1,"status":"final","tags":["a"],"address":{"city":2}}`,
		"nested extra":         `{"name":"Ada","age":1,"status":"final","tags":["a"],"address":{"city":"L","zip":"1"}}`,
		"array instead object": `[]`,
		"null":                 `null`,
	} {
		t.Run(name, func(t *testing.T) {
			err := Validate(schema, json.RawMessage(payload))
			if err == nil {
				t.Fatalf("Validate(%s) accepted non-conforming output", payload)
			}
			if err.Code != CodeOutputValidation {
				t.Fatalf("code = %q, want %q", err.Code, CodeOutputValidation)
			}
			if len(err.Details) == 0 {
				t.Fatal("expected at least one violation detail")
			}
		})
	}
}

func TestValidateAcceptsIntegerForNumber(t *testing.T) {
	schema := mustCompile(t, `{"type":"object","additionalProperties":false,"required":["score"],"properties":{"score":{"type":"number"}}}`)
	for _, payload := range []string{`{"score":1}`, `{"score":1.5}`, `{"score":-0.25}`} {
		if err := Validate(schema, json.RawMessage(payload)); err != nil {
			t.Fatalf("Validate(%s) error = %v", payload, err.Details)
		}
	}
	if err := Validate(schema, json.RawMessage(`{"score":"1"}`)); err == nil {
		t.Fatal("Validate() accepted a string for a number")
	}
}

func TestValidateAllowsNullTypeAndBooleans(t *testing.T) {
	schema := mustCompile(t, `{"type":"object","additionalProperties":false,"required":["note","ok"],"properties":{"note":{"type":"null"},"ok":{"type":"boolean"}}}`)
	if err := Validate(schema, json.RawMessage(`{"note":null,"ok":true}`)); err != nil {
		t.Fatalf("Validate() error = %v", err.Details)
	}
	if err := Validate(schema, json.RawMessage(`{"note":"x","ok":true}`)); err == nil {
		t.Fatal("Validate() accepted a string for a null type")
	}
}

func TestValidateBoundsViolationDetails(t *testing.T) {
	schema := mustCompile(t, `{"type":"object","additionalProperties":false,"required":["items"],"properties":{"items":{"type":"array","items":{"type":"string"}}}}`)
	items := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		items = append(items, "1")
	}
	err := Validate(schema, json.RawMessage(`{"items":[`+strings.Join(items, ",")+`]}`))
	if err == nil {
		t.Fatal("Validate() accepted 60 integers for a string array")
	}
	if len(err.Details) != maxViolations+1 {
		t.Fatalf("details = %d, want %d truncated entries", len(err.Details), maxViolations+1)
	}
	if !strings.Contains(err.Details[len(err.Details)-1], "further violations omitted") {
		t.Fatalf("last detail = %q", err.Details[len(err.Details)-1])
	}
}

func TestExtractJSONHandlesModelFormatting(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"bare":       {`{"a":1}`, `{"a":1}`},
		"whitespace": {"  \n{\"a\":1}\n ", `{"a":1}`},
		"fenced":     {"```json\n{\"a\":1}\n```", `{"a":1}`},
		"bare fence": {"```\n{\"a\":1}\n```", `{"a":1}`},
		"preamble":   {"Here is the JSON: {\"a\":1}", `{"a":1}`},
		"trailer":    {"{\"a\":1}\nHope that helps!", `{"a":1}`},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := ExtractJSON(tc.text)
			if err != nil {
				t.Fatalf("ExtractJSON(%q) error = %v", tc.text, err)
			}
			if string(data) != tc.want {
				t.Fatalf("ExtractJSON(%q) = %s, want %s", tc.text, data, tc.want)
			}
		})
	}
}

func TestExtractJSONRejectsMalformedOutput(t *testing.T) {
	for name, text := range map[string]string{
		"empty":        "",
		"whitespace":   "   \n ",
		"prose":        "I cannot answer that.",
		"truncated":    `{"a": 1`,
		"array":        `[1,2,3]`,
		"scalar":       `"hello"`,
		"broken fence": "```json\n{\"a\": }\n```",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := ExtractJSON(text)
			if err == nil {
				t.Fatalf("ExtractJSON(%q) = %s, want output_validation_failed", text, data)
			}
			if err.Code != CodeOutputValidation {
				t.Fatalf("code = %q, want %q", err.Code, CodeOutputValidation)
			}
		})
	}
}
