package server

import (
	"strings"
	"testing"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

func TestDegenerateAgentTurn(t *testing.T) {
	if !degenerateAgentTurn(true, "stop", 200, 0) {
		t.Fatal("expected degenerate agent turn")
	}
	if degenerateAgentTurn(true, "tool_calls", 200, 1) {
		t.Fatal("tool_calls finish should not be degenerate")
	}
	if degenerateAgentTurn(false, "stop", 200, 0) {
		t.Fatal("plain chat should not be degenerate")
	}
	if !degenerateAgentTurn(true, "stop", 14, 0) {
		t.Fatal("short text-only stop with tools should be degenerate")
	}
}

func TestShouldRetryDegenerateTurn(t *testing.T) {
	msgs := []openai.ChatMessage{{Role: "user", Content: openai.TextContent("go")}}
	if shouldRetryDegenerateTurn(true, true, msgs, 0, "done", 4) {
		t.Fatal("should not retry plain user answer without loop phrase")
	}
	if !shouldRetryDegenerateTurn(true, true, msgs, 0, "I'll inspect the repo now.", 26) {
		t.Fatal("expected retry for user turn with loop phrase")
	}
	if shouldRetryDegenerateTurn(true, true, msgs, 1, "done", 4) {
		t.Fatal("should not retry when tool calls present")
	}
	msgs[len(msgs)-1].Role = "tool"
	if shouldRetryDegenerateTurn(true, true, msgs, 0, "go.mod declares module.", 24) {
		t.Fatal("should not retry short final answer after tool result")
	}
	if !shouldRetryDegenerateTurn(true, true, msgs, 0, "I'll inspect the repo now.", 26) {
		t.Fatal("expected retry for tool continuation with loop phrase")
	}
	if shouldRetryDegenerateTurn(true, true, msgs, 0, strings.Repeat("x", 600), 600) {
		t.Fatal("should not retry long final answer after tool result")
	}
	msgs[len(msgs)-1].Role = "assistant"
	if shouldRetryDegenerateTurn(true, true, msgs, 0, "done", 4) {
		t.Fatal("should not retry when last message is assistant")
	}
}

func TestAgentTurnExpectsToolCalls(t *testing.T) {
	msgs := []openai.ChatMessage{{Role: "user", Content: openai.TextContent("go")}}
	if !agentTurnExpectsToolCalls(msgs, true) {
		t.Fatal("expected agent turn for user message with tools")
	}
	if !agentTurnStreamsReasoning(msgs) {
		t.Fatal("expected user agent turn to stream reasoning")
	}
	msgs[len(msgs)-1].Role = "tool"
	if !agentTurnExpectsToolCalls(msgs, true) {
		t.Fatal("expected agent turn for tool continuation with tools")
	}
	if agentTurnStreamsReasoning(msgs) {
		t.Fatal("tool continuation should not stream reasoning")
	}
	msgs[len(msgs)-1].Role = "assistant"
	if agentTurnExpectsToolCalls(msgs, true) {
		t.Fatal("should not treat assistant continuation as agent turn")
	}
	if agentTurnExpectsToolCalls(msgs, false) {
		t.Fatal("should not treat plain chat as agent turn")
	}
}

func TestBuildDegenerateRetryRequest(t *testing.T) {
	req := codex.Request{
		Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}},
	}
	retry := buildDegenerateRetryRequest(req)
	if string(retry.ToolChoice) != `"required"` {
		t.Fatalf("ToolChoice = %s, want required", retry.ToolChoice)
	}
	if len(retry.Messages) != 2 || retry.Messages[0].Role != "system" {
		t.Fatalf("retry messages = %#v", retry.Messages)
	}
}

func TestDetectLoopPhrase(t *testing.T) {
	if got := detectLoopPhrase("I'll implement that now."); got != "i'll" {
		t.Fatalf("detectLoopPhrase() = %q, want i'll", got)
	}
	if got := detectLoopPhrase("done"); got != "" {
		t.Fatalf("detectLoopPhrase() = %q, want empty", got)
	}
}

func TestShouldRetryDegenerateTurnIgnoresFinalAnswers(t *testing.T) {
	for _, role := range []string{"user", "tool"} {
		msgs := []openai.ChatMessage{{Role: role, Content: openai.TextContent("go")}}
		for _, text := range []string{
			"Updated the config and ran the tests. Let me know if you need anything else.",
			"The bug was an off-by-one in the loop bound, which I fixed in parser.go. I'll note that the tests still pass.",
			"Summary:\n" + strings.Repeat("- changed a file\n", 60) + "I'll keep an eye on it.",
		} {
			if shouldRetryDegenerateTurn(true, true, msgs, 0, text, len(text)) {
				t.Fatalf("role %s: final answer must not retry: %q", role, text)
			}
		}
		if !shouldRetryDegenerateTurn(true, true, msgs, 0, "Let me look at the failing test first.", 38) {
			t.Fatalf("role %s: short announcement should retry", role)
		}
	}
}
