package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

func TestCLIModelControlsAndMeasuredSpeed(t *testing.T) {
	for _, tc := range []struct{ alias, effort, speed, measured string }{
		{"api/claude-opus-5-5-fast-max", "max", "fast", "standard"},
		{"api/claude-opus-5-5-normal-xhigh", "xhigh", "normal", ""},
		{"api/claude-opus-4-6-max", "max", "", ""},
		{"api/claude-haiku-4-5", "", "", ""},
	} {
		t.Run(tc.alias, func(t *testing.T) {
			result := `{"type":"result","subtype":"success","result":"hello","usage":{"input_tokens":3,"output_tokens":2,"speed":"` + tc.measured + `"}}`
			script, logDir := fakeClaude(t, []string{result})
			client := newTestClient(t, script, HistoryModeNative)
			completion, err := client.Complete(context.Background(), codex.Request{Model: tc.alias, Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hello")}}})
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSpace(readLog(t, logDir, "args")), "\n")
			value := func(flag string) string {
				for i, a := range args {
					if a == flag && i+1 < len(args) {
						return args[i+1]
					}
				}
				return ""
			}
			if value("--effort") != tc.effort || strings.Contains(value("--model"), "api/") || strings.Contains(value("--model"), "-fast") {
				t.Fatalf("args = %v", args)
			}
			var settings struct {
				FastMode bool `json:"fastMode"`
			}
			if err := json.Unmarshal([]byte(value("--settings")), &settings); err != nil {
				t.Fatal(err)
			}
			if settings.FastMode != (tc.speed == "fast") {
				t.Fatalf("settings=%#v", settings)
			}
			if completion.Usage.Speed != tc.measured || completion.Usage.TotalTokens != 5 {
				t.Fatalf("usage=%#v", completion.Usage)
			}
		})
	}
}

func TestInvalidClaudeControlsNeverInvokeCLI(t *testing.T) {
	for _, tc := range []struct{ model, effort, speed string }{
		{"api/claude-sonnet-5-5", "high", "fast"}, {"api/claude-opus-4-6", "xhigh", ""},
		{"api/claude-haiku-4-5", "high", ""}, {"api/claude-opus-4-5", "max", ""},
	} {
		script, logDir := fakeClaude(t, nil)
		client := newTestClient(t, script, HistoryModeNative)
		_, err := client.Complete(context.Background(), codex.Request{Model: tc.model, ReasoningEffort: tc.effort, Speed: tc.speed, Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hello")}}})
		if err == nil {
			t.Fatalf("accepted %#v", tc)
		}
		if _, err := os.Stat(filepath.Join(logDir, "args")); !os.IsNotExist(err) {
			t.Fatalf("CLI was invoked: %v", err)
		}
	}
}

func TestFastPrintRequiresSupportedCLIVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    bool
	}{{"", false}, {"2.1.204", false}, {"2.1.205", true}, {"2.1.286", true}, {"2.2.0", true}, {"3.0.0", true}, {"invalid", false}} {
		if got := supportsFastPrint(tc.version); got != tc.want {
			t.Fatalf("version %s = %t", tc.version, got)
		}
	}
	script, logDir := fakeClaude(t, nil)
	client := newTestClient(t, script, HistoryModeNative)
	client.versionOnce.Do(func() { client.version = "2.1.204" })
	_, err := client.prepare(codex.Request{Model: "api/claude-opus-5-5-fast", Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hello")}}})
	if err == nil {
		t.Fatal("old CLI accepted fast")
	}
	if _, err := os.Stat(filepath.Join(logDir, "args")); !os.IsNotExist(err) {
		t.Fatalf("CLI invoked: %v", err)
	}
}

func TestUsageNeverInventsFast(t *testing.T) {
	for _, speed := range []string{"", "fast", "standard", "unknown"} {
		got := usageToOpenAI(usage{Speed: speed})
		want := speed
		if speed == "unknown" {
			want = ""
		}
		if got.Speed != want {
			t.Fatalf("speed %q = %q", speed, got.Speed)
		}
	}
}
