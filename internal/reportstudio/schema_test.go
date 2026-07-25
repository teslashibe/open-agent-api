package reportstudio

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func strictTestSchema() SchemaRequest {
	return SchemaRequest{
		Name: "summary", Version: "summary.v1", Strict: true,
		Schema: json.RawMessage(`{
			"type":"object",
			"properties":{"title":{"type":"string"},"count":{"type":"integer"}},
			"required":["title","count"],
			"additionalProperties":false
		}`),
	}
}

func TestCompileSchemaAndValidateOutput(t *testing.T) {
	validator, err := CompileSchema(strictTestSchema())
	if err != nil {
		t.Fatalf("CompileSchema() error = %v", err)
	}
	got, err := validator.ValidateRaw([]byte(`{"count":2,"title":"ok"}`))
	if err != nil {
		t.Fatalf("ValidateRaw() error = %v", err)
	}
	if string(got) != `{"count":2,"title":"ok"}` {
		t.Fatalf("canonical output = %s", got)
	}
}

func TestCompileSchemaRejectsNonStrictAndRemoteReferences(t *testing.T) {
	nonStrict := strictTestSchema()
	nonStrict.Strict = false
	if _, err := CompileSchema(nonStrict); err == nil || !strings.Contains(err.Error(), "strict") {
		t.Fatalf("non-strict error = %v", err)
	}
	remote := strictTestSchema()
	remote.Schema = json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"https://example.test/schema"}},"required":["x"],"additionalProperties":false}`)
	if _, err := CompileSchema(remote); err == nil || !strings.Contains(err.Error(), "external") {
		t.Fatalf("remote ref error = %v", err)
	}
}

func TestCompileSchemaRejectsInvalidNameAndUnsupportedStrictConstructs(t *testing.T) {
	invalidName := strictTestSchema()
	invalidName.Name = "invalid schema name"
	if _, err := CompileSchema(invalidName); err == nil || !strings.Contains(err.Error(), "schema.name") {
		t.Fatalf("invalid name error = %v", err)
	}

	unsupported := strictTestSchema()
	unsupported.Schema = json.RawMessage(`{
		"type":"object",
		"properties":{"title":{"type":"string","minLength":1}},
		"required":["title"],
		"additionalProperties":false
	}`)
	if _, err := CompileSchema(unsupported); err == nil || !strings.Contains(err.Error(), "minLength") {
		t.Fatalf("unsupported keyword error = %v", err)
	}

	optionalProperty := strictTestSchema()
	optionalProperty.Schema = json.RawMessage(`{
		"type":"object",
		"properties":{"title":{"type":"string"},"subtitle":{"type":"string"}},
		"required":["title"],
		"additionalProperties":false
	}`)
	if _, err := CompileSchema(optionalProperty); err == nil || !strings.Contains(err.Error(), "every property") {
		t.Fatalf("optional property error = %v", err)
	}
}

func TestCompileSchemaRejectsUpstreamEnumLimits(t *testing.T) {
	schemaWithEnum := func(values []string) SchemaRequest {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string", "enum": values},
			},
			"required":             []string{"value"},
			"additionalProperties": false,
		})
		if err != nil {
			t.Fatal(err)
		}
		req := strictTestSchema()
		req.Schema = raw
		return req
	}

	tooMany := make([]string, maxSchemaEnumValues+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("value-%04d", i)
	}
	if _, err := CompileSchema(schemaWithEnum(tooMany)); err == nil || !strings.Contains(err.Error(), "enum value count") {
		t.Fatalf("enum count error = %v", err)
	}

	tooLong := make([]string, maxLargeEnumValues+1)
	for i := range tooLong {
		tooLong[i] = fmt.Sprintf("%060d", i)
	}
	if _, err := CompileSchema(schemaWithEnum(tooLong)); err == nil || !strings.Contains(err.Error(), "aggregate length") {
		t.Fatalf("enum string length error = %v", err)
	}
}

func TestValidateRawRejectsMalformedTrailingAndSchemaMismatch(t *testing.T) {
	validator, err := CompileSchema(strictTestSchema())
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`not-json`,
		`{"title":"ok","count":2} trailing`,
		`{"title":"ok","count":"two"}`,
		`{"title":"ok","count":2,"extra":true}`,
	} {
		if _, err := validator.ValidateRaw([]byte(raw)); err == nil {
			t.Fatalf("ValidateRaw(%q) unexpectedly succeeded", raw)
		}
	}
}
