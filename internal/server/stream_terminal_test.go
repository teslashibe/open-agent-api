package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/config"
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

type failOnDoneWriter struct {
	bytes.Buffer
	failed bool
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
