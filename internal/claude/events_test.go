package claude

import (
	"strings"
	"testing"

	"github.com/teslashibe/open-agent-api/internal/codex"
)

// Lines below are trimmed from real claude 2.1.283 stream-json output.
const (
	lineInit        = `{"type":"system","subtype":"init","session_id":"s1","model":"claude-opus-5-5","tools":["mcp__c__Read"]}`
	lineMsgStart    = `{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":12,"cache_read_input_tokens":100,"output_tokens":1}}}}`
	lineThinking    = `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"","estimated_tokens":50}}}`
	lineToolStart0  = `{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_A","name":"mcp__c__Read","input":{},"caller":{"type":"direct"}}}}`
	lineToolDelta0a = `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\": \"READ"}}}`
	lineToolDelta0b = `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"ME.md\"}"}}}`
	lineToolStop0   = `{"type":"stream_event","event":{"type":"content_block_stop","index":1}}`
	lineAssistant0  = `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_A","name":"mcp__c__Read","input":{"path":"README.md"}}]}}`
	lineToolStart1  = `{"type":"stream_event","event":{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_B","name":"mcp__c__Read","input":{}}}}`
	lineToolDelta1  = `{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"go.mod\"}"}}}`
	lineToolStop1   = `{"type":"stream_event","event":{"type":"content_block_stop","index":2}}`
	lineDenied      = `{"type":"system","subtype":"permission_denied"}`
	lineDeniedUser  = `{"type":"user","message":{"content":[{"type":"tool_result","content":"Permission to use mcp__c__Read has been denied"}]}}`
	lineMsgDelta    = `{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":40}}}`
	lineMsgStop     = `{"type":"stream_event","event":{"type":"message_stop"}}`
	lineTextStart   = `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`
)

func consumeAll(t *testing.T, p *streamParser, lines ...string) ([]codex.StreamEvent, bool, error) {
	t.Helper()
	var all []codex.StreamEvent
	for _, line := range lines {
		events, done, err := p.consume([]byte(line))
		all = append(all, events...)
		if err != nil || done {
			return all, done, err
		}
	}
	return all, false, nil
}

func toolDeltas(events []codex.StreamEvent) []codex.ToolCallDelta {
	var out []codex.ToolCallDelta
	for _, e := range events {
		if e.ToolCallDelta != nil {
			out = append(out, *e.ToolCallDelta)
		}
	}
	return out
}

func TestStreamParserParallelNativeToolCalls(t *testing.T) {
	p := newStreamParser(readTool())
	events, done, err := consumeAll(t, p,
		lineInit, lineMsgStart, lineThinking,
		lineToolStart0, lineToolDelta0a, lineToolDelta0b, lineToolStop0, lineAssistant0,
		lineDenied, lineDeniedUser,
		lineToolStart1, lineToolDelta1, lineToolStop1,
		lineMsgDelta, lineMsgStop,
		`{"type":"result","subtype":"error_max_turns","is_error":true}`,
	)
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	calls := toolDeltas(events)
	if len(calls) != 2 {
		t.Fatalf("tool calls = %#v", calls)
	}
	if calls[0].Index != 0 || calls[0].ID != "toolu_A" || calls[0].Function.Name != "Read" || calls[0].Function.Arguments != `{"path": "README.md"}` || !calls[0].Final {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].Index != 1 || calls[1].ID != "toolu_B" || calls[1].Function.Arguments != `{"path":"go.mod"}` {
		t.Fatalf("second call = %#v", calls[1])
	}
	last := events[len(events)-1]
	if !last.Done {
		t.Fatalf("last event = %#v", last)
	}
	for _, e := range events {
		if strings.Contains(e.Delta, "denied") {
			t.Fatalf("CLI denial leaked: %#v", e)
		}
	}
	var sawUsage bool
	for _, e := range events {
		if e.Usage.PromptTokens == 112 && e.Usage.CompletionTokens == 40 {
			sawUsage = true
		}
	}
	if !sawUsage {
		t.Fatalf("merged usage missing: %#v", events)
	}
}

func TestStreamParserCustomToolReturnsRawInput(t *testing.T) {
	p := newStreamParser(readTool())
	events, _, err := consumeAll(t, p,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_P","name":"mcp__c__apply_patch","input":{}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"*** Begin Patch\\n*** End Patch\"}"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	calls := toolDeltas(events)
	if len(calls) != 1 || calls[0].Type != "custom" || calls[0].Function.Arguments != "*** Begin Patch\n*** End Patch" {
		t.Fatalf("custom call = %#v", calls)
	}
}

func TestStreamParserTextOnly(t *testing.T) {
	p := newStreamParser(nil)
	events, done, err := consumeAll(t, p, lineMsgStart, lineTextStart,
		textDelta("Hello "), textDelta("world"),
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}}`,
		lineMsgStop)
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	var text strings.Builder
	for _, e := range events {
		text.WriteString(e.Delta)
	}
	if text.String() != "Hello world" || len(toolDeltas(events)) != 0 {
		t.Fatalf("text = %q", text.String())
	}
}

func TestStreamParserAPIErrors(t *testing.T) {
	cases := []struct {
		name   string
		lines  []string
		kind   codex.ErrorKind
		status int
	}{
		{
			name: "model not found",
			lines: []string{
				`{"type":"assistant","error":"model_not_found","message":{"model":"<synthetic>","content":[{"type":"text","text":"There's an issue with the selected model"}]}}`,
				`{"type":"result","subtype":"success","is_error":true,"api_error_status":404,"result":"There's an issue with the selected model (claude-x)."}`,
			},
			kind: codex.ErrorKindClient, status: 404,
		},
		{
			name: "org disabled",
			lines: []string{
				`{"type":"assistant","error":"oauth_org_not_allowed","message":{"content":[]}}`,
				`{"type":"result","subtype":"success","is_error":true,"api_error_status":403,"result":"Your organization has disabled Claude subscription access for Claude Code"}`,
			},
			kind: codex.ErrorKindAuth, status: 403,
		},
		{
			name:  "rate limited",
			lines: []string{`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1790568000}}`},
			kind:  codex.ErrorKindUpstream, status: 429,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events, _, err := consumeAll(t, newStreamParser(nil), tc.lines...)
			if len(events) != 0 {
				t.Fatalf("error text leaked as events: %#v", events)
			}
			typed, ok := codex.ErrorAs(err)
			if !ok || typed.Kind != tc.kind || typed.Status != tc.status {
				t.Fatalf("err = %#v", err)
			}
		})
	}
}

func TestStreamParserIgnoresAllowedRateLimitEvents(t *testing.T) {
	_, _, err := consumeAll(t, newStreamParser(nil), `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","overageStatus":"rejected"}}`)
	if err != nil {
		t.Fatalf("allowed rate limit event must not fail: %v", err)
	}
}

func TestStreamParserToleratesUnexpectedShapes(t *testing.T) {
	p := newStreamParser(readTool())
	events, _, err := consumeAll(t, p,
		`{"type":"system","subtype":"permission_denied","tool_name":"mcp__c__Read","message":"Permission to use mcp__c__Read has been denied"}`,
		`not json at all`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		textDelta("ok"),
	)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(events) != 1 || events[0].Delta != "ok" {
		t.Fatalf("events = %#v", events)
	}
}
