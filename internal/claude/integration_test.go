package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

// fakeClaude writes a small script to disk that emits Claude Code stream-json
// JSONL on stdout, so the client can be exercised end-to-end without the real
// CLI or upstream credentials.
func fakeClaude(t *testing.T, jsonlLines []string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude harness uses a POSIX shell script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	var b []byte
	b = append(b, "#!/bin/sh\n"...)
	for _, line := range jsonlLines {
		b = append(b, "cat <<'CLAUDE_EOF'\n"...)
		b = append(b, line...)
		b = append(b, "\nCLAUDE_EOF\n"...)
	}
	if err := os.WriteFile(script, b, 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return script
}

func textDelta(text string) string {
	payload, _ := json.Marshal(text)
	return `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":` + string(payload) + `}}}`
}

func TestCompleteAccumulatesToolCalls(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"sess-1","model":"claude-sonnet-5"}`,
		textDelta("```cursor_tool_call\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"go.mod\"}}\n```"),
		`{"type":"result","subtype":"success","session_id":"sess-1","model":"claude-sonnet-5","usage":{"input_tokens":10,"output_tokens":4}}`,
	}
	client, err := NewClient(Config{Executable: fakeClaude(t, lines), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	req := codex.Request{
		Model:    "claude-sonnet-5",
		Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("read go.mod")}},
		Tools:    json.RawMessage(`[{"type":"function","function":{"name":"read_file","description":"read","parameters":{"type":"object"}}}]`),
	}
	completion, err := client.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", completion.ToolCalls)
	}
	call := completion.ToolCalls[0]
	if call.Function.Name != "read_file" || call.Function.Arguments != `{"path":"go.mod"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if completion.Text != "" {
		t.Fatalf("fence leaked into text: %q", completion.Text)
	}
	if completion.Model != "claude-sonnet-5" {
		t.Fatalf("model = %q", completion.Model)
	}
}

func TestStreamPassesPlainTextThrough(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s","model":"claude-sonnet-5"}`,
		textDelta("Hello "),
		textDelta("world"),
		`{"type":"result","subtype":"success","session_id":"s","model":"claude-sonnet-5","usage":{"input_tokens":1,"output_tokens":2}}`,
	}
	client, err := NewClient(Config{Executable: fakeClaude(t, lines), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	completion, err := client.Complete(context.Background(), codex.Request{
		Model:    "claude-sonnet-5",
		Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completion.Text != "Hello world" {
		t.Fatalf("text = %q", completion.Text)
	}
	if len(completion.ToolCalls) != 0 {
		t.Fatalf("unexpected tool calls: %#v", completion.ToolCalls)
	}
}
