package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/teslashibe/codex-chat-api/internal/config"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

func TestManageContextDisabledLeavesMessagesUnchanged(t *testing.T) {
	messages := []openai.ChatMessage{
		{Role: "user", Content: openai.TextContent("read")},
		{Role: "tool", ToolCallID: "call_1", Content: openai.TextContent(strings.Repeat("x", 100))},
	}
	cfg := config.Defaults()
	cfg.ContextManagementEnabled = false
	cfg.ContextToolOutputMaxBytes = 10

	result := manageContext(messages, cfg)
	if result.Changed {
		t.Fatal("Changed = true, want false")
	}
	if &result.Messages[0] != &messages[0] {
		t.Fatal("disabled context management should return original message slice")
	}
}

func TestManageContextTruncatesOversizedToolOutput(t *testing.T) {
	messages := []openai.ChatMessage{
		{Role: "user", Content: openai.TextContent("read")},
		{
			Role:       "assistant",
			Content:    []byte("null"),
			ToolCalls:  []openai.ToolCall{{ID: "call_1", Type: "function", Function: openai.ToolCallFunction{Name: "read_file", Arguments: `{"path":"big.txt"}`}}},
			ToolCallID: "",
		},
		{Role: "tool", ToolCallID: "call_1", Content: openai.TextContent(strings.Repeat("x", 200))},
	}
	cfg := contextTestConfig()
	cfg.ContextToolOutputMaxBytes = 80
	cfg.ContextMaxBytes = 0
	cfg.ContextMaxMessages = 0

	result := manageContext(messages, cfg)
	if !result.Changed || result.TruncatedTools != 1 {
		t.Fatalf("changed=%t truncated=%d, want one truncation", result.Changed, result.TruncatedTools)
	}
	if result.Messages[1].ToolCalls[0].ID != "call_1" || result.Messages[2].ToolCallID != "call_1" {
		t.Fatalf("tool pairing changed: assistant=%#v tool=%#v", result.Messages[1], result.Messages[2])
	}
	text := openai.MessageText(result.Messages[2].Content)
	if !strings.Contains(text, "tool output truncated from 200 bytes") {
		t.Fatalf("tool content = %q, want truncation marker", text)
	}
	if strings.Contains(text, strings.Repeat("x", 200)) {
		t.Fatalf("tool content was not truncated: %q", text)
	}
}

func TestManageContextCompactsOlderToolOutputsAndKeepsRecentMessages(t *testing.T) {
	messages := []openai.ChatMessage{
		{Role: "user", Content: openai.TextContent("first")},
		{Role: "assistant", Content: []byte("null"), ToolCalls: []openai.ToolCall{{ID: "call_old", Type: "function", Function: openai.ToolCallFunction{Name: "list", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "call_old", Content: openai.TextContent(strings.Repeat("old", 60))},
		{Role: "user", Content: openai.TextContent("second")},
		{Role: "assistant", Content: []byte("null"), ToolCalls: []openai.ToolCall{{ID: "call_recent", Type: "function", Function: openai.ToolCallFunction{Name: "read", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "call_recent", Content: openai.TextContent(strings.Repeat("recent", 20))},
	}
	cfg := contextTestConfig()
	cfg.ContextMaxBytes = 1
	cfg.ContextRecentMessages = 3
	cfg.ContextCompactedToolOutputMaxBytes = 9
	cfg.ContextToolOutputMaxBytes = 1000

	result := manageContext(messages, cfg)
	if !result.Changed || result.CompactedTools != 1 {
		t.Fatalf("changed=%t compacted=%d, want one compaction", result.Changed, result.CompactedTools)
	}
	oldText := openai.MessageText(result.Messages[2].Content)
	if !strings.Contains(oldText, "older tool output compacted") {
		t.Fatalf("old tool content = %q, want compaction marker", oldText)
	}
	if result.Messages[1].ToolCalls[0].ID != "call_old" || result.Messages[2].ToolCallID != "call_old" {
		t.Fatalf("old pair changed: assistant=%#v tool=%#v", result.Messages[1], result.Messages[2])
	}
	recentText := openai.MessageText(result.Messages[5].Content)
	if recentText != strings.Repeat("recent", 20) {
		t.Fatalf("recent tool content changed: %q", recentText)
	}
	if result.Messages[4].ToolCalls[0].ID != "call_recent" || result.Messages[5].ToolCallID != "call_recent" {
		t.Fatalf("recent pair changed: assistant=%#v tool=%#v", result.Messages[4], result.Messages[5])
	}
}

func TestManageContextDoesNotCompactToolMessagesWithoutCallID(t *testing.T) {
	messages := []openai.ChatMessage{
		{Role: "tool", Content: openai.TextContent("orphan")},
		{Role: "user", Content: openai.TextContent("recent")},
	}
	cfg := contextTestConfig()
	cfg.ContextMaxBytes = 1
	cfg.ContextRecentMessages = 1

	result := manageContext(messages, cfg)
	if result.CompactedTools != 0 {
		t.Fatalf("CompactedTools = %d, want 0", result.CompactedTools)
	}
	if openai.MessageText(result.Messages[0].Content) != "orphan" {
		t.Fatalf("orphan tool changed: %#v", result.Messages[0])
	}
}

func TestManageContextDoesNotExpandOlderSmallToolOutputs(t *testing.T) {
	messages := []openai.ChatMessage{
		{Role: "assistant", Content: []byte("null"), ToolCalls: []openai.ToolCall{{ID: "call_small", Type: "function", Function: openai.ToolCallFunction{Name: "lookup", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "call_small", Content: openai.TextContent("small")},
		{Role: "user", Content: openai.TextContent("recent")},
	}
	cfg := contextTestConfig()
	cfg.ContextMaxBytes = 1
	cfg.ContextRecentMessages = 1
	cfg.ContextCompactedToolOutputMaxBytes = 32

	result := manageContext(messages, cfg)
	if result.CompactedTools != 0 {
		t.Fatalf("CompactedTools = %d, want 0", result.CompactedTools)
	}
	if openai.MessageText(result.Messages[1].Content) != "small" {
		t.Fatalf("small tool output changed: %#v", result.Messages[1])
	}
}

func TestDefaultContextManagementCompactsOlderToolOutputs(t *testing.T) {
	messages := []openai.ChatMessage{{Role: "user", Content: openai.TextContent("first")}}
	for i := 0; i < 8; i++ {
		callID := "call_old_" + string(rune('a'+i))
		messages = append(messages,
			openai.ChatMessage{Role: "assistant", Content: []byte("null"), ToolCalls: []openai.ToolCall{{ID: callID, Type: "function", Function: openai.ToolCallFunction{Name: "list", Arguments: `{}`}}}},
			openai.ChatMessage{Role: "tool", ToolCallID: callID, Content: openai.TextContent(strings.Repeat("old", 20000))},
		)
	}
	for i := 0; i < config.DefaultContextRecentMessages; i++ {
		messages = append(messages, openai.ChatMessage{Role: "user", Content: openai.TextContent("filler")})
	}
	messages = append(messages,
		openai.ChatMessage{Role: "user", Content: openai.TextContent("second")},
		openai.ChatMessage{Role: "assistant", Content: []byte("null"), ToolCalls: []openai.ToolCall{{ID: "call_recent", Type: "function", Function: openai.ToolCallFunction{Name: "read", Arguments: `{}`}}}},
		openai.ChatMessage{Role: "tool", ToolCallID: "call_recent", Content: openai.TextContent("recent result")},
	)
	cfg := config.Defaults()

	result := manageContext(messages, cfg)
	if !result.Changed {
		t.Fatal("Changed = false, want default context management to compact oversized Cursor-style context")
	}
	if result.TruncatedTools != 8 || result.CompactedTools != 8 {
		t.Fatalf("truncated=%d compacted=%d, want eight truncations and older compactions", result.TruncatedTools, result.CompactedTools)
	}
	oldText := openai.MessageText(result.Messages[2].Content)
	if !strings.Contains(oldText, "older tool output compacted") {
		t.Fatalf("old tool content = %q, want compaction marker", oldText)
	}
	if len(oldText) > config.DefaultContextCompactedToolOutputMaxBytes+200 {
		t.Fatalf("old tool content len = %d, want compact default near %d", len(oldText), config.DefaultContextCompactedToolOutputMaxBytes)
	}
	if result.Messages[1].ToolCalls[0].ID != "call_old_a" || result.Messages[2].ToolCallID != "call_old_a" {
		t.Fatalf("old tool pair changed: assistant=%#v tool=%#v", result.Messages[1], result.Messages[2])
	}
	if got := openai.MessageText(result.Messages[len(result.Messages)-1].Content); got != "recent result" {
		t.Fatalf("recent tool content = %q, want unchanged", got)
	}
}

func TestManageContextTruncatesStructuredToolOutputParts(t *testing.T) {
	large := strings.Repeat("y", 200)
	messages := []openai.ChatMessage{
		{Role: "user", Content: openai.TextContent("read")},
		{
			Role:      "assistant",
			Content:   []byte("null"),
			ToolCalls: []openai.ToolCall{{ID: "call_struct", Type: "function", Function: openai.ToolCallFunction{Name: "read_file", Arguments: `{}`}}},
		},
		{
			Role:       "tool",
			ToolCallID: "call_struct",
			Content:    json.RawMessage(`[{"type":"input_text","text":"` + large + `"}]`),
		},
	}
	cfg := contextTestConfig()
	cfg.ContextToolOutputMaxBytes = 80
	cfg.ContextMaxBytes = 0
	cfg.ContextMaxMessages = 0

	result := manageContext(messages, cfg)
	if !result.Changed || result.TruncatedTools != 1 {
		t.Fatalf("changed=%t truncated=%d, want one truncation", result.Changed, result.TruncatedTools)
	}
	text := openai.MessageText(result.Messages[2].Content)
	if !strings.Contains(text, "tool output truncated from 200 bytes") {
		t.Fatalf("tool content = %q, want truncation marker", text)
	}
}

func contextTestConfig() config.Config {
	cfg := config.Defaults()
	cfg.ContextManagementEnabled = true
	cfg.ContextMaxBytes = 100000
	cfg.ContextMaxMessages = 100000
	cfg.ContextRecentMessages = 10
	cfg.ContextToolOutputMaxBytes = 100000
	cfg.ContextCompactedToolOutputMaxBytes = 32
	return cfg
}
