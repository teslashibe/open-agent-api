package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/config"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

func TestHealth(t *testing.T) {
	app := New(config.Defaults())

	req, err := http.NewRequest(http.MethodGet, "/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status body = %q, want ok", body["status"])
	}
}

func TestChatCompletionsNonStreamingSuccess(t *testing.T) {
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			if req.Model != "gpt-test" {
				t.Fatalf("Model = %q, want gpt-test", req.Model)
			}
			if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
				t.Fatalf("Messages = %#v", req.Messages)
			}
			return codex.Completion{
				ID:    "chatcmpl-test",
				Model: req.Model,
				Text:  "hello from fake",
				Usage: openai.Usage{
					PromptTokens:     2,
					CompletionTokens: 3,
					TotalTokens:      5,
				},
			}, nil
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var body openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != "chatcmpl-test" || body.Object != "chat.completion" || body.Model != "gpt-test" {
		t.Fatalf("unexpected response metadata: %#v", body)
	}
	if got := string(body.Choices[0].Message.Content); got != `"hello from fake"` {
		t.Fatalf("message content = %s, want quoted hello", got)
	}
	if body.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %#v", body.Usage)
	}
}

func TestChatCompletionsStreamingSuccess(t *testing.T) {
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			events := make(chan codex.StreamEvent, 3)
			events <- codex.StreamEvent{Delta: "Hel"}
			events <- codex.StreamEvent{Delta: "lo"}
			events <- codex.StreamEvent{Done: true}
			close(events)
			return events, nil
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(data)
	for _, event := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		if !strings.HasPrefix(event, "data: ") {
			t.Fatalf("SSE event missing data prefix: %q in body %q", event, body)
		}
	}
	if !strings.Contains(body, `"role":"assistant"`) {
		t.Fatalf("stream missing assistant role chunk: %q", body)
	}
	if !strings.Contains(body, `"content":"Hel"`) || !strings.Contains(body, `"content":"lo"`) {
		t.Fatalf("stream missing content chunks: %q", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream = %q, want terminal DONE event", body)
	}
}

func TestChatCompletionsUpstreamErrorIsSanitized(t *testing.T) {
	const secret = "secret-access-token"
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			return codex.Completion{}, codex.NewError(
				codex.ErrorKindUpstream,
				http.StatusBadGateway,
				"upstream unavailable "+secret,
				errors.New("backend included "+secret),
			)
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	body := readString(t, resp.Body)
	if !strings.Contains(body, "upstream error") {
		t.Fatalf("body = %q, want sanitized upstream message", body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("response leaked secret: %q", body)
	}
}

func TestChatCompletionsAuthErrorIsSanitized(t *testing.T) {
	const secret = "secret-access-token"
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			return codex.Completion{}, codex.NewError(
				codex.ErrorKindAuth,
				http.StatusUnauthorized,
				"bad token "+secret,
				errors.New("authorization header had "+secret),
			)
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	body := readString(t, resp.Body)
	if !strings.Contains(body, "authentication failed") {
		t.Fatalf("body = %q, want sanitized auth message", body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("response leaked secret: %q", body)
	}
}

func TestChatCompletionsStreamingErrorIsSanitized(t *testing.T) {
	const secret = "secret-access-token"
	service := fakeCodexService{
		stream: func(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
			events := make(chan codex.StreamEvent, 1)
			events <- codex.StreamEvent{
				Err: codex.NewError(
					codex.ErrorKindUpstream,
					http.StatusBadGateway,
					"raw upstream "+secret,
					errors.New("payload contained "+secret),
				),
			}
			close(events)
			return events, nil
		},
	}
	app := New(config.Defaults(), WithCodexService(service), fixedServerOptions())

	resp := doJSON(t, app, `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readString(t, resp.Body)
	if !strings.Contains(body, "[error: upstream error]") {
		t.Fatalf("body = %q, want sanitized streaming error", body)
	}
	if strings.Contains(body, secret) {
		t.Fatalf("response leaked secret: %q", body)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream = %q, want terminal DONE event", body)
	}
}

func TestChatCompletionsBadRequestIsSanitized(t *testing.T) {
	const secret = "secret-access-token"
	app := New(config.Defaults(), WithCodexService(fakeCodexService{}), fixedServerOptions())

	resp := doJSON(t, app, `{"messages":`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	body := readString(t, resp.Body)
	if strings.Contains(body, secret) {
		t.Fatalf("response leaked secret: %q", body)
	}

	resp = doJSON(t, app, `{"messages":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty messages status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestChatCompletionsCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	var sawCanceledContext bool
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			select {
			case <-ctx.Done():
				sawCanceledContext = true
				return codex.Completion{}, ctx.Err()
			case <-time.After(time.Second):
				t.Fatal("service did not receive canceled context")
				return codex.Completion{}, nil
			}
		},
	}
	app := New(
		config.Defaults(),
		WithCodexService(service),
		fixedServerOptions(),
		func(opts *options) {
			opts.requestContext = func(*fiber.Ctx) context.Context {
				return canceled
			}
		},
	)

	resp := doJSON(t, app, `{"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != 499 {
		t.Fatalf("status = %d, want 499", resp.StatusCode)
	}
	if !sawCanceledContext {
		t.Fatal("service did not observe canceled context")
	}
}

type fakeCodexService struct {
	complete func(context.Context, codex.Request) (codex.Completion, error)
	stream   func(context.Context, codex.Request) (<-chan codex.StreamEvent, error)
}

func (f fakeCodexService) Complete(ctx context.Context, req codex.Request) (codex.Completion, error) {
	if f.complete == nil {
		return codex.Completion{}, errors.New("unexpected Complete call")
	}
	return f.complete(ctx, req)
}

func (f fakeCodexService) Stream(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
	if f.stream == nil {
		return nil, errors.New("unexpected Stream call")
	}
	return f.stream(ctx, req)
}

func fixedServerOptions() Option {
	return func(opts *options) {
		opts.now = func() time.Time {
			return time.Unix(123, 0)
		}
		opts.newID = func() string {
			return "chatcmpl-fixed"
		}
	}
}

func doJSON(t *testing.T, app interface {
	Test(*http.Request, ...int) (*http.Response, error)
}, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-access-token")
	resp, err := app.Test(req, 2000)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	return resp
}

func readString(t *testing.T, r io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(data)
}
