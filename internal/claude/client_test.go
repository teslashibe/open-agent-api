package claude

import (
	"strings"
	"testing"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

func TestPromptFromMessages(t *testing.T) {
	prompt := promptFromMessages([]openai.ChatMessage{
		{Role: "system", Content: openai.TextContent("be brief")},
		{Role: "user", Content: openai.TextContent("say hi")},
	}, nil)
	if !strings.Contains(prompt, "System: be brief") || !strings.Contains(prompt, "User: say hi") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestNewClientDefaults(t *testing.T) {
	client, err := NewClient(Config{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.executable != DefaultExecutable || client.defaultModel != DefaultModel || client.timeout != DefaultTimeout {
		t.Fatalf("client = %#v", client)
	}
}

func TestCommandIncludesModelAndEffort(t *testing.T) {
	client, err := NewClient(Config{Executable: "claude"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cmd := client.command(t.Context(), codex.Request{
		Model:           "fable",
		ReasoningEffort: "high",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}},
	}, nil)
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "--model fable") || !strings.Contains(args, "--effort high") {
		t.Fatalf("args = %v", cmd.Args)
	}
}

func TestClaudeEffortFiltersUnsupportedValues(t *testing.T) {
	if claudeEffort("low") != "low" || claudeEffort("medium") != "medium" || claudeEffort("high") != "high" {
		t.Fatal("expected low/medium/high to pass through")
	}
	if claudeEffort("none") != "" || claudeEffort("") != "" {
		t.Fatal("expected unsupported efforts to be omitted")
	}
}

func TestModelStripsAPIPrefix(t *testing.T) {
	client, err := NewClient(Config{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := client.model(codex.Request{Model: "api/claude-fable-5"}); got != "claude-fable-5" {
		t.Fatalf("model = %q", got)
	}
}

func TestPromptFromMessagesIncludesToolProtocol(t *testing.T) {
	prompt := promptFromMessages([]openai.ChatMessage{{Role: "user", Content: openai.TextContent("read file")}}, []toolSpec{{Name: "read_file", Type: "function"}})
	if !strings.Contains(prompt, "cursor_tool_call") || !strings.Contains(prompt, "read_file") || !strings.Contains(prompt, "User: read file") {
		t.Fatalf("prompt = %q", prompt)
	}
}
