package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseToolSpecsSupportsFlatFunctionAndCustom(t *testing.T) {
	specs := parseToolSpecs(json.RawMessage(`[
		{"type":"function","name":"read_file","description":"read","parameters":{"type":"object"}},
		{"type":"custom","custom":{"name":"apply_patch","description":"patch"}}
	]`))
	if len(specs) != 2 {
		t.Fatalf("specs = %#v", specs)
	}
	if specs[0].Name != "read_file" || specs[0].Type != "function" {
		t.Fatalf("function spec = %#v", specs[0])
	}
	if specs[1].Name != "apply_patch" || specs[1].Type != "custom" {
		t.Fatalf("custom spec = %#v", specs[1])
	}
}

func TestToolInstructionsContainProtocolAndToolNames(t *testing.T) {
	prompt := toolInstructions([]toolSpec{{Name: "read_file", Type: "function", Description: "read"}})
	if !strings.Contains(prompt, "cursor_tool_call") || !strings.Contains(prompt, "read_file") || !strings.Contains(prompt, "must request Cursor tool execution") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestExtractCursorToolCall(t *testing.T) {
	call, ok := extractCursorToolCall("```cursor_tool_call\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"README.md\"}}\n```")
	if !ok || call.Name != "read_file" || string(call.Arguments) != `{"path":"README.md"}` {
		t.Fatalf("call=%#v ok=%t", call, ok)
	}
}
