package claude

import (
	"testing"

	"github.com/teslashibe/open-agent-api/internal/codex"
)

func TestNewClientDefaults(t *testing.T) {
	client, err := NewClient(Config{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.executable != DefaultExecutable || client.defaultModel != DefaultModel || client.timeout != DefaultTimeout || client.historyMode != HistoryModeNative {
		t.Fatalf("client = %#v", client)
	}
	if _, err := NewClient(Config{HistoryMode: "bogus"}); err == nil {
		t.Fatal("expected error for unknown history mode")
	}
}

func TestClaudeEffortPassesFullLadder(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		if claudeEffort(effort) != effort {
			t.Fatalf("effort %q dropped", effort)
		}
	}
	if claudeEffort("none") != "" || claudeEffort("minimal") != "" || claudeEffort("") != "" {
		t.Fatal("expected unsupported efforts to be omitted")
	}
}

func TestModelAndEffortNormalizesCursorNames(t *testing.T) {
	client, err := NewClient(Config{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cases := []struct{ in, model, effort string }{
		{"", DefaultModel, ""},
		{"api/claude-fable-5", "claude-fable-5", ""},
		{"anthropic/claude-opus-5.5", "claude-opus-5-5", ""},
		{"claude-opus-5.5-high", "claude-opus-5-5", "high"},
		{"anthropic/claude-fable-5-1-xhigh", "claude-fable-5-1", "xhigh"},
		{"opus", "opus", ""},
	}
	for _, tc := range cases {
		model, effort := client.modelAndEffort(codex.Request{Model: tc.in})
		if model != tc.model || effort != tc.effort {
			t.Fatalf("%q -> (%q,%q), want (%q,%q)", tc.in, model, effort, tc.model, tc.effort)
		}
	}
}
