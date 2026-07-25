package codex

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/openai"
)

func TestBuildFaithfulRequestUsesProfileScaffoldAndTurnShape(t *testing.T) {
	builder := fixtureBuilder()

	payload := builder.buildFaithful([]openai.ChatMessage{
		{Role: "system", Content: openai.TextContent("system rules")},
		{Role: "user", Content: openai.TextContent("hello")},
		{Role: "assistant", Content: openai.TextContent("hi")},
	}, "", "session-123", requestKindTurn, "high", "low")

	if payload["type"] != "response.create" {
		t.Fatalf("type = %v", payload["type"])
	}
	if payload["model"] != "fixture-model" {
		t.Fatalf("model = %v", payload["model"])
	}
	if payload["instructions"] != "fixture instructions" {
		t.Fatalf("instructions = %v", payload["instructions"])
	}
	if _, ok := payload["generate"]; ok {
		t.Fatal("turn payload unexpectedly includes generate")
	}
	if payload["prompt_cache_key"] != "session-123" {
		t.Fatalf("prompt_cache_key = %v", payload["prompt_cache_key"])
	}

	input := payload["input"].([]any)
	if len(input) != 5 {
		t.Fatalf("input len = %d, want developer + system + env + 2 messages", len(input))
	}
	if input[0].(map[string]any)["role"] != "developer" {
		t.Fatalf("developer item = %#v", input[0])
	}
	system := input[1].(map[string]any)
	if system["role"] != "developer" {
		t.Fatalf("system role = %v", system["role"])
	}
	if got := system["content"].([]any)[0].(map[string]any)["text"]; got != "system rules" {
		t.Fatalf("system text = %v", got)
	}
	envText := input[2].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	for _, want := range []string{
		"<cwd>/tmp/work</cwd>",
		"<current_date>2026-06-26</current_date>",
		"<timezone>PDT</timezone>",
	} {
		if !strings.Contains(envText, want) {
			t.Fatalf("environment context missing %q in %q", want, envText)
		}
	}
	if got := input[3].(map[string]any)["content"].([]any)[0].(map[string]any)["type"]; got != "input_text" {
		t.Fatalf("user part type = %v", got)
	}
	if got := input[4].(map[string]any)["content"].([]any)[0].(map[string]any)["type"]; got != "output_text" {
		t.Fatalf("assistant part type = %v", got)
	}

	metadata := payload["client_metadata"].(map[string]any)
	if metadata["session_id"] != "session-123" || metadata["thread_id"] != "session-123" {
		t.Fatalf("metadata session/thread = %#v", metadata)
	}
	if metadata["turn_id"] != "turn-123" {
		t.Fatalf("turn_id = %v", metadata["turn_id"])
	}
	if metadata["x-codex-installation-id"] != "install-123" {
		t.Fatalf("installation id = %v", metadata["x-codex-installation-id"])
	}
	if !strings.Contains(metadata["x-codex-turn-metadata"].(string), `"request_kind": "turn"`) {
		t.Fatalf("turn metadata = %v", metadata["x-codex-turn-metadata"])
	}
}

func TestBuildFaithfulPrewarmRequestHasGenerateFalseAndEmptyInput(t *testing.T) {
	builder := fixtureBuilder()

	payload := builder.buildFaithful(nil, "override-model", "session-123", requestKindPrewarm, "medium", "medium")

	if payload["model"] != "override-model" {
		t.Fatalf("model = %v", payload["model"])
	}
	if payload["generate"] != false {
		t.Fatalf("generate = %v, want false", payload["generate"])
	}
	if input := payload["input"].([]any); len(input) != 0 {
		t.Fatalf("input len = %d, want 0", len(input))
	}
	metadata := payload["client_metadata"].(map[string]any)
	if metadata["turn_id"] != "" {
		t.Fatalf("prewarm turn_id = %v, want empty", metadata["turn_id"])
	}
	if !strings.Contains(metadata["x-codex-turn-metadata"].(string), `"request_kind": "prewarm"`) {
		t.Fatalf("turn metadata = %v", metadata["x-codex-turn-metadata"])
	}
}

func TestBuildMinimalRequest(t *testing.T) {
	builder := fixtureBuilder()
	payload, err := builder.buildMinimal(Request{
		Model:           "plain-model",
		ReasoningEffort: "low",
		Verbosity:       "high",
		Messages: []openai.ChatMessage{
			{Role: "system", Content: openai.TextContent("one")},
			{Role: "system", Content: openai.TextContent("two")},
			{Role: "user", Content: []byte(`[{"type":"text","text":"hello "},{"type":"text","text":"there"}]`)},
		},
	})
	if err != nil {
		t.Fatalf("buildMinimal() error = %v", err)
	}

	if payload["model"] != "plain-model" {
		t.Fatalf("model = %v", payload["model"])
	}
	if payload["instructions"] != "one\n\ntwo" {
		t.Fatalf("instructions = %q", payload["instructions"])
	}
	if payload["prompt_cache_key"] != "cache-123" {
		t.Fatalf("prompt_cache_key = %v", payload["prompt_cache_key"])
	}
	if _, ok := payload["tools"]; ok {
		t.Fatal("minimal payload unexpectedly includes tools")
	}
	input := payload["input"].([]any)
	if got := input[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]; got != "hello there" {
		t.Fatalf("message text = %v", got)
	}
}

func TestBuildMinimalStructuredRequest(t *testing.T) {
	builder := fixtureBuilder()
	payload, err := builder.buildMinimal(Request{
		Model:              "plain-model",
		ReasoningEffort:    "low",
		Verbosity:          "low",
		Messages:           []openai.ChatMessage{{Role: "user", Content: openai.TextContent("extract")}},
		ResponseSchemaName: "summary",
		ResponseSchema:     json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}`),
		MaxOutputTokens:    321,
	})
	if err != nil {
		t.Fatalf("buildMinimal() error = %v", err)
	}
	if payload["max_output_tokens"] != 321 {
		t.Fatalf("max_output_tokens = %#v", payload["max_output_tokens"])
	}
	text := payload["text"].(map[string]any)
	format := text["format"].(map[string]any)
	if format["type"] != "json_schema" || format["name"] != "summary" || format["strict"] != true {
		t.Fatalf("format = %#v", format)
	}
	if _, ok := payload["tools"]; ok {
		t.Fatal("structured minimal payload unexpectedly has tools")
	}
	if _, ok := payload["client_metadata"]; ok {
		t.Fatal("structured minimal payload unexpectedly has coding metadata")
	}
}

func TestBuildMinimalRequestIncludesClientTools(t *testing.T) {
	builder := fixtureBuilder()
	parallel := false
	payload, err := builder.buildMinimal(Request{
		Model:             "plain-model",
		ReasoningEffort:   "medium",
		Verbosity:         "medium",
		Messages:          []openai.ChatMessage{{Role: "user", Content: openai.TextContent("use a tool")}},
		Tools:             json.RawMessage(`[{"type":"function","function":{"name":"lookup","description":"Look up a term","parameters":{"type":"object","properties":{"q":{"type":"string","description":"query"}},"required":["q"]}}}]`),
		ToolChoice:        json.RawMessage(`{"type":"function","function":{"name":"lookup"}}`),
		ParallelToolCalls: &parallel,
	})
	if err != nil {
		t.Fatalf("buildMinimal() error = %v", err)
	}

	// Codex uses the Responses API tool format: function fields are flattened
	// to the top level rather than nested under a "function" object.
	tools := payload["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" ||
		tool["name"] != "lookup" ||
		tool["description"] != "Look up a term" {
		t.Fatalf("tool schema = %#v", tool)
	}
	if _, nested := tool["function"]; nested {
		t.Fatalf("tool must not keep nested function object: %#v", tool)
	}
	parameters := tool["parameters"].(map[string]any)
	properties := parameters["properties"].(map[string]any)
	q := properties["q"].(map[string]any)
	if parameters["type"] != "object" || q["type"] != "string" || q["description"] != "query" {
		t.Fatalf("parameters = %#v", parameters)
	}
	toolChoice := payload["tool_choice"].(map[string]any)
	if toolChoice["type"] != "function" || toolChoice["name"] != "lookup" {
		t.Fatalf("tool_choice = %#v", toolChoice)
	}
	if _, nested := toolChoice["function"]; nested {
		t.Fatalf("tool_choice must not keep nested function object: %#v", toolChoice)
	}
	if payload["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls = %v, want false", payload["parallel_tool_calls"])
	}
}

func TestNormalizeCallID(t *testing.T) {
	short := "call_MeAziM96Zep7N4zG0mWQnUjF"
	if got := normalizeCallID(short); got != short {
		t.Fatalf("short id changed: %q", got)
	}
	long := strings.Repeat("x", 94)
	got := normalizeCallID(long)
	if len(got) != 64 {
		t.Fatalf("normalized length = %d, want 64", len(got))
	}
	if got != normalizeCallID(long) {
		t.Fatalf("normalizeCallID not deterministic")
	}
}

func TestBuildMinimalRequestPassesThroughFlatTools(t *testing.T) {
	builder := fixtureBuilder()
	payload, err := builder.buildMinimal(Request{
		Model:           "plain-model",
		ReasoningEffort: "medium",
		Verbosity:       "medium",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.TextContent("use a tool")}},
		Tools:           json.RawMessage(`[{"type":"function","name":"lookup","description":"flat","parameters":{"type":"object","properties":{}}}]`),
		ToolChoice:      json.RawMessage(`"auto"`),
	})
	if err != nil {
		t.Fatalf("buildMinimal() error = %v", err)
	}
	tool := payload["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "lookup" || tool["description"] != "flat" {
		t.Fatalf("flat tool altered: %#v", tool)
	}
	if payload["tool_choice"] != "auto" {
		t.Fatalf("string tool_choice = %#v, want \"auto\"", payload["tool_choice"])
	}
}

func TestBuildMinimalRequestIncludesToolResultContinuation(t *testing.T) {
	builder := fixtureBuilder()
	payload, err := builder.buildMinimal(Request{
		Model:           "plain-model",
		ReasoningEffort: "medium",
		Verbosity:       "medium",
		Messages: []openai.ChatMessage{
			{Role: "user", Content: openai.TextContent("read go.mod")},
			{
				Role:    "assistant",
				Content: json.RawMessage("null"),
				ToolCalls: []openai.ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: openai.ToolCallFunction{
							Name:      "read_file",
							Arguments: `{"path":"go.mod"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_123", Content: openai.TextContent("module github.com/teslashibe/codex-chat-api")},
		},
		Tools: json.RawMessage(`[{"type":"function","function":{"name":"read_file"}}]`),
	})
	if err != nil {
		t.Fatalf("buildMinimal() error = %v", err)
	}

	input := payload["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input len = %d, want user + function_call + output", len(input))
	}
	call := input[1].(map[string]any)
	if call["type"] != "function_call" ||
		call["call_id"] != "call_123" ||
		call["name"] != "read_file" ||
		call["arguments"] != `{"path":"go.mod"}` {
		t.Fatalf("function call item = %#v", call)
	}
	output := input[2].(map[string]any)
	if output["type"] != "function_call_output" ||
		output["call_id"] != "call_123" ||
		output["output"] != "module github.com/teslashibe/codex-chat-api" {
		t.Fatalf("function output item = %#v", output)
	}
}

func TestBuildMinimalRequestIncludesSequentialToolResults(t *testing.T) {
	builder := fixtureBuilder()
	payload, err := builder.buildMinimal(Request{
		Model:           "plain-model",
		ReasoningEffort: "medium",
		Verbosity:       "medium",
		Messages: []openai.ChatMessage{
			{Role: "user", Content: openai.TextContent("list files then read go.mod")},
			{
				Role:    "assistant",
				Content: json.RawMessage("null"),
				ToolCalls: []openai.ToolCall{
					{
						ID:   "call_list",
						Type: "function",
						Function: openai.ToolCallFunction{
							Name:      "list_dir",
							Arguments: `{"path":"."}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_list", Content: openai.TextContent("README.md\ngo.mod")},
			{
				Role:    "assistant",
				Content: json.RawMessage("null"),
				ToolCalls: []openai.ToolCall{
					{
						ID:   "call_read",
						Type: "function",
						Function: openai.ToolCallFunction{
							Name:      "read_file",
							Arguments: `{"path":"go.mod"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_read", Content: openai.TextContent("module github.com/teslashibe/codex-chat-api")},
		},
		Tools: json.RawMessage(`[{"type":"function","function":{"name":"list_dir"}},{"type":"function","function":{"name":"read_file"}}]`),
	})
	if err != nil {
		t.Fatalf("buildMinimal() error = %v", err)
	}

	input := payload["input"].([]any)
	if len(input) != 5 {
		t.Fatalf("input len = %d, want user + two call/output pairs", len(input))
	}
	want := []struct {
		index  int
		typ    string
		callID string
	}{
		{index: 1, typ: "function_call", callID: "call_list"},
		{index: 2, typ: "function_call_output", callID: "call_list"},
		{index: 3, typ: "function_call", callID: "call_read"},
		{index: 4, typ: "function_call_output", callID: "call_read"},
	}
	for _, tc := range want {
		item := input[tc.index].(map[string]any)
		if item["type"] != tc.typ || item["call_id"] != tc.callID {
			t.Fatalf("input[%d] = %#v, want type %s call_id %s", tc.index, item, tc.typ, tc.callID)
		}
	}
}

func fixtureBuilder() requestBuilder {
	builder := newRequestBuilder(Profile{
		Model:             "fixture-model",
		Instructions:      "fixture instructions",
		Tools:             []any{map[string]any{"type": "function", "name": "tool"}},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Include:           []any{"reasoning.encrypted_content"},
	}, Scaffold{
		DeveloperItem: map[string]any{
			"type":    "message",
			"role":    "developer",
			"content": []any{map[string]any{"type": "input_text", "text": "developer fixture"}},
		},
		EnvironmentContext: "<environment_context><cwd>old</cwd><current_date>old</current_date><timezone>old</timezone></environment_context>",
	}, "")
	builder.now = func() time.Time {
		return time.Date(2026, 6, 26, 12, 0, 0, 0, time.FixedZone("PDT", -7*60*60))
	}
	builder.cwd = func() string { return "/tmp/work" }
	builder.newTurnID = func() string { return "turn-123" }
	builder.newPromptCache = func() string { return "cache-123" }
	builder.installationID = func() string { return "install-123" }
	return builder
}
