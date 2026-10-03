package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

func jsonValue(t *testing.T, data []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSchemaWrappingPreservesConstraintsAndCallShape(t *testing.T) {
	cases := []struct{ name, schema, args string }{
		{"anyOf", `{"type":"object","anyOf":[{"properties":{"path":{"type":"string","minLength":1}},"required":["path"]},{"properties":{"url":{"type":"string","pattern":"^https://"}},"required":["url"]}]}`, `{"path":"README.md"}`},
		{"oneOf", `{"type":"object","oneOf":[{"properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false},{"properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}]}`, `{"url":"https://example.test"}`},
		{"allOf", `{"type":"object","allOf":[{"properties":{"count":{"type":"integer","minimum":1}},"required":["count"]},{"properties":{"count":{"maximum":5}}}]}`, `{"count":3}`},
		{"array", `{"type":"array","items":{"type":"integer"},"minItems":1}`, `[1,2]`},
		{"nullable", `{"type":["string","null"],"minLength":3}`, `null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "lookup.resource"
			spec := toolSpec{Name: name, Type: "function", Parameters: json.RawMessage(tc.schema)}
			set := newToolSet([]toolSpec{spec})
			if err := set.validateSchemas(); err != nil {
				t.Fatal(err)
			}
			advertised := jsonValue(t, inputSchema(spec)).(map[string]any)
			properties := advertised["properties"].(map[string]any)
			if !reflect.DeepEqual(properties["input"], jsonValue(t, []byte(tc.schema))) {
				t.Fatalf("lost constraints: %s", inputSchema(spec))
			}
			if !reflect.DeepEqual(advertised["required"], []any{"input"}) || advertised["additionalProperties"] != false {
				t.Fatalf("invalid wrapper: %#v", advertised)
			}
			call := fnCall("call_1", name, tc.args)
			use := toolUseBlock(call, set)
			input := mustJSON(t, use["input"])
			if !reflect.DeepEqual(jsonValue(t, []byte(toolArguments(spec, input))), jsonValue(t, []byte(tc.args))) {
				t.Fatalf("replay/output mismatch: %s", input)
			}
			p := newStreamParser(set)
			event := p.toolEvent("call_1", set.modelName(name), input)
			if event.ToolCallDelta.Function.Name != name || !reflect.DeepEqual(jsonValue(t, []byte(event.ToolCallDelta.Function.Arguments)), jsonValue(t, []byte(tc.args))) {
				t.Fatalf("wrong name/shape: %#v", event.ToolCallDelta)
			}
		})
	}
}

func TestUnwrappedLocalDefinitionsAndReferencesRemainValid(t *testing.T) {
	raw := `{"type":"object","$defs":{"Path":{"type":"string","minLength":1}},"properties":{"path":{"$ref":"#/$defs/Path"}},"required":["path"]}`
	spec := toolSpec{Name: "Read", Type: "function", Parameters: json.RawMessage(raw)}
	set := newToolSet([]toolSpec{spec})
	if err := set.validateSchemas(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(jsonValue(t, inputSchema(spec)), jsonValue(t, []byte(raw))) {
		t.Fatalf("changed reference root: %s", inputSchema(spec))
	}
	if got := toolArguments(spec, `{"path":"a"}`); got != `{"path":"a"}` {
		t.Fatalf("unexpected wrapping: %s", got)
	}
}

func TestUnsupportedWrappedReferencesRejectedBeforeCLI(t *testing.T) {
	schemas := []string{
		`{"type":"object","$defs":{"Path":{"type":"string"}},"oneOf":[{"properties":{"path":{"$ref":"#/$defs/Path"}}}]}`,
		`{"type":"array","items":{"$ref":"#/definitions/Item"},"definitions":{"Item":{"type":"string"}}}`,
		`{"anyOf":[{"$dynamicRef":"#node"}]}`,
		`{"allOf":[{"$recursiveRef":"#"}]}`,
		`false`,
	}
	for _, schema := range schemas {
		script, logDir := fakeClaude(t, nil)
		client := newTestClient(t, script, HistoryModeNative)
		tools := `[{"type":"function","function":{"name":"Read","parameters":` + schema + `}}]`
		_, err := client.prepare(codex.Request{Model: "api/claude-opus-5-5-fast", Tools: json.RawMessage(tools), Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("read")}}})
		typed, ok := codex.ErrorAs(err)
		if !ok || typed.Status != 400 || !strings.Contains(typed.Message, "Read") {
			t.Fatalf("schema=%s error=%v", schema, err)
		}
		if client.version != "" {
			t.Fatal("version process invoked before schema rejection")
		}
		if _, err := os.Stat(filepath.Join(logDir, "args")); !os.IsNotExist(err) {
			t.Fatalf("CLI ran: %v", err)
		}
	}
}

func TestWrappedCallsRoundTripThroughStreamAndCompletion(t *testing.T) {
	name := "lookup.resource"
	tools := json.RawMessage(`[{"type":"function","function":{"name":"lookup.resource","parameters":{"type":"object","oneOf":[{"properties":{"path":{"type":"string"}},"required":["path"]},{"properties":{"url":{"type":"string"}},"required":["url"]}]}}}]`)
	set := newToolSet(parseToolSpecs(tools))
	payload := map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": "call_1", "name": set.modelName(name), "input": map[string]any{"input": map[string]any{"path": "README.md"}}}}}}
	encoded, _ := json.Marshal(payload)
	for _, stream := range []bool{false, true} {
		script, _ := fakeClaude(t, []string{string(encoded), lineMsgStop})
		client := newTestClient(t, script, HistoryModeNative)
		req := codex.Request{Model: "sonnet", Tools: tools, Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("read")}}}
		var calls []codex.ToolCall
		if stream {
			events, err := client.Stream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			for event := range events {
				if event.Err != nil {
					t.Fatal(event.Err)
				}
				if event.ToolCallDelta != nil {
					calls = append(calls, toolCallFromDelta(*event.ToolCallDelta))
				}
			}
		} else {
			completion, err := client.Complete(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			calls = completion.ToolCalls
		}
		if len(calls) != 1 || calls[0].Function.Name != name || calls[0].Function.Arguments != `{"path":"README.md"}` {
			t.Fatalf("stream=%t calls=%#v", stream, calls)
		}
	}
}

func TestAnthropicHistoryUsesTheSameUnionWrapper(t *testing.T) {
	set := newToolSet([]toolSpec{{Name: "Read", Type: "function", Parameters: json.RawMessage(`{"type":"object","anyOf":[{"required":["path"]}]}`)}})
	conv := buildConversation([]openai.ChatMessage{
		{Role: "user", Content: openai.TextContent("read")},
		{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"call_1","name":"Read","input":{"path":"a"}}]`)},
		{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]`)},
	}, set)
	if got := mustJSON(t, conv.Tail[0].Content[0]["input"]); got != `{"input":{"path":"a"}}` {
		t.Fatalf("history shape=%s", got)
	}
}

func TestWrappedSchemaAndHistoryPreserveLargeIntegers(t *testing.T) {
	spec := toolSpec{Name: "Read", Type: "function", Parameters: json.RawMessage(`{"type":"object","allOf":[{"properties":{"id":{"type":"integer","minimum":9007199254740993}},"required":["id"]}]}`)}
	set := newToolSet([]toolSpec{spec})
	if !strings.Contains(string(inputSchema(spec)), `"minimum":9007199254740993`) {
		t.Fatalf("rounded constraint: %s", inputSchema(spec))
	}
	use := toolUseBlock(fnCall("call_1", "Read", `{"id":9007199254740993}`), set)
	got := toolArguments(spec, mustJSON(t, use["input"]))
	if got != `{"id":9007199254740993}` {
		t.Fatalf("rounded replay arguments: %s", got)
	}
}
