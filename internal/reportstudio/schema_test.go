package reportstudio

import (
	"encoding/json"
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
	remote.Schema = json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"https://example.test/schema"}},"additionalProperties":false}`)
	if _, err := CompileSchema(remote); err == nil || !strings.Contains(err.Error(), "external") {
		t.Fatalf("remote ref error = %v", err)
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
