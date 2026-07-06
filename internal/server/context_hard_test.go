package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/teslashibe/codex-chat-api/internal/openai"
)

func hardTestMessage(role, text string) openai.ChatMessage {
	return openai.ChatMessage{Role: role, Content: openai.TextContent(text)}
}

func TestDropOldestToFitKeepsSystemAndRecent(t *testing.T) {
	big := strings.Repeat("x", 4096)
	messages := []openai.ChatMessage{hardTestMessage("system", "sys")}
	for i := 0; i < 40; i++ {
		messages = append(messages, hardTestMessage("user", big), hardTestMessage("assistant", big))
	}
	trimmed, dropped := dropOldestToFit(messages, 64*1024, hardContextProtectRecent)
	if dropped == 0 {
		t.Fatal("expected messages to be dropped")
	}
	data, _ := json.Marshal(trimmed)
	if len(data) > 64*1024 {
		t.Fatalf("trimmed size %d exceeds budget", len(data))
	}
	if trimmed[0].Role != "system" {
		t.Fatalf("system message not preserved: %q", trimmed[0].Role)
	}
	if len(trimmed) < hardContextProtectRecent {
		t.Fatalf("protected tail lost: %d messages", len(trimmed))
	}
}

func TestDropOldestToFitSkipsOrphanedToolResults(t *testing.T) {
	big := strings.Repeat("y", 8192)
	messages := []openai.ChatMessage{hardTestMessage("system", "sys")}
	for i := 0; i < 30; i++ {
		messages = append(messages,
			hardTestMessage("assistant", big),
			openai.ChatMessage{Role: "tool", ToolCallID: "call_1", Content: openai.TextContent(big)},
		)
	}
	trimmed, dropped := dropOldestToFit(messages, 48*1024, hardContextProtectRecent)
	if dropped == 0 {
		t.Fatal("expected messages to be dropped")
	}
	if trimmed[1].Role == "tool" {
		t.Fatal("kept window starts on an orphaned tool result")
	}
}

func TestDropOldestToFitNoopUnderBudget(t *testing.T) {
	messages := []openai.ChatMessage{hardTestMessage("user", "hi")}
	trimmed, dropped := dropOldestToFit(messages, 64*1024, hardContextProtectRecent)
	if dropped != 0 || len(trimmed) != 1 {
		t.Fatalf("unexpected trim: %d dropped", dropped)
	}
}
