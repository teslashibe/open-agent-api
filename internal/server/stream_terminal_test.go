package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/config"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

func TestDeliverToolStreamCountsSuccessOnlyAfterDoneDelivery(t *testing.T) {
	events := make(chan codex.StreamEvent, 2)
	events <- codex.StreamEvent{Delta: "ok"}
	events <- codex.StreamEvent{Done: true}
	close(events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &failOnDoneWriter{}
	w := bufio.NewWriter(sink)
	terminal, _, _, upstreamEvents, _, _, _, _, _, _ := deliverToolStream(
		ctx,
		streamTerminalTestOptions(),
		w,
		cancel,
		codex.Request{Model: "gpt-test"},
		nil,
		events,
		"chatcmpl-test",
		123,
		"gpt-test",
		"chatcmpl-test",
		time.Now(),
	)

	if terminal.outcome != streamOutcomeCanceled || terminal.result() != requestResultCanceled {
		t.Fatalf("terminal = %#v, want canceled", terminal)
	}
	if terminal.phase != codex.PhaseMidStream {
		t.Fatalf("phase = %q, want %q", terminal.phase, codex.PhaseMidStream)
	}
	if upstreamEvents != 2 {
		t.Fatalf("upstream events = %d, want 2", upstreamEvents)
	}
	if !sink.failed {
		t.Fatal("test writer did not reject the terminal DONE marker")
	}
}

func TestDeliverToolStreamRecordsSuccessAfterDoneDelivery(t *testing.T) {
	events := make(chan codex.StreamEvent, 1)
	events <- codex.StreamEvent{Done: true}
	close(events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sink bytes.Buffer
	w := bufio.NewWriter(&sink)
	terminal, _, _, _, _, _, _, _, _, _ := deliverToolStream(
		ctx,
		streamTerminalTestOptions(),
		w,
		cancel,
		codex.Request{Model: "gpt-test"},
		nil,
		events,
		"chatcmpl-test",
		123,
		"gpt-test",
		"chatcmpl-test",
		time.Now(),
	)

	if terminal != successfulStreamTerminal() {
		t.Fatalf("terminal = %#v, want %#v", terminal, successfulStreamTerminal())
	}
	if !bytes.HasSuffix(sink.Bytes(), []byte("data: [DONE]\n\n")) {
		t.Fatalf("stream missing terminal DONE: %q", sink.String())
	}
}

func TestDeliverToolStreamRejectsMissingUpstreamTerminalEvent(t *testing.T) {
	tests := []struct {
		name   string
		events []codex.StreamEvent
		phase  codex.FailurePhase
	}{
		{
			name:  "before content",
			phase: codex.PhaseFirstEvent,
		},
		{
			name:   "after delivered content",
			events: []codex.StreamEvent{{Delta: "partial"}},
			phase:  codex.PhaseMidStream,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := make(chan codex.StreamEvent, len(tt.events))
			for _, event := range tt.events {
				events <- event
			}
			close(events)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var sink bytes.Buffer
			terminal, _, _, _, _, _, _, _, _, _ := deliverToolStream(
				ctx,
				streamTerminalTestOptions(),
				bufio.NewWriter(&sink),
				cancel,
				codex.Request{Model: "gpt-test"},
				nil,
				events,
				"chatcmpl-test",
				123,
				"gpt-test",
				"chatcmpl-test",
				time.Now(),
			)

			if terminal.outcome != streamOutcomeUpstreamError || terminal.result() != requestResultUpstreamError {
				t.Fatalf("terminal = %#v, want upstream error", terminal)
			}
			if terminal.phase != tt.phase {
				t.Fatalf("phase = %q, want %q", terminal.phase, tt.phase)
			}
			if terminal.failureClass != string(codex.FailureTransient) {
				t.Fatalf("failure class = %q, want %q", terminal.failureClass, codex.FailureTransient)
			}
			if !bytes.Contains(sink.Bytes(), []byte("[error: upstream error]")) {
				t.Fatalf("stream missing sanitized error chunk: %q", sink.String())
			}
		})
	}
}

func TestDeliverToolStreamFailedFirstContentWriteIsFirstEventCancellation(t *testing.T) {
	events := make(chan codex.StreamEvent, 2)
	events <- codex.StreamEvent{Delta: "first"}
	events <- codex.StreamEvent{Done: true}
	close(events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &failOnSubstringWriter{needle: []byte(`"content":"first"`)}
	terminal, deltas, _, _, _, _, _, _, _, _ := deliverToolStream(
		ctx,
		streamTerminalTestOptions(),
		bufio.NewWriter(sink),
		cancel,
		codex.Request{Model: "gpt-test"},
		nil,
		events,
		"chatcmpl-test",
		123,
		"gpt-test",
		"chatcmpl-test",
		time.Now(),
	)

	if terminal.outcome != streamOutcomeCanceled || terminal.phase != codex.PhaseFirstEvent {
		t.Fatalf("terminal = %#v, want first-event cancellation", terminal)
	}
	if deltas != 0 {
		t.Fatalf("delivered deltas = %d, want 0", deltas)
	}
	if !sink.failed {
		t.Fatal("test writer did not reject the first content delta")
	}
}

func TestDeliverToolStreamBufferedToolCallDoesNotAdvanceDeliveryPhase(t *testing.T) {
	events := make(chan codex.StreamEvent, 2)
	events <- codex.StreamEvent{ToolCallDelta: &codex.ToolCallDelta{
		Index: 0,
		ID:    "call-test",
		Type:  "function",
		Function: codex.ToolCallFunctionDelta{
			Name:      "lookup",
			Arguments: `{}`,
		},
	}}
	events <- codex.StreamEvent{Err: codex.NewError(
		codex.ErrorKindUpstream,
		http.StatusBadGateway,
		"upstream failed",
		errors.New("tool stream failed"),
	)}
	close(events)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sink bytes.Buffer
	terminal, _, _, _, _, _, _, _, _, _ := deliverToolStream(
		ctx,
		streamTerminalTestOptions(),
		bufio.NewWriter(&sink),
		cancel,
		codex.Request{
			Model:    "gpt-test",
			Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("use lookup")}},
			Tools:    json.RawMessage(`[{"type":"function"}]`),
		},
		nil,
		events,
		"chatcmpl-test",
		123,
		"gpt-test",
		"chatcmpl-test",
		time.Now(),
	)

	if terminal.outcome != streamOutcomeUpstreamError || terminal.phase != codex.PhaseFirstEvent {
		t.Fatalf("terminal = %#v, want first-event upstream error", terminal)
	}
	if bytes.Contains(sink.Bytes(), []byte(`"tool_calls"`)) {
		t.Fatalf("buffered tool call was unexpectedly delivered: %q", sink.String())
	}
}

type failOnDoneWriter struct {
	bytes.Buffer
	failed bool
}

type failOnSubstringWriter struct {
	bytes.Buffer
	needle []byte
	failed bool
}

func (w *failOnSubstringWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, w.needle) {
		w.failed = true
		return 0, errors.New("matched write failed")
	}
	return w.Buffer.Write(data)
}

func (w *failOnDoneWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte("[DONE]")) {
		w.failed = true
		return 0, errors.New("terminal write failed")
	}
	return w.Buffer.Write(data)
}

func streamTerminalTestOptions() options {
	cfg := config.Defaults()
	cfg.StreamIdleTimeout = 0
	return options{
		now:           time.Now,
		logOutput:     io.Discard,
		contextConfig: cfg,
	}
}
