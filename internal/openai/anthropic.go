package openai

import (
	"encoding/json"
	"strings"
)

// NormalizeAnthropicDialect rewrites the Anthropic Messages shapes that Cursor
// sends to this Chat Completions endpoint (for models whose name contains
// "claude") into canonical Chat Completions shapes, in place:
//
//   - tools {name, description, input_schema} become function tools; Anthropic
//     server tools (web_search_*, bash_*, …) are dropped;
//   - tool_choice {type: auto|any|none|tool} becomes "auto"/"required"/"none"/
//     a forced function, and disable_parallel_tool_use sets
//     parallel_tool_calls=false;
//   - assistant tool_use blocks become tool_calls, user tool_result blocks
//     become role:"tool" messages (placed before the rest of that user turn),
//     image blocks become image_url parts, thinking blocks and cache_control
//     are dropped.
//
// The top-level system field is handled by WithSystemMessage. Requests that
// are already in Chat Completions shape are left untouched, so the function
// is idempotent. It reports whether anything changed.
func NormalizeAnthropicDialect(req *ChatCompletionRequest) bool {
	changed := false
	if tools, ok := normalizeAnthropicTools(req.Tools); ok {
		req.Tools = tools
		changed = true
	}
	if choice, serial, ok := normalizeAnthropicToolChoice(req.ToolChoice); ok {
		req.ToolChoice = choice
		if serial && req.ParallelToolCalls == nil {
			off := false
			req.ParallelToolCalls = &off
		}
		changed = true
	}
	var messages []ChatMessage
	messagesChanged := false
	for _, msg := range req.Messages {
		converted, ok := normalizeAnthropicMessage(msg)
		if ok {
			messagesChanged = true
		}
		messages = append(messages, converted...)
	}
	if messagesChanged {
		req.Messages = messages
		changed = true
	}
	return changed
}

func normalizeAnthropicTools(raw json.RawMessage) (json.RawMessage, bool) {
	if !rawPresent(raw) {
		return nil, false
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, false
	}
	anthropic := false
	for _, tool := range tools {
		if _, ok := tool["input_schema"]; ok {
			if _, hasFunction := tool["function"]; !hasFunction {
				anthropic = true
			}
		}
	}
	if !anthropic {
		return nil, false
	}
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		if _, ok := tool["function"]; ok {
			out = append(out, tool)
			continue
		}
		schema, hasSchema := tool["input_schema"]
		if !hasSchema {
			// Anthropic server tools carry a versioned type and no schema;
			// they cannot be executed by the client through this endpoint.
			continue
		}
		var name, description string
		_ = json.Unmarshal(tool["name"], &name)
		_ = json.Unmarshal(tool["description"], &description)
		if name == "" {
			continue
		}
		function := map[string]any{"name": name, "parameters": schema}
		if description != "" {
			function["description"] = description
		}
		out = append(out, map[string]any{"type": "function", "function": function})
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return data, true
}

func normalizeAnthropicToolChoice(raw json.RawMessage) (json.RawMessage, bool, bool) {
	if !rawPresent(raw) {
		return nil, false, false
	}
	var choice struct {
		Type                   string          `json:"type"`
		Name                   string          `json:"name"`
		Function               json.RawMessage `json:"function"`
		DisableParallelToolUse bool            `json:"disable_parallel_tool_use"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil || rawPresent(choice.Function) {
		return nil, false, false
	}
	switch choice.Type {
	case "auto":
		return json.RawMessage(`"auto"`), choice.DisableParallelToolUse, true
	case "any":
		return json.RawMessage(`"required"`), choice.DisableParallelToolUse, true
	case "none":
		return json.RawMessage(`"none"`), false, true
	case "tool":
		if choice.Name == "" {
			return nil, false, false
		}
		data, _ := json.Marshal(map[string]any{"type": "function", "function": map[string]string{"name": choice.Name}})
		return data, choice.DisableParallelToolUse, true
	}
	return nil, false, false
}

// normalizeAnthropicMessage converts one message; ok is false when the
// message has no Anthropic-only blocks and is returned unchanged.
func normalizeAnthropicMessage(msg ChatMessage) ([]ChatMessage, bool) {
	var blocks []map[string]any
	if len(msg.Content) == 0 || msg.Content[0] != '[' || json.Unmarshal(msg.Content, &blocks) != nil || !hasAnthropicBlock(blocks) {
		return []ChatMessage{msg}, false
	}
	switch msg.Role {
	case "assistant":
		var texts []string
		out := msg
		for _, b := range blocks {
			switch b["type"] {
			case "text":
				if text, _ := b["text"].(string); strings.TrimSpace(text) != "" {
					texts = append(texts, text)
				}
			case "tool_use":
				id, _ := b["id"].(string)
				name, _ := b["name"].(string)
				out.ToolCalls = append(out.ToolCalls, ToolCall{
					ID:       id,
					Type:     "function",
					Function: ToolCallFunction{Name: name, Arguments: toolUseArguments(b["input"])},
				})
			}
			// thinking / redacted_thinking and anything else are dropped.
		}
		out.Content = nil
		if len(texts) > 0 {
			out.Content = TextContent(strings.Join(texts, "\n\n"))
		}
		if out.Content == nil && len(out.ToolCalls) == 0 {
			// Only thinking blocks: nothing a provider can replay.
			return nil, true
		}
		return []ChatMessage{out}, true
	default:
		var results []ChatMessage
		var parts []any
		for _, b := range blocks {
			switch b["type"] {
			case "tool_result":
				id, _ := b["tool_use_id"].(string)
				isError, _ := b["is_error"].(bool)
				results = append(results, ChatMessage{Role: "tool", ToolCallID: id, Content: toolResultContent(b["content"], isError)})
			case "text":
				if text, _ := b["text"].(string); strings.TrimSpace(text) != "" {
					parts = append(parts, map[string]any{"type": "text", "text": text})
				}
			case "image":
				if part := imagePart(b); part != nil {
					parts = append(parts, part)
				}
			case "document":
				parts = append(parts, map[string]any{"type": "text", "text": documentText(b)})
			}
		}
		out := results
		if len(parts) > 0 {
			content, _ := json.Marshal(parts)
			rest := msg
			rest.Content = content
			out = append(out, rest)
		}
		return out, true
	}
}

func hasAnthropicBlock(blocks []map[string]any) bool {
	for _, b := range blocks {
		switch b["type"] {
		case "tool_use", "tool_result", "thinking", "redacted_thinking", "document":
			return true
		case "image":
			if _, ok := b["source"]; ok {
				return true
			}
		}
		if _, ok := b["cache_control"]; ok {
			return true
		}
	}
	return false
}

func toolUseArguments(input any) string {
	switch v := input.(type) {
	case nil:
		return "{}"
	case string:
		// Some clients send the input pre-encoded.
		if json.Valid([]byte(v)) {
			return v
		}
		data, _ := json.Marshal(map[string]string{"input": v})
		return string(data)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return "{}"
		}
		return string(data)
	}
}

// toolResultContent keeps text as a plain string, or an OpenAI parts array
// when the result carries images (e.g. Cursor reading a PNG).
func toolResultContent(content any, isError bool) json.RawMessage {
	prefix := ""
	if isError {
		prefix = "Error: "
	}
	items, ok := content.([]any)
	if !ok {
		text, _ := content.(string)
		return TextContent(prefix + text)
	}
	var texts []string
	var parts []any
	hasImage := false
	for _, item := range items {
		b, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch b["type"] {
		case "text":
			if text, _ := b["text"].(string); text != "" {
				texts = append(texts, text)
				parts = append(parts, map[string]any{"type": "text", "text": text})
			}
		case "image":
			if part := imagePart(b); part != nil {
				parts = append(parts, part)
				hasImage = true
			}
		}
	}
	if !hasImage {
		return TextContent(prefix + strings.Join(texts, "\n"))
	}
	if prefix != "" {
		parts = append([]any{map[string]any{"type": "text", "text": strings.TrimSpace(prefix)}}, parts...)
	}
	data, _ := json.Marshal(parts)
	return data
}

func imagePart(b map[string]any) map[string]any {
	source, ok := b["source"].(map[string]any)
	if !ok {
		return nil
	}
	switch source["type"] {
	case "base64":
		mediaType, _ := source["media_type"].(string)
		data, _ := source["data"].(string)
		if mediaType == "" || data == "" {
			return nil
		}
		return map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + mediaType + ";base64," + data}}
	case "url":
		url, _ := source["url"].(string)
		if url == "" {
			return nil
		}
		return map[string]any{"type": "image_url", "image_url": map[string]string{"url": url}}
	}
	return nil
}

func documentText(b map[string]any) string {
	if source, ok := b["source"].(map[string]any); ok && source["type"] == "text" {
		if data, _ := source["data"].(string); data != "" {
			return data
		}
	}
	title, _ := b["title"].(string)
	if title != "" {
		return "[document omitted: " + title + "]"
	}
	return "[document omitted]"
}

func rawPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}
