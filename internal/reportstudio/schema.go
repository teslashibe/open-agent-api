package reportstudio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	MaxSchemaBytes = 64 * 1024
	maxSchemaDepth = 32
	maxSchemaNodes = 2048
)

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
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("schema.name is required")
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
	if err := inspectSchema(doc, 0, &nodes); err != nil {
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

func inspectSchema(value any, depth int, nodes *int) error {
	*nodes++
	if depth > maxSchemaDepth {
		return errors.New("json_schema exceeds maximum nesting depth")
	}
	if *nodes > maxSchemaNodes {
		return errors.New("json_schema exceeds maximum node count")
	}
	switch typed := value.(type) {
	case map[string]any:
		if typ, _ := typed["type"].(string); typ == "object" {
			if additional, ok := typed["additionalProperties"].(bool); !ok || additional {
				return errors.New("every object schema must set additionalProperties to false")
			}
		}
		if rawRef, ok := typed["$ref"].(string); ok {
			parsed, err := url.Parse(rawRef)
			if err != nil || (parsed.Scheme != "" || parsed.Host != "") {
				return errors.New("external schema references are not allowed")
			}
			if !strings.HasPrefix(rawRef, "#") {
				return errors.New("only local schema references are allowed")
			}
		}
		for _, nested := range typed {
			if err := inspectSchema(nested, depth+1, nodes); err != nil {
				return err
			}
		}
	case []any:
		for _, nested := range typed {
			if err := inspectSchema(nested, depth+1, nodes); err != nil {
				return err
			}
		}
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
