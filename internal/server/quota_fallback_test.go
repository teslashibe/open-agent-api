package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/open-chat-api/internal/codex"
	"github.com/teslashibe/open-chat-api/internal/config"
	"github.com/teslashibe/open-chat-api/internal/openai"
)

func quotaErr() error {
	return codex.NewError(codex.ErrorKindUpstream, 429, "usage limit reached", fmt.Errorf("%w: quota", codex.ErrUsageLimitReached))
}

type fallbackService struct {
	streamed []codex.Request
	events   func(req codex.Request) []codex.StreamEvent
}

func (s *fallbackService) Complete(context.Context, codex.Request) (codex.Completion, error) {
	return codex.Completion{}, errors.New("not used")
}

func (s *fallbackService) Stream(_ context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
	s.streamed = append(s.streamed, req)
	out := make(chan codex.StreamEvent, 8)
	for _, event := range s.events(req) {
		out <- event
	}
	close(out)
	return out, nil
}

func fallbackTestOptions() options {
	cfg := config.Defaults()
	return options{
		contextConfig: cfg,
		now:           time.Now,
	}
}

func TestBuildQuotaFallbackRequestSwapsModelAndTrims(t *testing.T) {
	big := strings.Repeat("z", 4096)
	messages := []openai.ChatMessage{{Role: "system", Content: openai.TextContent("sys")}}
	for i := 0; i < 60; i++ {
		messages = append(messages,
			openai.ChatMessage{Role: "user", Content: openai.TextContent(big)},
			openai.ChatMessage{Role: "assistant", Content: openai.TextContent(big)},
		)
	}
	req := codex.Request{Model: "gpt-5.5", Messages: messages}
	fallback, ok := buildQuotaFallbackRequest(req, config.Defaults())
	if !ok {
		t.Fatal("expected fallback to apply")
	}
	if fallback.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("fallback model = %q", fallback.Model)
	}
	if !fallback.AllowCooling {
		t.Fatal("fallback must allow the existing model-overflow attempt when all clients are cooling")
	}
	if len(fallback.Messages) >= len(messages) {
		t.Fatalf("fallback did not trim: %d messages", len(fallback.Messages))
	}
	if fallback.Messages[0].Role != "system" {
		t.Fatal("system message lost in fallback trim")
	}
}

func TestBuildQuotaFallbackRequestNoopCases(t *testing.T) {
	cfg := config.Defaults()
	if _, ok := buildQuotaFallbackRequest(codex.Request{Model: "gpt-5.3-codex-spark"}, cfg); ok {
		t.Fatal("must not fall back onto itself")
	}
	if _, ok := buildQuotaFallbackRequest(codex.Request{Model: "gemini-2.5-pro"}, cfg); ok {
		t.Fatal("must not cross providers")
	}
	cfg.QuotaFallbackModel = ""
	if _, ok := buildQuotaFallbackRequest(codex.Request{Model: "gpt-5.5"}, cfg); ok {
		t.Fatal("must respect disabled fallback")
	}
}

func TestApplyQuotaFallbackSwitchesStreamOnQuotaError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	service := &fallbackService{events: func(req codex.Request) []codex.StreamEvent {
		return []codex.StreamEvent{{Delta: "from " + req.Model}, {Done: true}}
	}}
	original := make(chan codex.StreamEvent, 1)
	original <- codex.StreamEvent{Err: quotaErr()}
	close(original)

	req := codex.Request{Model: "gpt-5.5", Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}}}
	events, gotReq := applyQuotaFallback(ctx, fallbackTestOptions(), service, req, original, "stream-1")
	if gotReq.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("request model = %q", gotReq.Model)
	}
	first := <-events
	if first.Err != nil || first.Delta != "from gpt-5.3-codex-spark" {
		t.Fatalf("first event = %#v", first)
	}
	if len(service.streamed) != 1 {
		t.Fatalf("fallback stream calls = %d", len(service.streamed))
	}
}

func TestApplyQuotaFallbackPassesThroughNormalEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	service := &fallbackService{events: func(codex.Request) []codex.StreamEvent { return nil }}
	original := make(chan codex.StreamEvent, 2)
	original <- codex.StreamEvent{Delta: "hello"}
	original <- codex.StreamEvent{Done: true}
	close(original)

	req := codex.Request{Model: "gpt-5.5"}
	events, gotReq := applyQuotaFallback(ctx, fallbackTestOptions(), service, req, original, "stream-2")
	if gotReq.Model != "gpt-5.5" {
		t.Fatalf("request model changed: %q", gotReq.Model)
	}
	first := <-events
	if first.Delta != "hello" {
		t.Fatalf("first event = %#v", first)
	}
	if len(service.streamed) != 0 {
		t.Fatal("fallback stream should not have been called")
	}
}

func TestApplyQuotaFallbackPassesThroughOtherErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	service := &fallbackService{events: func(codex.Request) []codex.StreamEvent { return nil }}
	original := make(chan codex.StreamEvent, 1)
	original <- codex.StreamEvent{Err: errors.New("boom")}
	close(original)

	events, _ := applyQuotaFallback(ctx, fallbackTestOptions(), service, codex.Request{Model: "gpt-5.5"}, original, "stream-3")
	first := <-events
	if first.Err == nil || first.Err.Error() != "boom" {
		t.Fatalf("first event = %#v", first)
	}
	if len(service.streamed) != 0 {
		t.Fatal("fallback must not trigger on non-quota errors")
	}
}

func TestApplyQuotaFallbackUsesAccountRotationBeforeModelOverflow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var firstCalls, secondCalls int
	var secondModel string
	pool, err := codex.NewPooledService(codex.PooledServiceConfig{
		UnavailablePolicy: codex.ClientPoolUnavailableFail,
		LogOutput:         io.Discard,
		Clients: []codex.PooledClientConfig{
			{Label: "first", Service: &streamFuncService{stream: func(codex.Request) (<-chan codex.StreamEvent, error) {
				firstCalls++
				return streamEvents(codex.StreamEvent{Err: quotaErr()}), nil
			}}},
			{Label: "second", Service: &streamFuncService{stream: func(req codex.Request) (<-chan codex.StreamEvent, error) {
				secondCalls++
				secondModel = req.Model
				return streamEvents(codex.StreamEvent{Delta: "rotated"}, codex.StreamEvent{Done: true}), nil
			}}},
		},
	})
	if err != nil {
		t.Fatalf("NewPooledService() error = %v", err)
	}
	req := requestForServerPoolIndex(pool, 0)
	req.Model = "gpt-5.5"
	events, err := pool.Stream(ctx, req)
	if err != nil {
		t.Fatalf("pool.Stream() error = %v", err)
	}
	events, gotReq := applyQuotaFallback(ctx, fallbackTestOptions(), pool, req, events, "stream-rotate")
	first := <-events
	if first.Err != nil || first.Delta != "rotated" {
		t.Fatalf("first event = %#v", first)
	}
	if gotReq.Model != req.Model || secondModel != req.Model {
		t.Fatalf("models = request:%q selected:%q", gotReq.Model, secondModel)
	}
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("calls = first:%d second:%d", firstCalls, secondCalls)
	}
}

func TestApplyQuotaFallbackRunsOverflowAfterPoolExhaustion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int
	var models []string
	pool, err := codex.NewPooledService(codex.PooledServiceConfig{
		UnavailablePolicy: codex.ClientPoolUnavailableFail,
		LogOutput:         io.Discard,
		Clients: []codex.PooledClientConfig{{Label: "only", Service: &streamFuncService{stream: func(req codex.Request) (<-chan codex.StreamEvent, error) {
			calls++
			models = append(models, req.Model)
			if calls == 1 {
				return streamEvents(codex.StreamEvent{Err: quotaErr()}), nil
			}
			return streamEvents(codex.StreamEvent{Delta: "overflow"}, codex.StreamEvent{Done: true}), nil
		}}}},
	})
	if err != nil {
		t.Fatalf("NewPooledService() error = %v", err)
	}
	req := codex.Request{Model: "gpt-5.5", Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}}}
	original, err := pool.Stream(ctx, req)
	if err != nil {
		t.Fatalf("pool.Stream() error = %v", err)
	}
	events, gotReq := applyQuotaFallback(ctx, fallbackTestOptions(), pool, req, original, "stream-exhausted")
	first := <-events
	if first.Err != nil || first.Delta != "overflow" {
		t.Fatalf("first event = %#v", first)
	}
	if gotReq.Model != "gpt-5.3-codex-spark" || fmt.Sprint(models) != "[gpt-5.5 gpt-5.3-codex-spark]" {
		t.Fatalf("request model = %q, attempted models = %v", gotReq.Model, models)
	}
}

func TestStreamingConnectQuotaRunsModelFallback(t *testing.T) {
	var calls int
	var models []string
	service := &streamFuncService{stream: func(req codex.Request) (<-chan codex.StreamEvent, error) {
		calls++
		models = append(models, req.Model)
		if calls == 1 {
			return nil, quotaErr()
		}
		return streamEvents(codex.StreamEvent{Delta: "overflow"}, codex.StreamEvent{Done: true}), nil
	}}
	app := New(config.Defaults(), WithCodexService(service), WithLogOutput(io.Discard), fixedServerOptions())

	resp := doJSON(t, app, `{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := readString(t, resp.Body)
	if !strings.Contains(body, `"content":"overflow"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream = %q", body)
	}
	if calls != 2 || fmt.Sprint(models) != "[gpt-5.5 gpt-5.3-codex-spark]" {
		t.Fatalf("calls = %d, models = %v", calls, models)
	}
}

func TestAllCoolingRateLimitDoesNotRunModelFallback(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var calls int
			var models []string
			rateLimitErr := codex.NewError(codex.ErrorKindUpstream, http.StatusTooManyRequests, "too many requests", errors.New("capacity"))
			pool, err := codex.NewPooledService(codex.PooledServiceConfig{
				UnavailablePolicy: codex.ClientPoolUnavailableFail,
				LogOutput:         io.Discard,
				Clients: []codex.PooledClientConfig{{Label: "only", Service: &streamFuncService{stream: func(req codex.Request) (<-chan codex.StreamEvent, error) {
					calls++
					models = append(models, req.Model)
					return nil, rateLimitErr
				}}}},
			})
			if err != nil {
				t.Fatalf("NewPooledService() error = %v", err)
			}
			var logs synchronizedBuffer
			app := New(config.Defaults(), WithCodexService(pool), WithLogOutput(&logs), fixedServerOptions())
			body := fmt.Sprintf(`{"model":"gpt-5.5","stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream)

			for attempt := 1; attempt <= 2; attempt++ {
				resp := doJSON(t, app, body)
				responseBody := readString(t, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusTooManyRequests {
					t.Fatalf("attempt %d status = %d, want %d; body = %q", attempt, resp.StatusCode, http.StatusTooManyRequests, responseBody)
				}
			}

			if calls != 1 || fmt.Sprint(models) != "[gpt-5.5]" {
				t.Fatalf("calls = %d, models = %v; model fallback must not run", calls, models)
			}
			if strings.Contains(logs.String(), "quota_fallback") {
				t.Fatalf("logs = %q, ordinary rate limit triggered model fallback", logs.String())
			}
		})
	}
}

type streamFuncService struct {
	stream func(codex.Request) (<-chan codex.StreamEvent, error)
}

func (s *streamFuncService) Complete(context.Context, codex.Request) (codex.Completion, error) {
	return codex.Completion{}, errors.New("not used")
}

func (s *streamFuncService) Stream(_ context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
	return s.stream(req)
}

func streamEvents(events ...codex.StreamEvent) <-chan codex.StreamEvent {
	out := make(chan codex.StreamEvent, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out
}

func requestForServerPoolIndex(pool *codex.PooledService, want int) codex.Request {
	// PooledService does not expose shard internals. Find a key by observing
	// which service receives a successful request would make this helper
	// stateful, so use the stable SHA-256 shard formula documented by the pool.
	for i := 0; ; i++ {
		key := fmt.Sprintf("server-pool-%d", i)
		sum := sha256.Sum256([]byte(key))
		if int(binary.BigEndian.Uint64(sum[:8])%2) == want {
			return codex.Request{AffinityKey: key}
		}
	}
}
