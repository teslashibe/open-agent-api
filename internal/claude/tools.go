package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/teslashibe/open-agent-api/internal/claude/mcpbridge"
)

// maxToolNameLen is the Anthropic API limit on tool names, which applies to
// the prefixed name the model sees (mcp__c__<name>).
const maxToolNameLen = 64

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type toolSpec struct {
	Name        string
	Type        string
	Description string
	Parameters  json.RawMessage
}

// toolSet maps the client's tools onto MCP tools served by the bridge. Names
// that are not valid Anthropic tool names, or too long once prefixed, are
// replaced by a stable hashed name and mapped back on the way out.
type toolSet struct {
	specs      []toolSpec
	wireByName map[string]string
	byWire     map[string]toolSpec
}

func parseToolSpecs(raw json.RawMessage) []toolSpec {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var tools []struct {
		Type     string `json:"type"`
		Function *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
		Custom *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"custom"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		// Anthropic-shaped tools ({name, description, input_schema}).
		InputSchema json.RawMessage `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	out := make([]toolSpec, 0, len(tools))
	for _, tool := range tools {
		spec := toolSpec{Type: tool.Type, Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}
		if len(spec.Parameters) == 0 {
			spec.Parameters = tool.InputSchema
		}
		if spec.Type == "" {
			spec.Type = "function"
		}
		if tool.Function != nil {
			spec.Type = "function"
			spec.Name = tool.Function.Name
			spec.Description = tool.Function.Description
			spec.Parameters = tool.Function.Parameters
		}
		if tool.Custom != nil {
			spec.Type = "custom"
			spec.Name = tool.Custom.Name
			spec.Description = tool.Custom.Description
			spec.Parameters = tool.Custom.Parameters
		}
		if spec.Type != "function" && spec.Type != "custom" {
			// Anthropic-shaped tools carry an arbitrary or missing type.
			spec.Type = "function"
		}
		if spec.Name != "" {
			out = append(out, spec)
		}
	}
	return out
}

func newToolSet(specs []toolSpec) *toolSet {
	set := &toolSet{wireByName: map[string]string{}, byWire: map[string]toolSpec{}}
	for _, spec := range specs {
		if _, dup := set.wireByName[spec.Name]; dup {
			continue
		}
		wire := wireToolName(spec.Name)
		for _, taken := set.byWire[wire]; taken; _, taken = set.byWire[wire] {
			wire = hashedToolName(wire + spec.Name)
		}
		set.specs = append(set.specs, spec)
		set.wireByName[spec.Name] = wire
		set.byWire[wire] = spec
	}
	return set
}

func (s *toolSet) empty() bool {
	return s == nil || len(s.specs) == 0
}

// modelName is the name the model sees for a client tool. Tools that are not
// in the current request (older history) are still prefixed consistently.
func (s *toolSet) modelName(clientName string) string {
	if s != nil {
		if wire, ok := s.wireByName[clientName]; ok {
			return mcpbridge.ToolPrefix + wire
		}
	}
	return mcpbridge.ToolPrefix + wireToolName(clientName)
}

// clientTool resolves a model-emitted tool name back to the client's tool.
func (s *toolSet) clientTool(modelName string) (toolSpec, bool) {
	wire := strings.TrimPrefix(modelName, mcpbridge.ToolPrefix)
	if s != nil {
		if spec, ok := s.byWire[wire]; ok {
			return spec, true
		}
	}
	return toolSpec{Name: wire, Type: "function"}, false
}

func (s *toolSet) isCustom(clientName string) bool {
	if s == nil {
		return false
	}
	wire, ok := s.wireByName[clientName]
	return ok && s.byWire[wire].Type == "custom"
}

func (s *toolSet) bridgeTools() []mcpbridge.Tool {
	out := make([]mcpbridge.Tool, 0, len(s.specs))
	for _, spec := range s.specs {
		out = append(out, mcpbridge.Tool{
			Name:        s.wireByName[spec.Name],
			Description: spec.Description,
			InputSchema: inputSchema(spec),
		})
	}
	return out
}

func wireToolName(name string) string {
	if toolNamePattern.MatchString(name) && len(mcpbridge.ToolPrefix)+len(name) <= maxToolNameLen {
		return name
	}
	return hashedToolName(name)
}

func hashedToolName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "t_" + hex.EncodeToString(sum[:6])
}

// schemaNeedsWrapper is shared by schema advertisement, output unwrapping
// and history replay, so callers always see their original argument shape.
func schemaNeedsWrapper(spec toolSpec) bool {
	if spec.Type == "custom" {
		return true
	}
	var schema map[string]any
	if json.Unmarshal(spec.Parameters, &schema) != nil || schema == nil {
		return false
	}
	if kind, present := schema["type"]; present && kind != "object" {
		return true
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if _, present := schema[key]; present {
			return true
		}
	}
	return false
}

func (s *toolSet) wrapsInput(clientName string) bool {
	if s == nil {
		return false
	}
	wire, ok := s.wireByName[clientName]
	return ok && schemaNeedsWrapper(s.byWire[wire])
}

// References in a wrapped schema would resolve against the new wrapper root.
// Reject them explicitly instead of changing their meaning or dropping them.
// Ordinary object schemas retain their reference root and definitions.
func hasSchemaReference(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if key == "$ref" || key == "$dynamicRef" || key == "$recursiveRef" {
				if _, isReference := child.(string); isReference {
					return true
				}
			}
			if hasSchemaReference(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if hasSchemaReference(child) {
				return true
			}
		}
	}
	return false
}

func (s *toolSet) validateSchemas() error {
	for _, spec := range s.specs {
		if spec.Type == "custom" || len(spec.Parameters) == 0 {
			continue
		}
		var schema map[string]any
		if json.Unmarshal(spec.Parameters, &schema) != nil || schema == nil {
			return fmt.Errorf("tool %q requires a JSON schema object", spec.Name)
		}
		if schemaNeedsWrapper(spec) && hasSchemaReference(schema) {
			return fmt.Errorf("tool %q uses references in a schema that requires input wrapping; this combination is not supported", spec.Name)
		}
	}
	return nil
}

// inputSchema preserves constraints under an input-object wrapper when the
// original schema has a non-object type or top-level combinators. The caller
// validates reference compatibility before creating any provider process.
func inputSchema(spec toolSpec) json.RawMessage {
	if spec.Type == "custom" {
		return json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"Raw freeform tool input."}},"required":["input"]}`)
	}
	var schema map[string]any
	decoder := json.NewDecoder(bytes.NewReader(spec.Parameters))
	decoder.UseNumber()
	if len(spec.Parameters) == 0 || decoder.Decode(&schema) != nil || schema == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	delete(schema, "$schema")
	if schemaNeedsWrapper(spec) {
		wrapped, _ := json.Marshal(map[string]any{
			"type": "object", "properties": map[string]any{"input": schema},
			"required": []string{"input"}, "additionalProperties": false,
		})
		return wrapped
	}
	schema["type"] = "object"
	if _, ok := schema["properties"]; !ok {
		schema["properties"] = map[string]any{}
	}
	data, _ := json.Marshal(schema)
	return data
}
