package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	toolWireNone   = "none"
	toolWireNested = "nested"
	toolWireFlat   = "flat"
	toolWireMixed  = "mixed"
)

type cursorWireSummary struct {
	ToolWire       string
	ToolChoice     string
	ParallelTools  string
	HasMetadata    bool
	HasUser        bool
	AssistantTools int
	ToolMessages   int
}

type streamResponseShape struct {
	Content          bool
	ReasoningContent bool
	ToolCalls        bool
}

func summarizeCursorWire(raw []byte) cursorWireSummary {
	summary := cursorWireSummary{
		ToolWire:      toolWireNone,
		ToolChoice:    "absent",
		ParallelTools: "absent",
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return summary
	}

	if rawTools, ok := body["tools"]; ok && rawJSONPresent(rawTools) {
		summary.ToolWire = classifyToolWire(rawTools)
	}
	summary.ToolChoice = classifyToolChoice(body["tool_choice"])
	if rawParallel, ok := body["parallel_tool_calls"]; ok && rawJSONPresent(rawParallel) {
		summary.ParallelTools = strings.TrimSpace(string(rawParallel))
	}
	summary.HasMetadata = rawJSONPresent(body["metadata"])
	summary.HasUser = rawJSONPresent(body["user"])

	if rawMessages, ok := body["messages"]; ok {
		var messages []map[string]json.RawMessage
		if err := json.Unmarshal(rawMessages, &messages); err == nil {
			for _, message := range messages {
				role := jsonString(message["role"])
				switch role {
				case "assistant":
					if rawJSONPresent(message["tool_calls"]) {
						summary.AssistantTools++
					}
				case "tool":
					summary.ToolMessages++
				}
			}
		}
	}
	return summary
}

func classifyToolWire(raw json.RawMessage) string {
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return toolWireNone
	}
	if len(tools) == 0 {
		return toolWireNone
	}

	nested := 0
	flat := 0
	for _, tool := range tools {
		switch toolShape(tool) {
		case toolWireNested:
			nested++
		case toolWireFlat:
			flat++
		}
	}
	switch {
	case nested > 0 && flat > 0:
		return toolWireMixed
	case flat > 0:
		return toolWireFlat
	case nested > 0:
		return toolWireNested
	default:
		return toolWireNone
	}
}

func toolShape(tool map[string]json.RawMessage) string {
	if rawJSONPresent(tool["function"]) {
		return toolWireNested
	}
	if strings.TrimSpace(string(tool["type"])) == "function" && rawJSONPresent(tool["name"]) {
		return toolWireFlat
	}
	if rawJSONPresent(tool["name"]) {
		return toolWireFlat
	}
	return toolWireNone
}

func classifyToolChoice(raw json.RawMessage) string {
	if !rawJSONPresent(raw) {
		return "absent"
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == `"auto"` || trimmed == `"none"` || trimmed == `"required"` {
		return trimmed
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return "unknown"
	}
	if rawJSONPresent(object["function"]) {
		return "nested_function"
	}
	if rawJSONPresent(object["name"]) {
		return "flat_function"
	}
	if kind := strings.TrimSpace(string(object["type"])); kind != "" {
		return "type=" + kind
	}
	return "object"
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return value
}

func (s cursorWireSummary) logFields() string {
	return fmt.Sprintf(
		"tool_wire=%s tool_choice=%s parallel_tool_calls=%s has_metadata=%t has_user=%t assistant_tool_messages=%d tool_result_messages=%d",
		s.ToolWire,
		s.ToolChoice,
		s.ParallelTools,
		s.HasMetadata,
		s.HasUser,
		s.AssistantTools,
		s.ToolMessages,
	)
}

func (s streamResponseShape) logFields() string {
	return fmt.Sprintf(
		"response_content=%t response_reasoning_content=%t response_tool_calls=%t",
		s.Content,
		s.ReasoningContent,
		s.ToolCalls,
	)
}
