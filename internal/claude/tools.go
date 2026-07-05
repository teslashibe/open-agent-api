package claude

import (
	"encoding/json"
	"fmt"
	"strings"
)

const toolCallFence = "```cursor_tool_call"

type toolSpec struct {
	Name        string
	Type        string
	Description string
	Parameters  json.RawMessage
}

type cursorToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Input     string          `json:"input,omitempty"`
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
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	out := make([]toolSpec, 0, len(tools))
	for _, tool := range tools {
		spec := toolSpec{Type: tool.Type, Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters}
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
		if spec.Name != "" {
			out = append(out, spec)
		}
	}
	return out
}

func toolInstructions(specs []toolSpec) string {
	if len(specs) == 0 {
		return ""
	}
	payload := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		entry := map[string]any{"name": spec.Name, "type": spec.Type}
		if spec.Description != "" {
			entry["description"] = spec.Description
		}
		if len(spec.Parameters) > 0 && json.Valid(spec.Parameters) {
			var schema any
			if err := json.Unmarshal(spec.Parameters, &schema); err == nil {
				entry["parameters"] = schema
			}
		}
		payload = append(payload, entry)
	}
	data, _ := json.Marshal(payload)
	return fmt.Sprintf("Cursor tool protocol:\n"+
		"Cursor, not Claude Code, executes tools. You must request Cursor tool execution with this exact fenced JSON block and no other text whenever you need current repository, file, terminal, transcript, or workspace information.\n"+
		"Do not claim repo access, tool access, file contents, command results, or implementation status unless a prior Tool result in this conversation provided it.\n"+
		"If the user asks you to inspect, search, read, edit, verify, test, run commands, check logs, or continue previous work, request a tool.\n"+
		"For pure conversational questions that require no external state, answer normally.\n\n"+
		"%s\n"+
		"{\"name\":\"tool_name\",\"arguments\":{}}\n"+
		"```\n\n"+
		"Use exactly one of the tools below. For custom tools, put freeform text in \"input\" instead of \"arguments\".\n"+
		"Available tools:\n%s\n", toolCallFence, string(data))
}

func extractCursorToolCall(text string) (cursorToolCall, bool) {
	start := strings.Index(text, toolCallFence)
	if start < 0 {
		return cursorToolCall{}, false
	}
	bodyStart := start + len(toolCallFence)
	body := text[bodyStart:]
	if strings.HasPrefix(body, "\n") {
		body = body[1:]
	}
	end := strings.Index(body, "```")
	if end < 0 {
		return cursorToolCall{}, false
	}
	body = strings.TrimSpace(body[:end])
	var call cursorToolCall
	if err := json.Unmarshal([]byte(body), &call); err != nil || call.Name == "" {
		return cursorToolCall{}, false
	}
	return call, true
}

// parseToolCallBody parses the JSON payload of a cursor_tool_call fence. It is
// tolerant of surrounding prose: if the body does not parse directly, it falls
// back to the outermost JSON object it can find.
func parseToolCallBody(body string) (cursorToolCall, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return cursorToolCall{}, false
	}
	if call, ok := decodeToolCall(body); ok {
		return call, true
	}
	start := strings.Index(body, "{")
	end := strings.LastIndex(body, "}")
	if start < 0 || end <= start {
		return cursorToolCall{}, false
	}
	return decodeToolCall(body[start : end+1])
}

func decodeToolCall(body string) (cursorToolCall, bool) {
	var call cursorToolCall
	if err := json.Unmarshal([]byte(body), &call); err != nil || call.Name == "" {
		return cursorToolCall{}, false
	}
	return call, true
}

func toolSpecByName(specs []toolSpec) map[string]toolSpec {
	out := map[string]toolSpec{}
	for _, spec := range specs {
		out[spec.Name] = spec
	}
	return out
}
