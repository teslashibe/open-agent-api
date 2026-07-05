package server

import (
	"context"
	"testing"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/codex"
)

func TestStreamIdleTimeoutPassesEventsThrough(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan codex.StreamEvent)
	out := withStreamIdleTimeout(ctx, in, 500*time.Millisecond)

	go func() {
		for i := 0; i < 20; i++ {
			in <- codex.StreamEvent{Delta: "x"}
			time.Sleep(20 * time.Millisecond)
		}
		close(in)
	}()

	received := 0
	for event := range out {
		if event.Err != nil {
			t.Fatalf("unexpected error event: %v", event.Err)
		}
		received++
	}
	if received != 20 {
		t.Fatalf("received %d events, want 20", received)
	}
}

func TestStreamIdleTimeoutResetsOnEachEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan codex.StreamEvent)
	// Events arrive every 60ms with a 100ms idle limit: the total run far
	// exceeds the idle limit but no single gap does, so no timeout fires.
	out := withStreamIdleTimeout(ctx, in, 100*time.Millisecond)

	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(60 * time.Millisecond)
			in <- codex.StreamEvent{Delta: "x"}
		}
		close(in)
	}()

	for event := range out {
		if event.Err != nil {
			t.Fatalf("idle timeout fired despite steady events: %v", event.Err)
		}
	}
}

func TestStreamIdleTimeoutIgnoresSlowConsumer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan codex.StreamEvent)
	// The consumer sits on the first event for 3x the idle limit while the
	// upstream stays responsive: the timer must not count forwarding time.
	out := withStreamIdleTimeout(ctx, in, 100*time.Millisecond)

	go func() {
		in <- codex.StreamEvent{Delta: "first"}
		in <- codex.StreamEvent{Delta: "second"}
		close(in)
	}()

	first := <-out
	if first.Err != nil {
		t.Fatalf("unexpected error on first event: %v", first.Err)
	}
	time.Sleep(300 * time.Millisecond)
	for event := range out {
		if event.Err != nil {
			t.Fatalf("idle timeout fired while consumer was slow, upstream healthy: %v", event.Err)
		}
	}
}

func TestStreamIdleTimeoutFiresOnSilence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan codex.StreamEvent)
	out := withStreamIdleTimeout(ctx, in, 50*time.Millisecond)

	var last codex.StreamEvent
	for event := range out {
		last = event
	}
	if last.Err == nil {
		t.Fatal("expected an error event after idle silence")
	}
	serviceErr, ok := codex.ErrorAs(last.Err)
	if !ok || serviceErr.Kind != codex.ErrorKindUpstream {
		t.Fatalf("expected upstream service error, got %v", last.Err)
	}
	// The wrapper must NOT cancel: the consumer still needs a live ctx to
	// write the error chunk to the client. Teardown is the caller's job.
	if ctx.Err() != nil {
		t.Fatal("idle timeout must not cancel the request context")
	}
}

func TestStreamIdleTimeoutDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan codex.StreamEvent)
	if got := withStreamIdleTimeout(ctx, in, 0); got != (<-chan codex.StreamEvent)(in) {
		t.Fatal("zero idle timeout should return the input channel unchanged")
	}
}

func TestAgentQueueForProviderIsolation(t *testing.T) {
	opts := options{agentQueues: map[string]*agentQueue{}}
	for _, provider := range []string{codex.ProviderCodex, codex.ProviderGemini, codex.ProviderClaude} {
		opts.agentQueues[provider] = newAgentQueue(true, 1, 1, 10, time.Second, "", false, time.Now, func(string, ...any) {})
	}

	codexQueue := opts.agentQueueFor(codex.ProviderCodex)
	geminiQueue := opts.agentQueueFor(codex.ProviderGemini)
	claudeQueue := opts.agentQueueFor(codex.ProviderClaude)
	if codexQueue == geminiQueue || codexQueue == claudeQueue || geminiQueue == claudeQueue {
		t.Fatal("providers must not share a queue")
	}
	if opts.agentQueueFor("unknown") != codexQueue {
		t.Fatal("unknown provider should fall back to the codex queue")
	}

	// Saturate the codex queue; gemini must still acquire instantly.
	key := newAgentQueueKey("test", "conversation")
	release, _, err := codexQueue.acquire(context.Background(), "req-codex", key, turnClassToolGenerating)
	if err != nil {
		t.Fatalf("codex acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	releaseGemini, wait, err := geminiQueue.acquire(ctx, "req-gemini", key, turnClassToolGenerating)
	if err != nil {
		t.Fatalf("gemini acquire should not block on codex saturation: %v", err)
	}
	defer releaseGemini()
	if wait > 100*time.Millisecond {
		t.Fatalf("gemini acquire waited %s despite its own empty queue", wait)
	}
}
