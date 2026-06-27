package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/codextest"
	"github.com/teslashibe/codex-chat-api/internal/config"
)

func TestCodexUpstreamGiantToolStreamForwardsIncrementally(t *testing.T) {
	requireLocalListener(t)
	upstream := codextest.NewUpstream(giantToolScript(t, 2200, "abcd"))
	defer upstream.Close()

	app, logs := newCodexProxyApp(t, upstream.URL())
	proxyURL := startLiveApp(t, app)

	resp := postLiveJSON(t, proxyURL, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"use lookup"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body := readString(t, resp.Body)
	if got := strings.Count(body, `"tool_calls"`); got < 2000 {
		t.Fatalf("tool call SSE chunks = %d, want many incremental chunks", got)
	}
	for _, want := range []string{`"finish_reason":"tool_calls"`, "data: [DONE]\n\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q in body length %d", want, len(body))
		}
	}
	if !strings.Contains(logs.String(), "tool_arg_chars=8800") || !strings.Contains(logs.String(), "tool_deltas=2201") {
		t.Fatalf("logs = %q, want giant tool stream counters", logs.String())
	}
	if !upstream.WaitRequests(1, time.Second) {
		t.Fatal("upstream did not record request")
	}
	reqs := upstream.Requests()
	if reqs[0].Headers.Get("Authorization") != "Bearer secret-access-token" || reqs[0].Headers.Get("Session-Id") == "" {
		t.Fatalf("upstream headers = %#v", reqs[0].Headers)
	}
	if payload, err := codextest.DecodePayload(reqs[0].Payload); err != nil || payload["stream"] != true {
		t.Fatalf("payload = %#v err=%v", payload, err)
	}
}

func TestCodexUpstreamClientCancellationReleasesAgentQueue(t *testing.T) {
	requireLocalListener(t)
	before := runtime.NumGoroutine()
	firstScript := append(codextest.Script{
		codexFrame(t, map[string]any{
			"type":         "response.output_item.added",
			"output_index": 0,
			"item": map[string]any{
				"id":        "fc_cancel",
				"type":      "function_call",
				"call_id":   "call_cancel",
				"name":      "lookup",
				"arguments": "",
			},
		}),
	}, delayedToolArgumentFrames(t, 2200, "zzzz", 2*time.Millisecond)...)
	secondScript := codextest.Script{
		codexFrame(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-second", "model": "gpt-test"}}),
	}
	upstream := codextest.NewUpstream(firstScript, secondScript)
	defer upstream.Close()

	app, logs := newCodexProxyApp(t, upstream.URL())
	proxyURL := startLiveApp(t, app)
	body := `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"use lookup"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`

	resp := postLiveJSON(t, proxyURL, body, map[string]string{"X-Cursor-Session-Id": "cancel-session"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	reader := bufio.NewReader(resp.Body)
	if event := readSSEEvent(t, reader, 2*time.Second); !strings.Contains(event, `"role":"assistant"`) {
		t.Fatalf("first event = %q, want assistant role", event)
	}
	if event := readSSEEvent(t, reader, 2*time.Second); !strings.Contains(event, `"tool_calls"`) {
		t.Fatalf("second event = %q, want tool call", event)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		return strings.Contains(logs.String(), "agent_queue_release request_id=")
	}, "agent queue release after client cancellation")

	resp = postLiveJSON(t, proxyURL, body, map[string]string{"X-Cursor-Session-Id": "cancel-session"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want %d; logs=%q", resp.StatusCode, http.StatusOK, logs.String())
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if !upstream.WaitRequests(2, time.Second) {
		t.Fatalf("upstream requests = %d, want 2", len(upstream.Requests()))
	}
	if strings.Contains(strings.ToLower(logs.String()), "panic") {
		t.Fatalf("logs contain panic: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "outcome=client_disconnect") {
		t.Fatalf("logs = %q, want client disconnect outcome", logs.String())
	}
	waitFor(t, 2*time.Second, func() bool {
		return runtime.NumGoroutine() <= before+20
	}, "goroutines to settle after cancellation")
}

func TestCodexUpstreamReasoningBeforeToolMapsToReasoningContent(t *testing.T) {
	requireLocalListener(t)
	upstream := codextest.NewUpstream(codextest.Script{
		codexFrame(t, map[string]any{"type": "response.output_text.delta", "delta": "I'll inspect the repo now."}),
		codexFrame(t, map[string]any{
			"type":         "response.output_item.added",
			"output_index": 0,
			"item": map[string]any{
				"id":      "fc_123",
				"type":    "function_call",
				"call_id": "call_123",
				"name":    "lookup",
			},
		}),
		codexFrame(t, map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": `{"q":"codex"}`}),
		codexFrame(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-123", "model": "gpt-test"}}),
	})
	defer upstream.Close()

	app, _ := newCodexProxyApp(t, upstream.URL())
	proxyURL := startLiveApp(t, app)
	resp := postLiveJSON(t, proxyURL, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"use lookup"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`, nil)
	defer resp.Body.Close()

	body := readString(t, resp.Body)
	if strings.Contains(body, `"content":"I'll inspect the repo now."`) {
		t.Fatalf("stream leaked pre-tool text as content: %q", body)
	}
	if !strings.Contains(body, `"reasoning_content":"I'll inspect the repo now."`) || !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("stream = %q, want reasoning content and tool finish", body)
	}
}

func TestCodexUpstreamUserTurnTextOnlyAnswerStreamsContent(t *testing.T) {
	requireLocalListener(t)
	upstream := codextest.NewUpstream(codextest.Script{
		codexFrame(t, map[string]any{"type": "response.output_text.delta", "delta": "All done."}),
		codexFrame(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-text-agent", "model": "gpt-test"}}),
	})
	defer upstream.Close()

	app, _ := newCodexProxyApp(t, upstream.URL())
	proxyURL := startLiveApp(t, app)
	resp := postLiveJSON(t, proxyURL, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"summarize"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`, nil)
	defer resp.Body.Close()

	body := readString(t, resp.Body)
	if !strings.Contains(body, `"content":"All done."`) {
		t.Fatalf("stream = %q, want text-only agent answer in content", body)
	}
	if strings.Contains(body, `"reasoning_content":"All done."`) {
		t.Fatalf("stream leaked text-only answer into reasoning_content: %q", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("stream = %q, want stop finish", body)
	}
}

func TestCodexUpstreamTextOnlyFinalStreamsContentAndStopsWithoutRetry(t *testing.T) {
	requireLocalListener(t)
	upstream := codextest.NewUpstream(codextest.Script{
		codexFrame(t, map[string]any{"type": "response.output_text.delta", "delta": "Hello "}),
		codexFrame(t, map[string]any{"type": "response.output_text.delta", "delta": "there."}),
		codexFrame(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-text", "model": "gpt-test"}}),
	})
	defer upstream.Close()

	app, _ := newCodexProxyApp(t, upstream.URL())
	proxyURL := startLiveApp(t, app)
	resp := postLiveJSON(t, proxyURL, `{"model":"gpt-test","stream":true,"faithful":false,"prewarm":false,"messages":[{"role":"user","content":"say hi"}]}`, nil)
	defer resp.Body.Close()

	body := readString(t, resp.Body)
	for _, want := range []string{`"content":"Hello "`, `"content":"there."`, `"finish_reason":"stop"`, "data: [DONE]\n\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream = %q, want %q", body, want)
		}
	}
	if strings.Contains(body, `"reasoning_content"`) || strings.Contains(body, `"tool_calls"`) {
		t.Fatalf("stream = %q, want text-only final answer", body)
	}
	if !upstream.WaitRequests(1, time.Second) || len(upstream.Requests()) != 1 {
		t.Fatalf("upstream requests = %d, want one turn without retry/prewarm", len(upstream.Requests()))
	}
}

func TestCodexUpstreamDegenerateUserTurnRetriesToRequiredToolCall(t *testing.T) {
	requireLocalListener(t)
	upstream := codextest.NewUpstream(
		codextest.Script{
			codexFrame(t, map[string]any{"type": "response.output_text.delta", "delta": "I'll inspect the repo now."}),
			codexFrame(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-degenerate", "model": "gpt-test"}}),
		},
		codextest.Script{
			codexFrame(t, map[string]any{
				"type":         "response.output_item.added",
				"output_index": 0,
				"item": map[string]any{
					"id":      "fc_retry",
					"type":    "function_call",
					"call_id": "call_retry",
					"name":    "lookup",
				},
			}),
			codexFrame(t, map[string]any{"type": "response.function_call_arguments.delta", "output_index": 0, "delta": `{"q":"retry"}`}),
			codexFrame(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-retry", "model": "gpt-test"}}),
		},
	)
	defer upstream.Close()

	app, _ := newCodexProxyApp(t, upstream.URL())
	proxyURL := startLiveApp(t, app)
	resp := postLiveJSON(t, proxyURL, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"use lookup"}],"tools":[{"type":"function","function":{"name":"lookup"}}]}`, nil)
	defer resp.Body.Close()

	body := readString(t, resp.Body)
	if !strings.Contains(body, `"tool_calls"`) || !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("stream = %q, want retried tool call", body)
	}
	if !upstream.WaitRequests(2, time.Second) {
		t.Fatalf("upstream requests = %d, want retry", len(upstream.Requests()))
	}
	reqs := upstream.Requests()
	retry, err := codextest.DecodePayload(reqs[1].Payload)
	if err != nil {
		t.Fatalf("decode retry payload: %v", err)
	}
	if retry["tool_choice"] != "required" {
		t.Fatalf("retry tool_choice = %#v, want required", retry["tool_choice"])
	}
	if got, _ := retry["instructions"].(string); !strings.Contains(got, degenerateRetryInstructions) {
		t.Fatalf("retry instructions = %q, want degenerate retry instruction", got)
	}
}

func TestCodexUpstreamSlowStreamForwardsFirstChunkPromptly(t *testing.T) {
	requireLocalListener(t)
	upstream := codextest.NewUpstream(codextest.Script{
		codexFrame(t, map[string]any{"type": "response.output_text.delta", "delta": "fast"}),
		codextest.DelayedFrame(250*time.Millisecond, string(mustFrameJSON(t, map[string]any{"type": "response.output_text.delta", "delta": "slow"}))),
		codexFrame(t, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-slow", "model": "gpt-test"}}),
	})
	defer upstream.Close()

	app, _ := newCodexProxyApp(t, upstream.URL())
	proxyURL := startLiveApp(t, app)
	start := time.Now()
	resp := postLiveJSON(t, proxyURL, `{"model":"gpt-test","stream":true,"faithful":false,"prewarm":false,"messages":[{"role":"user","content":"say hi"}]}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	reader := bufio.NewReader(resp.Body)
	if event := readSSEEvent(t, reader, 150*time.Millisecond); !strings.Contains(event, `"role":"assistant"`) {
		t.Fatalf("first event = %q, want assistant role", event)
	}
	if event := readSSEEvent(t, reader, 150*time.Millisecond); !strings.Contains(event, `"content":"fast"`) {
		t.Fatalf("second event = %q, want first upstream content", event)
	}
	if elapsed := time.Since(start); elapsed >= 250*time.Millisecond {
		t.Fatalf("first upstream content arrived after %s, want before delayed final frame", elapsed)
	}
}

func newCodexProxyApp(t *testing.T, upstreamURL string) (*fiber.App, *bytes.Buffer) {
	t.Helper()

	authPath, codexHome, profilePath, scaffoldPath := writeCodexClientFixtures(t)
	client, err := codex.NewClient(codex.ClientConfig{
		AuthPath:     authPath,
		CodexHome:    codexHome,
		ProfilePath:  profilePath,
		ScaffoldPath: scaffoldPath,
		WebsocketURL: upstreamURL,
		Timeout:      5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	cfg := config.Defaults()
	cfg.LogBodyShape = true
	cfg.DegenerateTurnRetryEnabled = true
	var logs bytes.Buffer
	app := New(cfg, WithCodexService(client), WithLogOutput(&logs), fixedServerOptions())
	return app, &logs
}

func requireLocalListener(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listeners are unavailable in this environment: %v", err)
	}
	_ = ln.Close()
}

func writeCodexClientFixtures(t *testing.T) (string, string, string, string) {
	t.Helper()

	dir := t.TempDir()
	codexHome := filepath.Join(dir, "codex-home")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(codexHome, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"secret-access-token","account_id":"acct_123"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "installation_id"), []byte("install-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(dir, "profile.json")
	if err := os.WriteFile(profilePath, []byte(`{"model":"gpt-test","instructions":"fixture instructions","tools":[],"tool_choice":"auto","parallel_tool_calls":true,"include":["reasoning.encrypted_content"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	scaffoldPath := filepath.Join(dir, "scaffold.json")
	if err := os.WriteFile(scaffoldPath, []byte(`{"developer_item":{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer"}]},"environment_context":"<environment_context><cwd>old</cwd><current_date>old</current_date><timezone>old</timezone></environment_context>"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return authPath, codexHome, profilePath, scaffoldPath
}

func startLiveApp(t *testing.T, app *fiber.App) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Listener(ln)
	}()
	t.Cleanup(func() {
		_ = app.Shutdown()
		select {
		case err := <-errCh:
			if err != nil {
				t.Logf("app listener stopped with error: %v", err)
			}
		case <-time.After(time.Second):
			t.Log("timed out waiting for app listener shutdown")
		}
	})
	return "http://" + ln.Addr().String()
}

func postLiveJSON(t *testing.T, baseURL string, body string, headers map[string]string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer local-test-token")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	return resp
}

func readSSEEvent(t *testing.T, reader *bufio.Reader, timeout time.Duration) string {
	t.Helper()

	type result struct {
		event string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		var b strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				done <- result{event: b.String(), err: err}
				return
			}
			if line == "\n" || line == "\r\n" {
				done <- result{event: b.String()}
				return
			}
			b.WriteString(line)
		}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("read SSE event: %v event=%q", got.err, got.event)
		}
		return got.event
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for SSE event after %s", timeout)
		return ""
	}
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool, description string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fn() {
		return
	}
	t.Fatalf("timed out waiting for %s", description)
}

func giantToolScript(t *testing.T, count int, fragment string) codextest.Script {
	t.Helper()

	script := codextest.Script{
		codexFrame(t, map[string]any{
			"type":         "response.output_item.added",
			"output_index": 0,
			"item": map[string]any{
				"id":        "fc_giant",
				"type":      "function_call",
				"call_id":   "call_giant",
				"name":      "lookup",
				"arguments": "",
			},
		}),
	}
	for i := 0; i < count; i++ {
		script = append(script, codexFrame(t, map[string]any{
			"type":         "response.function_call_arguments.delta",
			"output_index": 0,
			"delta":        fragment,
		}))
	}
	script = append(script, codexFrame(t, map[string]any{
		"type":     "response.completed",
		"response": map[string]any{"id": "resp-giant", "model": "gpt-test"},
	}))
	return script
}

func delayedToolArgumentFrames(t *testing.T, count int, fragment string, delay time.Duration) codextest.Script {
	t.Helper()

	frames := make(codextest.Script, 0, count)
	for i := 0; i < count; i++ {
		frames = append(frames, codextest.Frame{
			Delay: delay,
			Raw: mustFrameJSON(t, map[string]any{
				"type":         "response.function_call_arguments.delta",
				"output_index": 0,
				"delta":        fragment,
			}),
		})
	}
	return frames
}

func codexFrame(t *testing.T, value map[string]any) codextest.Frame {
	t.Helper()
	return codextest.Frame{Raw: mustFrameJSON(t, value)}
}

func mustFrameJSON(t *testing.T, value map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal frame %#v: %v", value, err)
	}
	return data
}

func TestCodexUpstreamHarnessSupportsDisconnect(t *testing.T) {
	requireLocalListener(t)
	upstream := codextest.NewUpstream(codextest.Script{codextest.DisconnectFrame()})
	defer upstream.Close()

	app, _ := newCodexProxyApp(t, upstream.URL())
	proxyURL := startLiveApp(t, app)
	resp := postLiveJSON(t, proxyURL, `{"model":"gpt-test","stream":true,"faithful":false,"prewarm":false,"messages":[{"role":"user","content":"hi"}]}`, nil)
	defer resp.Body.Close()

	body := readString(t, resp.Body)
	if !strings.Contains(body, "data: ") {
		t.Fatalf("stream = %q, want at least an SSE error chunk after upstream disconnect", body)
	}
}
