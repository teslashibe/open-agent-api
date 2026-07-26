package codex

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/teslashibe/open-chat-api/internal/openai"
)

type codexEvent struct {
	Type           string          `json:"type"`
	Delta          string          `json:"delta"`
	Input          string          `json:"input"`
	Arguments      string          `json:"arguments"`
	ArgumentsDelta string          `json:"arguments_delta"`
	ItemID         string          `json:"item_id"`
	OutputIndex    int             `json:"output_index"`
	ToolCalls      []ToolCall      `json:"tool_calls"`
	ToolCallDelta  *ToolCallDelta  `json:"tool_call_delta"`
	Item           *codexToolItem  `json:"item"`
	Output         *codexToolItem  `json:"output"`
	ToolCall       *codexToolItem  `json:"tool_call"`
	Function       *codexFunction  `json:"function"`
	Status         int             `json:"status"`
	Error          json.RawMessage `json:"error"`
	Response       *codexResponse  `json:"response"`
}

type codexResponse struct {
	ID    string     `json:"id"`
	Model string     `json:"model"`
	Usage codexUsage `json:"usage"`
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type codexToolItem struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	Function  *codexFunction  `json:"function"`
	Raw       json.RawMessage `json:"-"`
}

type codexFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func parseStreamEvent(raw []byte) (StreamEvent, bool, error) {
	var event codexEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return StreamEvent{}, false, NewError(ErrorKindUpstream, http.StatusBadGateway, "invalid codex websocket frame", err)
	}

	switch event.Type {
	case "response.output_text.delta":
		return StreamEvent{Delta: event.Delta}, false, nil
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return StreamEvent{ReasoningDelta: event.Delta}, false, nil
	case "response.output_item.added", "response.tool_call.created", "response.tool_call.start", "response.function_call.started":
		if delta, ok := event.toolCallStartDelta(); ok {
			return StreamEvent{ToolCallDelta: &delta}, false, nil
		}
		return StreamEvent{}, false, nil
	case "response.function_call_arguments.delta", "response.tool_call.arguments.delta", "response.tool_call.delta":
		if event.ToolCallDelta != nil {
			return StreamEvent{ToolCallDelta: event.ToolCallDelta}, false, nil
		}
		if len(event.ToolCalls) > 0 {
			return StreamEvent{ToolCalls: event.ToolCalls}, false, nil
		}
		if delta, ok := event.toolCallArgumentsDelta(); ok {
			return StreamEvent{ToolCallDelta: &delta}, false, nil
		}
		return StreamEvent{}, false, nil
	case "response.tool_call.completed", "response.tool_call.done", "response.function_call.completed":
		if len(event.ToolCalls) > 0 {
			return StreamEvent{ToolCalls: event.ToolCalls}, false, nil
		}
		if toolCall, ok := event.fullToolCall(); ok {
			return StreamEvent{ToolCalls: []ToolCall{toolCall}}, false, nil
		}
		return StreamEvent{}, false, nil
	case "response.function_call_arguments.done":
		if delta, ok := event.toolCallArgumentsDoneDelta(); ok {
			return StreamEvent{ToolCallDelta: &delta}, false, nil
		}
		return StreamEvent{}, false, nil
	case "response.custom_tool_call_input.delta":
		if delta, ok := event.customToolCallInputDelta(false); ok {
			return StreamEvent{ToolCallDelta: &delta}, false, nil
		}
		return StreamEvent{}, false, nil
	case "response.custom_tool_call_input.done":
		if delta, ok := event.customToolCallInputDelta(true); ok {
			return StreamEvent{ToolCallDelta: &delta}, false, nil
		}
		return StreamEvent{}, false, nil
	case "response.output_item.done":
		if toolCall, ok := event.fullToolCall(); ok {
			return StreamEvent{ToolCalls: []ToolCall{toolCall}}, false, nil
		}
		return StreamEvent{}, false, nil
	case "response.completed":
		done := StreamEvent{Done: true}
		if event.Response != nil {
			done.ID = event.Response.ID
			done.Model = event.Response.Model
			done.Usage = mapUsage(event.Response.Usage)
		}
		return done, true, nil
	case "response.failed", "error":
		status := event.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		detail := strings.TrimSpace(string(event.Error))
		if detail == "" || detail == "null" {
			detail = fmt.Sprintf("codex event type %s", event.Type)
		}
		if strings.Contains(detail, "usage_limit_reached") {
			retryAfter, resetAt := retryHintFromJSON(raw)
			err := NewError(ErrorKindUpstream, http.StatusTooManyRequests, "usage limit reached", fmt.Errorf("%w: codex %s: %s", ErrUsageLimitReached, event.Type, detail))
			return StreamEvent{}, true, withRetryHint(err, retryAfter, resetAt)
		}
		if strings.Contains(detail, "context_length_exceeded") {
			return StreamEvent{}, true, NewError(ErrorKindClient, http.StatusBadRequest, "conversation exceeds the model's context window", fmt.Errorf("%w: codex %s: %s", ErrContextWindowExceeded, event.Type, detail))
		}
		retryAfter, resetAt := retryHintFromJSON(raw)
		err := NewError(ErrorKindUpstream, status, "codex backend error", fmt.Errorf("codex %s: %s", event.Type, detail))
		return StreamEvent{}, true, withRetryHint(err, retryAfter, resetAt)
	default:
		return StreamEvent{}, false, nil
	}
}

func (e codexEvent) toolCallStartDelta() (ToolCallDelta, bool) {
	item := e.toolItem()
	if item == nil || !item.isToolCall() {
		return ToolCallDelta{}, false
	}
	id := firstNonEmpty(item.CallID, item.ID, e.ItemID)
	name := item.toolName()
	if id == "" && name == "" {
		return ToolCallDelta{}, false
	}
	return ToolCallDelta{
		Index: e.OutputIndex,
		ID:    id,
		Type:  item.toolCallType(),
		Function: ToolCallFunctionDelta{
			Name: name,
		},
	}, true
}

func (e codexEvent) toolCallArgumentsDelta() (ToolCallDelta, bool) {
	arguments := firstNonEmpty(e.Delta, e.ArgumentsDelta, e.Arguments)
	if arguments == "" {
		return ToolCallDelta{}, false
	}
	item := e.toolItem()
	id := ""
	name := ""
	if item != nil {
		id = firstNonEmpty(item.CallID, item.ID)
		name = item.toolName()
	}
	return ToolCallDelta{
		Index: e.OutputIndex,
		ID:    id,
		Type:  "function",
		Function: ToolCallFunctionDelta{
			Name:      name,
			Arguments: arguments,
		},
	}, true
}

func (e codexEvent) toolCallArgumentsDoneDelta() (ToolCallDelta, bool) {
	arguments := firstNonEmpty(e.Arguments, e.ArgumentsDelta, e.Delta)
	if arguments == "" {
		return ToolCallDelta{}, false
	}
	item := e.toolItem()
	id := ""
	name := ""
	if item != nil {
		id = firstNonEmpty(item.CallID, item.ID, e.ItemID)
		name = item.toolName()
	}
	return ToolCallDelta{
		Index: e.OutputIndex,
		ID:    id,
		Type:  "function",
		Function: ToolCallFunctionDelta{
			Name:      name,
			Arguments: arguments,
		},
		Final: true,
	}, true
}

// customToolCallInputDelta maps Codex custom tool input frames (freeform text
// in delta/input fields) onto the internal tool-call delta representation.
func (e codexEvent) customToolCallInputDelta(final bool) (ToolCallDelta, bool) {
	arguments := e.Delta
	if final {
		arguments = firstNonEmpty(e.Input, e.Delta)
	}
	if arguments == "" {
		return ToolCallDelta{}, false
	}
	return ToolCallDelta{
		Index: e.OutputIndex,
		Type:  "custom",
		Function: ToolCallFunctionDelta{
			Arguments: arguments,
		},
		Final: final,
	}, true
}

func (e codexEvent) fullToolCall() (ToolCall, bool) {
	item := e.toolItem()
	if item == nil || !item.isToolCall() {
		return ToolCall{}, false
	}
	id := firstNonEmpty(item.CallID, item.ID, e.ItemID)
	name := item.toolName()
	arguments := item.toolArguments()
	if id == "" && name == "" && arguments == "" {
		return ToolCall{}, false
	}
	return ToolCall{
		ID:   id,
		Type: item.toolCallType(),
		Function: ToolCallFunction{
			Name:      name,
			Arguments: arguments,
		},
	}, true
}

func (e codexEvent) toolItem() *codexToolItem {
	for _, item := range []*codexToolItem{e.Item, e.Output, e.ToolCall} {
		if item != nil {
			return item
		}
	}
	if e.Function != nil {
		return &codexToolItem{Type: "function_call", ID: e.ItemID, Function: e.Function}
	}
	return nil
}

func (i codexToolItem) isToolCall() bool {
	switch i.Type {
	case "", "function_call", "tool_call", "function":
		return true
	default:
		return strings.Contains(i.Type, "function") || strings.Contains(i.Type, "tool")
	}
}

func (i codexToolItem) toolName() string {
	if i.Name != "" {
		return i.Name
	}
	if i.Function != nil {
		return i.Function.Name
	}
	return ""
}

func (i codexToolItem) toolArguments() string {
	if i.Arguments != "" {
		return i.Arguments
	}
	if i.Input != "" {
		return i.Input
	}
	if i.Function != nil {
		return i.Function.Arguments
	}
	return ""
}

func (i codexToolItem) toolCallType() string {
	if strings.Contains(i.Type, "custom") {
		return "custom"
	}
	return "function"
}

func isCodexToolEvent(raw []byte) bool {
	var event struct {
		Type          string          `json:"type"`
		ToolCalls     json.RawMessage `json:"tool_calls"`
		ToolCallDelta json.RawMessage `json:"tool_call_delta"`
		Item          json.RawMessage `json:"item"`
		Output        json.RawMessage `json:"output"`
		ToolCall      json.RawMessage `json:"tool_call"`
		Function      json.RawMessage `json:"function"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return false
	}
	if strings.Contains(event.Type, "tool_call") || strings.Contains(event.Type, "function_call") {
		return true
	}
	return len(event.ToolCalls) > 0 ||
		len(event.ToolCallDelta) > 0 ||
		toolJSONLooksLikeToolCall(event.Item) ||
		toolJSONLooksLikeToolCall(event.Output) ||
		toolJSONLooksLikeToolCall(event.ToolCall) ||
		len(event.Function) > 0
}

func redactedCodexToolEventShape(raw []byte) string {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return "valid_json=false"
	}
	eventType := jsonString(body["type"])
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	toolCallsCount := 0
	if rawToolCalls, ok := body["tool_calls"]; ok {
		var toolCalls []json.RawMessage
		if err := json.Unmarshal(rawToolCalls, &toolCalls); err == nil {
			toolCallsCount = len(toolCalls)
		}
	}
	return fmt.Sprintf(
		"valid_json=true type=%s fields=%s has_item=%t has_tool_call_delta=%t tool_calls_count=%d",
		eventType,
		strings.Join(keys, ","),
		rawJSONPresent(body["item"]) || rawJSONPresent(body["output"]) || rawJSONPresent(body["tool_call"]),
		rawJSONPresent(body["tool_call_delta"]),
		toolCallsCount,
	)
}

func jsonString(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

func toolJSONLooksLikeToolCall(raw json.RawMessage) bool {
	if !rawJSONPresent(raw) {
		return false
	}
	var item struct {
		Type     string          `json:"type"`
		CallID   string          `json:"call_id"`
		Name     string          `json:"name"`
		Function json.RawMessage `json:"function"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return false
	}
	return strings.Contains(item.Type, "function") ||
		strings.Contains(item.Type, "tool") ||
		item.CallID != "" ||
		item.Name != "" ||
		rawJSONPresent(item.Function)
}

func mapUsage(usage codexUsage) openai.Usage {
	return openai.Usage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.TotalTokens,
	}
}
