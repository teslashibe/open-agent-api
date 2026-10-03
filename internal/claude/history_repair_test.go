package claude

import (
	"fmt"
	"strings"
	"testing"

	"github.com/teslashibe/open-agent-api/internal/openai"
)

func TestRepairUsesGloballyUniqueIDsAndMatchingResults(t *testing.T) {
	cases := [][]string{
		{"a", "a", "a_1"},
		{"a_1", "a", "a"},
		{"a", "a", "a", "a_1", "a_2", "a_1_1"},
		{strings.Repeat("a", 64), strings.Repeat("a", 64), strings.Repeat("a", 62) + "_1"},
	}
	for _, ids := range cases {
		messages := []openai.ChatMessage{{Role: "user", Content: openai.TextContent("go")}}
		for i, id := range ids {
			messages = append(messages, openai.ChatMessage{Role: "assistant", ToolCalls: []openai.ToolCall{fnCall(id, "Read", fmt.Sprintf(`{"path":"file%d"}`, i))}}, openai.ChatMessage{Role: "tool", ToolCallID: id, Content: openai.TextContent(fmt.Sprintf("result%d", i))})
		}
		conv := buildConversation(messages, readTool())
		all := append(append([]turn{}, conv.History...), conv.Tail...)
		used := map[string]bool{}
		index := 0
		for i, tn := range all {
			if tn.Role != "assistant" {
				continue
			}
			use := tn.Content[0]
			id := use["id"].(string)
			if used[id] || !toolUseIDPattern.MatchString(id) {
				t.Fatalf("invalid/reused id %q in %s", id, mustJSON(t, all))
			}
			used[id] = true
			result := all[i+1].Content[0]
			if result["tool_use_id"] != id || result["content"] != fmt.Sprintf("result%d", index) {
				t.Fatalf("mismatched result: %#v / %#v", use, result)
			}
			index++
		}
		if index != len(ids) {
			t.Fatalf("calls lost: %d", index)
		}
	}
}

func TestCollisionRepairPreservesParallelPairsAndOrphans(t *testing.T) {
	conv := buildConversation([]openai.ChatMessage{
		{Role: "user", Content: openai.TextContent("go")},
		{Role: "assistant", ToolCalls: []openai.ToolCall{fnCall("a", "Read", `{"path":"old"}`)}},
		{Role: "tool", ToolCallID: "a", Content: openai.TextContent("old")},
		{Role: "assistant", ToolCalls: []openai.ToolCall{fnCall("a", "Read", `{"path":"first"}`), fnCall("a_1", "Read", `{"path":"second"}`)}},
		{Role: "tool", ToolCallID: "a", Content: openai.TextContent("first")},
		{Role: "tool", ToolCallID: "a_1", Content: openai.TextContent("second")},
		{Role: "tool", ToolCallID: "orphan", Content: openai.TextContent("unmatched")},
	}, readTool())
	calls := conv.Tail[0].Content
	results := conv.Tail[1].Content
	if calls[0]["id"] == calls[1]["id"] || results[0]["tool_use_id"] != calls[0]["id"] || results[1]["tool_use_id"] != calls[1]["id"] || results[0]["content"] != "first" || results[1]["content"] != "second" {
		t.Fatalf("pairs lost: %s / %s", mustJSON(t, calls), mustJSON(t, results))
	}
	if results[2]["type"] != "text" || !strings.Contains(results[2]["text"].(string), "unmatched") {
		t.Fatalf("orphan lost: %#v", results[2])
	}
}
