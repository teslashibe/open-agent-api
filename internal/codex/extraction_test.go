package codex

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/teslashibe/open-agent-api/internal/openai"
)

const extractionFormat = `{"type":"json_schema","name":"report","strict":true,"schema":{"type":"object","additionalProperties":false,"required":["title"],"properties":{"title":{"type":"string"}}}}`

func extractionRequest() Request {
	return Request{
		Model: "gpt-5.6-sol",
		Messages: []openai.ChatMessage{
			{Role: "system", Content: openai.TextContent("extract strictly")},
			{Role: "user", Content: openai.TextContent("the input")},
		},
		ReasoningEffort: "low",
		Verbosity:       "low",
		Extraction:      true,
		ResponseFormat:  json.RawMessage(extractionFormat),
		// A caller that leaves tools populated must still get a tool-free
		// extraction payload.
		Tools:      json.RawMessage(`[{"type":"function","function":{"name":"shell","parameters":{}}}]`),
		ToolChoice: json.RawMessage(`"required"`),
		Faithful:   true,
		Prewarm:    true,
	}
}

// extractionPayloadKeys is the complete set of upstream-supported keys an
// extraction payload may carry. max_output_tokens is deliberately absent:
// Codex Responses honours no output-token cap (issue #126).
var extractionPayloadKeys = []string{
	"type",
	"model",
	"instructions",
	"input",
	"stream",
	"store",
	"reasoning",
	"text",
	"prompt_cache_key",
}

// assertPayloadKeys fails unless payload's keys are exactly want.
func assertPayloadKeys(t *testing.T, payload map[string]any, want []string) {
	t.Helper()
	expected := make(map[string]bool, len(want))
	for _, key := range want {
		expected[key] = true
		if _, present := payload[key]; !present {
			t.Fatalf("extraction payload is missing %q: %v", key, sortedKeys(payload))
		}
	}
	for key := range payload {
		if !expected[key] {
			t.Fatalf("extraction payload carries unsupported key %q: %#v", key, payload[key])
		}
	}
}

func sortedKeys(payload map[string]any) []string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestBuildMinimalEmitsStrictJSONSchemaForExtraction(t *testing.T) {
	builder := fixtureBuilder()

	payload, err := builder.buildMinimal(extractionRequest())
	if err != nil {
		t.Fatalf("buildMinimal() error = %v", err)
	}

	text, ok := payload["text"].(map[string]any)
	if !ok {
		t.Fatalf("text = %#v", payload["text"])
	}
	if text["verbosity"] != "low" {
		t.Fatalf("verbosity = %v", text["verbosity"])
	}
	format, ok := text["format"].(map[string]any)
	if !ok {
		t.Fatalf("text.format = %#v", text["format"])
	}
	if format["type"] != "json_schema" || format["name"] != "report" || format["strict"] != true {
		t.Fatalf("format = %#v", format)
	}
	if _, ok := format["schema"].(map[string]any); !ok {
		t.Fatalf("format.schema = %#v", format["schema"])
	}
	// Pin the exact key set: Codex Responses supports no output cap, so an
	// extraction payload must carry these keys and nothing else. A new key here
	// is a deliberate contract change, not an accident.
	assertPayloadKeys(t, payload, extractionPayloadKeys)
	// The captured CLI profile must never leak into an extraction turn.
	if payload["instructions"] != "extract strictly" {
		t.Fatalf("instructions = %v, want only the caller's system message", payload["instructions"])
	}
}

func TestBuildMinimalExtractionOmitsUnsetOptionals(t *testing.T) {
	builder := fixtureBuilder()
	req := extractionRequest()
	req.ResponseFormat = nil

	payload, err := builder.buildMinimal(req)
	if err != nil {
		t.Fatalf("buildMinimal() error = %v", err)
	}
	text := payload["text"].(map[string]any)
	if _, present := text["format"]; present {
		t.Fatalf("text.format = %#v, want absent", text["format"])
	}
	assertPayloadKeys(t, payload, extractionPayloadKeys)
}

func TestBuildMinimalExtractionRejectsInvalidResponseFormat(t *testing.T) {
	builder := fixtureBuilder()
	req := extractionRequest()
	req.ResponseFormat = json.RawMessage(`{"type":`)

	if _, err := builder.buildMinimal(req); err == nil {
		t.Fatal("buildMinimal() accepted malformed response_format")
	} else if serviceErr, ok := ErrorAs(err); !ok || serviceErr.Kind != ErrorKindClient {
		t.Fatalf("error = %v, want a client error", err)
	}
}

func TestBuildMinimalNonExtractionIsUnchanged(t *testing.T) {
	builder := fixtureBuilder()
	req := extractionRequest()
	req.Extraction = false

	payload, err := builder.buildMinimal(req)
	if err != nil {
		t.Fatalf("buildMinimal() error = %v", err)
	}
	if _, present := payload["tools"]; !present {
		t.Fatal("non-extraction payload lost its tools")
	}
	if _, present := payload["tool_choice"]; !present {
		t.Fatal("non-extraction payload lost its tool_choice")
	}
	if _, present := payload["max_output_tokens"]; present {
		t.Fatal("non-extraction payload gained max_output_tokens")
	}
	text := payload["text"].(map[string]any)
	if _, present := text["format"]; present {
		t.Fatal("non-extraction payload gained text.format")
	}
}

func TestExtractionSkipsPrewarmAndTheFaithfulProfile(t *testing.T) {
	authPath, codexHome := writeAuthFixture(t)
	turnConn := &fakeWebsocketConn{readMessages: [][]byte{
		[]byte(`{"type":"response.output_text.delta","delta":"{\"title\":\"ok\"}"}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp-9","model":"gpt-5.6-sol","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`),
	}}
	fakeDialer := &recordingDialer{conns: []websocketConn{turnConn}}

	client := testClient(t, authPath, codexHome, "ws://example.test/codex")
	client.dial = fakeDialer.dial
	client.builder.newSessionID = func() string { return "session-extract" }
	client.builder.installationID = func() string { return "install-123" }

	completion, err := client.Complete(context.Background(), extractionRequest())
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if completion.Text != `{"title":"ok"}` || completion.ID != "resp-9" {
		t.Fatalf("completion = %#v", completion)
	}
	// Faithful+Prewarm are both set on the request; extraction must still open
	// exactly one connection and take the minimal path.
	if len(fakeDialer.requests) != 1 {
		t.Fatalf("connections = %d, want 1 (no prewarm)", len(fakeDialer.requests))
	}
	if got := fakeDialer.requests[0].headers.Get("X-Codex-Beta-Features"); got != "" {
		t.Fatalf("extraction used faithful CLI headers: %q", got)
	}
	payload := decodeWrittenPayload(t, turnConn)
	if _, present := payload["tools"]; present {
		t.Fatalf("extraction payload carried tools: %#v", payload["tools"])
	}
	if _, present := payload["client_metadata"]; present {
		t.Fatal("extraction payload carried faithful client_metadata")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"json_schema"`) {
		t.Fatalf("extraction payload missing json_schema format: %s", raw)
	}
}
