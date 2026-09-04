package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teslashibe/open-agent-api/internal/auth"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

func TestCompleteUsesPrewarmThenTurnAndAggregatesEvents(t *testing.T) {
	authPath, codexHome := writeAuthFixture(t)
	prewarmConn := &fakeWebsocketConn{readMessages: [][]byte{[]byte(`{"type":"response.created"}`)}}
	turnConn := &fakeWebsocketConn{readMessages: [][]byte{
		[]byte(`{"type":"response.output_text.delta","delta":"Hel"}`),
		[]byte(`{"type":"response.output_text.delta","delta":"lo"}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp-123","model":"gpt-test","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`),
	}}
	fakeDialer := &recordingDialer{conns: []websocketConn{prewarmConn, turnConn}}

	client := testClient(t, authPath, codexHome, "ws://example.test/codex")
	client.dial = fakeDialer.dial
	client.builder.newSessionID = func() string { return "session-123" }
	client.builder.newTurnID = func() string { return "turn-123" }
	client.builder.installationID = func() string { return "install-123" }

	completion, err := client.Complete(context.Background(), Request{
		Model:           "gpt-test",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}},
		ReasoningEffort: "medium",
		Verbosity:       "medium",
		Faithful:        true,
		Prewarm:         true,
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if completion.Text != "Hello" || completion.ID != "resp-123" || completion.Model != "gpt-test" {
		t.Fatalf("completion = %#v", completion)
	}
	if completion.Usage.TotalTokens != 5 || completion.Usage.PromptTokens != 2 || completion.Usage.CompletionTokens != 3 {
		t.Fatalf("usage = %#v", completion.Usage)
	}

	if len(fakeDialer.requests) != 2 {
		t.Fatalf("request count = %d, want prewarm and turn", len(fakeDialer.requests))
	}
	prewarmPayload := decodeWrittenPayload(t, prewarmConn)
	turnPayload := decodeWrittenPayload(t, turnConn)
	prewarmMetadata := prewarmPayload["client_metadata"].(map[string]any)["x-codex-turn-metadata"].(string)
	turnMetadata := turnPayload["client_metadata"].(map[string]any)["x-codex-turn-metadata"].(string)
	if !strings.Contains(prewarmMetadata, `"request_kind": "prewarm"`) {
		t.Fatalf("first request metadata = %s", prewarmMetadata)
	}
	if prewarmPayload["generate"] != false {
		t.Fatalf("prewarm generate = %v", prewarmPayload["generate"])
	}
	if !strings.Contains(turnMetadata, `"request_kind": "turn"`) {
		t.Fatalf("second request metadata = %s", turnMetadata)
	}
	if _, ok := turnPayload["generate"]; ok {
		t.Fatal("turn request unexpectedly includes generate")
	}
	if fakeDialer.requests[0].headers.Get("Session-Id") != "session-123" || fakeDialer.requests[1].headers.Get("Session-Id") != "session-123" {
		t.Fatalf("session headers = %q %q", fakeDialer.requests[0].headers.Get("Session-Id"), fakeDialer.requests[1].headers.Get("Session-Id"))
	}
	if fakeDialer.requests[1].headers.Get("Authorization") != "Bearer secret-access-token" || fakeDialer.requests[1].headers.Get("Chatgpt-Account-Id") != "acct_123" {
		t.Fatalf("auth headers = %#v", fakeDialer.requests[1].headers)
	}
	if !prewarmConn.closed || !turnConn.closed {
		t.Fatalf("connections closed = %v %v", prewarmConn.closed, turnConn.closed)
	}
}

func TestStreamWithOmittedMetricsDoesNotPanic(t *testing.T) {
	authPath, codexHome := writeAuthFixture(t)
	turnConn := &fakeWebsocketConn{readMessages: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp-123","model":"gpt-test"}}`),
	}}
	client := testClient(t, authPath, codexHome, "ws://example.test/codex")
	client.dial = (&recordingDialer{conns: []websocketConn{turnConn}}).dial
	client.builder.newSessionID = func() string { return "session-123" }

	events, err := client.Stream(context.Background(), Request{
		Model:           "gpt-test",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}},
		ReasoningEffort: "medium",
		Verbosity:       "medium",
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	for event := range events {
		if event.Err != nil {
			t.Fatalf("Stream() event error = %v", event.Err)
		}
	}
}

func TestCompleteAggregatesToolCallFrames(t *testing.T) {
	authPath, codexHome := writeAuthFixture(t)
	turnConn := &fakeWebsocketConn{readMessages: [][]byte{
		[]byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_123","type":"function_call","call_id":"call_123","name":"lookup","arguments":""}}`),
		[]byte(`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_123","delta":"{\"q\":"}`),
		[]byte(`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_123","delta":"\"codex\"}"}`),
		[]byte(`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_123","arguments":"{\"q\":\"codex\"}"}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp-123","model":"gpt-test","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}}`),
	}}
	client := testClient(t, authPath, codexHome, "ws://example.test/codex")
	client.dial = (&recordingDialer{conns: []websocketConn{turnConn}}).dial
	client.builder.newSessionID = func() string { return "session-123" }

	completion, err := client.Complete(context.Background(), Request{
		Model:           "gpt-test",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.TextContent("use a tool")}},
		ReasoningEffort: "medium",
		Verbosity:       "medium",
		Faithful:        false,
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if completion.Text != "" || len(completion.ToolCalls) != 1 {
		t.Fatalf("completion = %#v", completion)
	}
	toolCall := completion.ToolCalls[0]
	if toolCall.ID != "call_123" || toolCall.Type != "function" || toolCall.Function.Name != "lookup" || toolCall.Function.Arguments != `{"q":"codex"}` {
		t.Fatalf("tool call = %#v", toolCall)
	}
}

func TestCompleteReturnsContextErrorFromReadLoop(t *testing.T) {
	authPath, codexHome := writeAuthFixture(t)
	client := testClient(t, authPath, codexHome, "ws://example.test/codex")
	client.dial = (&recordingDialer{conns: []websocketConn{&fakeWebsocketConn{}}}).dial
	client.builder.newSessionID = func() string { return "session-123" }

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	completion, err := client.Complete(ctx, Request{
		Model:           "gpt-test",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}},
		ReasoningEffort: "medium",
		Verbosity:       "medium",
		Faithful:        false,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Complete() error = %v, want context.Canceled; completion = %#v", err, completion)
	}
}

func TestStreamDeliversContextErrorAfterBufferedDelta(t *testing.T) {
	authPath, codexHome := writeAuthFixture(t)
	conn := &cancelAfterFirstReadConn{
		first: []byte(`{"type":"response.output_text.delta","delta":"partial"}`),
	}
	client := testClient(t, authPath, codexHome, "ws://example.test/codex")
	client.dial = (&recordingDialer{conns: []websocketConn{conn}}).dial
	client.builder.newSessionID = func() string { return "session-123" }

	ctx, cancel := context.WithCancel(context.Background())
	conn.cancel = cancel
	events, err := client.Stream(ctx, Request{
		Model:           "gpt-test",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}},
		ReasoningEffort: "medium",
		Verbosity:       "medium",
		Faithful:        false,
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}

	var got []StreamEvent
	for event := range events {
		got = append(got, event)
	}
	if len(got) != 2 {
		t.Fatalf("events = %#v, want delta and context error", got)
	}
	if got[0].Delta != "partial" {
		t.Fatalf("first event = %#v", got[0])
	}
	if !errors.Is(got[1].Err, context.Canceled) {
		t.Fatalf("second event error = %v, want context.Canceled", got[1].Err)
	}
}

func TestStreamWriteRespectsContextCancellation(t *testing.T) {
	authPath, codexHome := writeAuthFixture(t)
	conn := newBlockingWriteConn()
	client := testClient(t, authPath, codexHome, "ws://example.test/codex")
	client.dial = (&recordingDialer{conns: []websocketConn{conn}}).dial
	client.builder.newSessionID = func() string { return "session-123" }

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	events, err := client.Stream(ctx, Request{
		Model:           "gpt-test",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}},
		ReasoningEffort: "medium",
		Verbosity:       "medium",
		Faithful:        false,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream() error = %v, want context.Canceled", err)
	}
	if events != nil {
		t.Fatalf("Stream() events = %#v, want nil on canceled write", events)
	}
	if !conn.closed {
		t.Fatal("expected websocket to close after canceled write")
	}
}

func TestPrewarmTimeoutDoesNotCancelTurn(t *testing.T) {
	authPath, codexHome := writeAuthFixture(t)
	turnConn := &fakeWebsocketConn{readMessages: [][]byte{
		[]byte(`{"type":"response.output_text.delta","delta":"ok"}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp-123","model":"gpt-test","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`),
	}}

	client := testClient(t, authPath, codexHome, "ws://example.test/codex")
	client.timeout = 5 * time.Millisecond
	client.builder.newSessionID = func() string { return "session-123" }
	client.builder.newTurnID = func() string { return "turn-123" }
	client.builder.installationID = func() string { return "install-123" }

	var calls int
	client.dial = func(ctx context.Context, url string, headers http.Header) (websocketConn, *http.Response, error) {
		calls++
		if calls == 1 {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("turn context was already canceled: %v", err)
		}
		return turnConn, nil, nil
	}

	completion, err := client.Complete(context.Background(), Request{
		Model:           "gpt-test",
		Messages:        []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}},
		ReasoningEffort: "medium",
		Verbosity:       "medium",
		Faithful:        true,
		Prewarm:         true,
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if completion.Text != "ok" || calls != 2 {
		t.Fatalf("completion = %#v, calls = %d", completion, calls)
	}
}

func TestHeadersDoNotExposeTokenInErrors(t *testing.T) {
	client := &Client{builder: fixtureBuilder()}
	headers := client.headers(auth.Credentials{AccessToken: "secret-access-token", AccountID: "acct_123"}, false, "sid", requestKindTurn)
	if headers.Get("Authorization") != "Bearer secret-access-token" {
		t.Fatalf("authorization header = %q", headers.Get("Authorization"))
	}
	err := NewError(ErrorKindAuth, http.StatusUnauthorized, "authentication failed", nil)
	if strings.Contains(err.Error(), "secret-access-token") {
		t.Fatalf("error leaked token: %v", err)
	}
}

func TestHeadersUseCodex01532Identity(t *testing.T) {
	client := &Client{builder: fixtureBuilder()}
	creds := auth.Credentials{AccessToken: "token", AccountID: "acct_123"}

	tests := []struct {
		name      string
		faithful  bool
		userAgent string
	}{
		{name: "minimal", userAgent: "codex_cli_rs/0.153.2 (api wrapper) dumb"},
		{name: "faithful", faithful: true, userAgent: "codex_cli_rs/0.153.2 (Mac OS 26.2.0; arm64) dumb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := client.headers(creds, tt.faithful, "sid", requestKindTurn)
			if got := headers.Get("Version"); got != "0.153.2" {
				t.Fatalf("version header = %q, want %q", got, "0.153.2")
			}
			if got := headers.Get("User-Agent"); got != tt.userAgent {
				t.Fatalf("user-agent header = %q, want %q", got, tt.userAgent)
			}
			for name, values := range headers {
				for _, value := range values {
					if strings.Contains(value, "0.144.1") {
						t.Fatalf("%s header contains stale Codex identity: %q", name, value)
					}
				}
			}
		})
	}
}

func TestOpenCapturesRetryAfterHeader(t *testing.T) {
	authPath, codexHome := writeAuthFixture(t)
	client := testClient(t, authPath, codexHome, "ws://example.test/codex")
	client.dial = func(context.Context, string, http.Header) (websocketConn, *http.Response, error) {
		return nil, &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"75"}},
		}, errors.New("rate limited")
	}

	_, err := client.open(context.Background(), false, "session-123", requestKindTurn)
	if err == nil {
		t.Fatal("open() error = nil")
	}
	codexErr, ok := ErrorAs(err)
	if !ok || codexErr.Status != http.StatusTooManyRequests || codexErr.RetryAfter != 75*time.Second {
		t.Fatalf("open() error = %#v", codexErr)
	}
}

func TestParseStreamEventFailureIsSanitized(t *testing.T) {
	_, terminal, err := parseStreamEvent([]byte(`{"type":"response.failed","status":500,"error":{"message":"secret-access-token"}}`))
	if err == nil {
		t.Fatal("parseStreamEvent() error = nil")
	}
	if !terminal {
		t.Fatal("terminal = false, want true")
	}
	if strings.Contains(err.Error(), "secret-access-token") {
		t.Fatalf("error leaked payload: %v", err)
	}
}

func TestCodexEventToolCallScaffoldingUnmarshalsJSON(t *testing.T) {
	var event codexEvent
	raw := []byte(`{
		"type":"response.tool_call.delta",
		"tool_calls":[{"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"codex\"}"}}],
		"tool_call_delta":{"index":0,"id":"call_123","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}
	}`)
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(event.ToolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(event.ToolCalls))
	}
	toolCall := event.ToolCalls[0]
	if toolCall.ID != "call_123" || toolCall.Type != "function" || toolCall.Function.Name != "lookup" || toolCall.Function.Arguments != `{"q":"codex"}` {
		t.Fatalf("tool call = %#v", toolCall)
	}
	if event.ToolCallDelta == nil {
		t.Fatal("tool_call_delta = nil")
	}
	delta := event.ToolCallDelta
	if delta.Index != 0 || delta.ID != "call_123" || delta.Type != "function" || delta.Function.Name != "lookup" || delta.Function.Arguments != `{"q":` {
		t.Fatalf("tool call delta = %#v", delta)
	}
}

type recordedCodexRequest struct {
	url     string
	headers http.Header
}

func testClient(t *testing.T, authPath, codexHome, url string) *Client {
	t.Helper()
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.json")
	scaffoldPath := filepath.Join(dir, "scaffold.json")
	if err := os.WriteFile(profilePath, []byte(`{"model":"gpt-test","instructions":"fixture instructions","tools":[],"tool_choice":"auto","parallel_tool_calls":true,"include":["reasoning.encrypted_content"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scaffoldPath, []byte(`{"developer_item":{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer"}]},"environment_context":"<environment_context><cwd>old</cwd><current_date>old</current_date><timezone>old</timezone></environment_context>"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(ClientConfig{
		AuthPath:     authPath,
		CodexHome:    codexHome,
		ProfilePath:  profilePath,
		ScaffoldPath: scaffoldPath,
		WebsocketURL: url,
		Timeout:      5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	client.builder.cwd = func() string { return "/tmp/work" }
	client.builder.now = func() time.Time { return time.Date(2026, 6, 26, 12, 0, 0, 0, time.FixedZone("PDT", -7*60*60)) }
	return client
}

func writeAuthFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"secret-access-token","account_id":"acct_123"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "installation_id"), []byte("install-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return authPath, dir
}

type recordingDialer struct {
	mu       sync.Mutex
	conns    []websocketConn
	requests []recordedCodexRequest
}

func (d *recordingDialer) dial(ctx context.Context, url string, headers http.Header) (websocketConn, *http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.conns) == 0 {
		return nil, nil, errors.New("unexpected dial")
	}
	conn := d.conns[0]
	d.conns = d.conns[1:]
	d.requests = append(d.requests, recordedCodexRequest{url: url, headers: headers.Clone()})
	return conn, nil, nil
}

type fakeWebsocketConn struct {
	mu           sync.Mutex
	readMessages [][]byte
	writes       [][]byte
	closed       bool
}

func (c *fakeWebsocketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeWebsocketConn) SetWriteDeadline(time.Time) error { return nil }

func (c *fakeWebsocketConn) WriteMessage(_ int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), data...))
	return nil
}

func (c *fakeWebsocketConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.readMessages) == 0 {
		return 0, nil, io.EOF
	}
	message := c.readMessages[0]
	c.readMessages = c.readMessages[1:]
	return 1, message, nil
}

func (c *fakeWebsocketConn) WriteControl(int, []byte, time.Time) error { return nil }

func (c *fakeWebsocketConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

type blockingWriteConn struct {
	fakeWebsocketConn
	unlocked chan struct{}
}

func newBlockingWriteConn() *blockingWriteConn {
	return &blockingWriteConn{unlocked: make(chan struct{})}
}

func (c *blockingWriteConn) WriteMessage(_ int, data []byte) error {
	c.mu.Lock()
	c.writes = append(c.writes, append([]byte(nil), data...))
	c.mu.Unlock()
	<-c.unlocked
	return io.ErrClosedPipe
}

func (c *blockingWriteConn) Close() error {
	select {
	case <-c.unlocked:
	default:
		close(c.unlocked)
	}
	return c.fakeWebsocketConn.Close()
}

type cancelAfterFirstReadConn struct {
	fakeWebsocketConn
	first  []byte
	cancel context.CancelFunc
	reads  int
}

func (c *cancelAfterFirstReadConn) ReadMessage() (int, []byte, error) {
	c.reads++
	if c.reads == 1 {
		return 1, c.first, nil
	}
	c.cancel()
	return 0, nil, io.EOF
}

func decodeWrittenPayload(t *testing.T, conn *fakeWebsocketConn) map[string]any {
	t.Helper()
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if len(conn.writes) != 1 {
		t.Fatalf("writes = %d, want 1", len(conn.writes))
	}
	var payload map[string]any
	if err := json.Unmarshal(conn.writes[0], &payload); err != nil {
		t.Fatalf("decode write: %v", err)
	}
	return payload
}
