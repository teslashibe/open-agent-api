package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

// fakeClaude writes a script that records its argv, stdin, system prompt and
// resumed transcript into logDir, then emits canned stream-json on stdout, so
// the client can be exercised end-to-end without the real CLI.
func fakeClaude(t *testing.T, jsonlLines []string) (script string, logDir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude harness uses a POSIX shell script")
	}
	dir := t.TempDir()
	logDir = filepath.Join(dir, "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script = filepath.Join(dir, "claude")
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("if [ \"$1\" = \"--version\" ]; then echo '2.1.283 (Claude Code)'; exit 0; fi\n")
	b.WriteString("LOG='" + logDir + "'\n")
	b.WriteString("printf '%s\\n' \"$@\" > \"$LOG/args\"\n")
	b.WriteString("cat > \"$LOG/stdin\"\n")
	b.WriteString("pwd > \"$LOG/cwd\"\n")
	b.WriteString("prev=''\nfor a in \"$@\"; do\n")
	b.WriteString("  case \"$prev\" in --resume) cp \"$a\" \"$LOG/history.jsonl\";; --system-prompt-file) cp \"$a\" \"$LOG/system.md\";; --mcp-config) cp \"$a\" \"$LOG/mcp.json\";; esac\n")
	b.WriteString("  prev=\"$a\"\ndone\n")
	b.WriteString("[ -f tools.json ] && cp tools.json \"$LOG/tools.json\"\n")
	for _, line := range jsonlLines {
		b.WriteString("cat <<'CLAUDE_EOF'\n" + line + "\nCLAUDE_EOF\n")
	}
	if err := os.WriteFile(script, []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return script, logDir
}

func textDelta(text string) string {
	payload, _ := json.Marshal(text)
	return `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(payload) + `}}}`
}

func readLog(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func newTestClient(t *testing.T, script string, mode string) *Client {
	t.Helper()
	client, err := NewClient(Config{Executable: script, Timeout: 10 * time.Second, BridgeExecutable: "/usr/local/bin/open-agent-api", RunDir: t.TempDir(), HistoryMode: mode})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

var cursorTools = json.RawMessage(`[{"type":"function","function":{"name":"Read","description":"read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}]`)

func TestCompleteNativeToolCallFirstTurn(t *testing.T) {
	script, logDir := fakeClaude(t, []string{lineInit, lineMsgStart, lineToolStart0, lineToolDelta0a, lineToolDelta0b, lineToolStop0, lineMsgDelta, lineMsgStop})
	client := newTestClient(t, script, HistoryModeNative)
	completion, err := client.Complete(context.Background(), codex.Request{
		Model:           "claude-opus-5-5",
		ReasoningEffort: "max",
		Messages: []openai.ChatMessage{
			{Role: "system", Content: openai.TextContent("You are Cursor's agent.")},
			{Role: "user", Content: openai.TextContent("read the readme")},
		},
		Tools: cursorTools,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(completion.ToolCalls) != 1 || completion.ToolCalls[0].ID != "toolu_A" || completion.ToolCalls[0].Function.Name != "Read" || completion.ToolCalls[0].Function.Arguments != `{"path": "README.md"}` {
		t.Fatalf("tool calls = %#v", completion.ToolCalls)
	}
	if completion.Text != "" {
		t.Fatalf("text = %q", completion.Text)
	}

	args := readLog(t, logDir, "args")
	for _, want := range []string{"--model\nclaude-opus-5-5\n", "--effort\nmax\n", "--tools\n\n", "--setting-sources=\n", "--strict-mcp-config\n", "--permission-mode\ndontAsk\n", "--max-turns\n1\n", "--input-format\nstream-json\n", "--no-session-persistence\n"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "--resume") {
		t.Fatalf("single-turn request must not resume:\n%s", args)
	}
	system := readLog(t, logDir, "system.md")
	if !strings.HasPrefix(system, "You are Cursor's agent.") || !strings.Contains(system, "mcp__c__") {
		t.Fatalf("system prompt = %q", system)
	}
	mcp := readLog(t, logDir, "mcp.json")
	if !strings.Contains(mcp, `"command":"/usr/local/bin/open-agent-api"`) || !strings.Contains(mcp, `"claude-mcp"`) {
		t.Fatalf("mcp config = %s", mcp)
	}
	if tools := readLog(t, logDir, "tools.json"); !strings.Contains(tools, `"name":"Read"`) || !strings.Contains(tools, `"inputSchema"`) {
		t.Fatalf("tools file = %s", tools)
	}
	stdin := strings.TrimSpace(readLog(t, logDir, "stdin"))
	if strings.Count(stdin, "\n") != 0 || !strings.Contains(stdin, `"type":"user"`) || !strings.Contains(stdin, "read the readme") {
		t.Fatalf("stdin = %s", stdin)
	}
}

func TestStreamToolContinuationReplaysNativeHistory(t *testing.T) {
	script, logDir := fakeClaude(t, []string{lineInit, lineMsgStart, lineTextStart, textDelta("It is Zephyr."),
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}}`, lineMsgStop})
	client := newTestClient(t, script, HistoryModeNative)
	events, err := client.Stream(context.Background(), codex.Request{
		Model: "claude-fable-5-1",
		Messages: []openai.ChatMessage{
			{Role: "user", Content: openai.TextContent("my colour is teal")},
			{Role: "assistant", Content: openai.TextContent("noted")},
			{Role: "user", Content: openai.TextContent("read the readme")},
			{Role: "assistant", ToolCalls: []openai.ToolCall{fnCall("call_cursor_1", "Read", `{"path":"README.md"}`)}},
			{Role: "tool", ToolCallID: "call_cursor_1", Content: openai.TextContent("# Zephyr")},
		},
		Tools: cursorTools,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var text strings.Builder
	for event := range events {
		if event.Err != nil {
			t.Fatalf("stream error: %v", event.Err)
		}
		text.WriteString(event.Delta)
	}
	if text.String() != "It is Zephyr." {
		t.Fatalf("text = %q", text.String())
	}
	args := readLog(t, logDir, "args")
	if !strings.Contains(args, "--resume\n") || strings.Contains(args, "--no-session-persistence") {
		t.Fatalf("args = %s", args)
	}
	history := strings.Split(strings.TrimSpace(readLog(t, logDir, "history.jsonl")), "\n")
	if len(history) != 3 {
		t.Fatalf("history rows = %d: %v", len(history), history)
	}
	var rows []map[string]any
	for _, line := range history {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("history row: %v", err)
		}
		rows = append(rows, row)
	}
	if rows[0]["parentUuid"] != nil || rows[1]["parentUuid"] != rows[0]["uuid"] || rows[2]["parentUuid"] != rows[1]["uuid"] {
		t.Fatalf("rows not chained: %v", rows)
	}
	if rows[1]["type"] != "assistant" || rows[0]["version"] != "2.1.283" || rows[0]["cwd"] != strings.TrimSpace(readLog(t, logDir, "cwd")) {
		t.Fatalf("row fields = %v", rows[1])
	}
	stdin := strings.Split(strings.TrimSpace(readLog(t, logDir, "stdin")), "\n")
	if len(stdin) != 2 || !strings.Contains(stdin[0], `"type":"assistant"`) || !strings.Contains(stdin[0], `"id":"call_cursor_1"`) || !strings.Contains(stdin[0], `"name":"mcp__c__Read"`) {
		t.Fatalf("stdin[0] = %v", stdin)
	}
	if !strings.Contains(stdin[1], `"type":"tool_result"`) || !strings.Contains(stdin[1], `"tool_use_id":"call_cursor_1"`) {
		t.Fatalf("stdin[1] = %v", stdin)
	}
}

func TestTextHistoryModeFlattensConversation(t *testing.T) {
	script, logDir := fakeClaude(t, []string{lineMsgStart, textDelta("ok"), lineMsgStop})
	client := newTestClient(t, script, HistoryModeText)
	if _, err := client.Complete(context.Background(), codex.Request{
		Model: "sonnet",
		Messages: []openai.ChatMessage{
			{Role: "user", Content: openai.TextContent("read it")},
			{Role: "assistant", ToolCalls: []openai.ToolCall{fnCall("call_1", "Read", `{"path":"a"}`)}},
			{Role: "tool", ToolCallID: "call_1", Content: openai.TextContent("AAA")},
		},
		Tools: cursorTools,
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	args := readLog(t, logDir, "args")
	if strings.Contains(args, "--resume") {
		t.Fatalf("text mode must not resume: %s", args)
	}
	stdin := strings.TrimSpace(readLog(t, logDir, "stdin"))
	if strings.Count(stdin, "\n") != 0 || strings.Count(stdin, "AAA") != 1 || !strings.Contains(stdin, "tool_call") {
		t.Fatalf("stdin = %s", stdin)
	}
}

func TestToolChoiceNoneDropsBridge(t *testing.T) {
	script, logDir := fakeClaude(t, []string{lineMsgStart, textDelta("hi"), lineMsgStop})
	client := newTestClient(t, script, HistoryModeNative)
	if _, err := client.Complete(context.Background(), codex.Request{
		Model:      "sonnet",
		Messages:   []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}},
		Tools:      cursorTools,
		ToolChoice: json.RawMessage(`"none"`),
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if args := readLog(t, logDir, "args"); strings.Contains(args, "--mcp-config") {
		t.Fatalf("tool_choice none must not attach tools: %s", args)
	}
	if system := readLog(t, logDir, "system.md"); system != defaultSystemPrompt {
		t.Fatalf("system = %q", system)
	}
}

func TestCompleteSurfacesAPIError(t *testing.T) {
	script, _ := fakeClaude(t, []string{
		`{"type":"assistant","error":"oauth_org_not_allowed","message":{"content":[]}}`,
		`{"type":"result","subtype":"success","is_error":true,"api_error_status":403,"result":"Your organization has disabled Claude subscription access for Claude Code"}`,
	})
	client := newTestClient(t, script, HistoryModeNative)
	_, err := client.Complete(context.Background(), codex.Request{Model: "sonnet", Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}}})
	typed, ok := codex.ErrorAs(err)
	if !ok || typed.Kind != codex.ErrorKindAuth || typed.Status != 403 || !strings.Contains(err.Error()+typed.Err.Error(), "disabled") {
		t.Fatalf("err = %#v", err)
	}
}

func TestRunDirIsRemoved(t *testing.T) {
	script, _ := fakeClaude(t, []string{lineMsgStart, textDelta("hi"), lineMsgStop})
	runDir := t.TempDir()
	client, err := NewClient(Config{Executable: script, Timeout: 10 * time.Second, RunDir: runDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), codex.Request{Model: "sonnet", Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}}, Tools: cursorTools}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(runDir)
	if len(entries) != 0 {
		t.Fatalf("run dir not cleaned: %v", entries)
	}
}
