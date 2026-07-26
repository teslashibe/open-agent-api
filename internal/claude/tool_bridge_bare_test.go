package claude

import (
	"testing"

	"github.com/teslashibe/open-chat-api/internal/codex"
)

func bareSpecs() []toolSpec {
	return []toolSpec{{Name: "Glob", Type: "function"}, {Name: "ApplyPatch", Type: "custom"}}
}

func collectBridge(t *testing.T, parser *toolBridgeParser, deltas []string) (text string, calls []codex.ToolCallDelta) {
	t.Helper()
	for _, delta := range deltas {
		for _, event := range parser.consume(codex.StreamEvent{Delta: delta}) {
			text += event.Delta
			if event.ToolCallDelta != nil {
				calls = append(calls, *event.ToolCallDelta)
			}
		}
	}
	for _, event := range parser.consume(codex.StreamEvent{Done: true}) {
		text += event.Delta
		if event.ToolCallDelta != nil {
			calls = append(calls, *event.ToolCallDelta)
		}
	}
	return text, calls
}

func TestBridgeParsesBareToolCallJSON(t *testing.T) {
	parser := newToolBridgeParser(bareSpecs())
	text, calls := collectBridge(t, parser, []string{
		`{"name":"Glob","arguments":{"target_directory":"/tmp","glob_pattern":"design/**"}}`,
	})
	if len(calls) != 1 {
		t.Fatalf("calls = %#v text=%q", calls, text)
	}
	if calls[0].Function.Name != "Glob" {
		t.Fatalf("tool = %q", calls[0].Function.Name)
	}
	if text != "" {
		t.Fatalf("tool JSON leaked as text: %q", text)
	}
}

func TestBridgeParsesBareToolCallSplitAcrossDeltas(t *testing.T) {
	parser := newToolBridgeParser(bareSpecs())
	text, calls := collectBridge(t, parser, []string{
		`{"name":"Gl`, `ob","arguments":{"glob_pattern":"**"}}`, "\nDone.",
	})
	if len(calls) != 1 || calls[0].Function.Name != "Glob" {
		t.Fatalf("calls = %#v", calls)
	}
	if text != "Done." {
		t.Fatalf("text = %q", text)
	}
}

func TestBridgeKeepsUnknownToolJSONAsText(t *testing.T) {
	parser := newToolBridgeParser(bareSpecs())
	line := `{"name":"NotARealTool","arguments":{}}` + "\n"
	text, calls := collectBridge(t, parser, []string{line})
	if len(calls) != 0 {
		t.Fatalf("unexpected calls: %#v", calls)
	}
	if text != line {
		t.Fatalf("text = %q", text)
	}
}

func TestBridgeKeepsOrdinaryJSONProse(t *testing.T) {
	parser := newToolBridgeParser(bareSpecs())
	line := `{"config":"value","nested":{"a":1}}` + "\nprose continues"
	text, calls := collectBridge(t, parser, []string{line})
	if len(calls) != 0 {
		t.Fatalf("unexpected calls: %#v", calls)
	}
	if text != line {
		t.Fatalf("text = %q", text)
	}
}

func TestBridgeStillParsesFencedCalls(t *testing.T) {
	parser := newToolBridgeParser(bareSpecs())
	text, calls := collectBridge(t, parser, []string{
		"Working on it.\n```cursor_tool_call\n{\"name\":\"Glob\",\"arguments\":{}}\n```\n",
	})
	if len(calls) != 1 || calls[0].Function.Name != "Glob" {
		t.Fatalf("calls = %#v", calls)
	}
	if text != "Working on it.\n\n" {
		t.Fatalf("text = %q", text)
	}
}

func TestBridgeProseStreamingNotLineBuffered(t *testing.T) {
	parser := newToolBridgeParser(bareSpecs())
	events := parser.consume(codex.StreamEvent{Delta: "partial prose without newline"})
	text := ""
	for _, event := range events {
		text += event.Delta
	}
	if text != "partial prose without newline" {
		t.Fatalf("prose held back: %q", text)
	}
}
