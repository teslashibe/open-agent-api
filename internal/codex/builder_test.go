package codex

import (
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
	payload := builder.buildMinimal([]openai.ChatMessage{
		{Role: "system", Content: openai.TextContent("one")},
		{Role: "system", Content: openai.TextContent("two")},
		{Role: "user", Content: []byte(`[{"type":"text","text":"hello "},{"type":"text","text":"there"}]`)},
	}, "plain-model", "low", "high")

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
