package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/config"
	"github.com/teslashibe/codex-chat-api/internal/openai"
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
