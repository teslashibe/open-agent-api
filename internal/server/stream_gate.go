package server

import (
	"context"
	"time"

	"github.com/teslashibe/open-agent-api/internal/codex"
)

// gateStreamStart holds a stream's HTTP headers back until the upstream
// produces content, finishes, or fails, for at most gate. An error seen in
// that window is returned so the handler can answer with a real HTTP status
// (Cursor retries 408/409/429/5xx and summarizes on context overflow) instead
// of committing 200 and writing the error as assistant text. Metadata-only
// events (model, id, usage) seen while waiting are replayed in order.
// keepInStream reports errors that must still flow through the stream (the
// in-stream quota fallback handles usage limits). gate <= 0 disables it.
func gateStreamStart(ctx context.Context, events <-chan codex.StreamEvent, gate time.Duration, keepInStream func(error) bool) (<-chan codex.StreamEvent, error) {
	if gate <= 0 || events == nil {
		return events, nil
	}
	timer := time.NewTimer(gate)
	defer timer.Stop()
	var buffered []codex.StreamEvent
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return replayEvents(ctx, buffered, nil), nil
			}
			if event.Err != nil && (keepInStream == nil || !keepInStream(event.Err)) {
				return nil, event.Err
			}
			buffered = append(buffered, event)
			if event.Err != nil || streamEventStartsResponse(event) {
				return replayEvents(ctx, buffered, events), nil
			}
		case <-timer.C:
			return replayEvents(ctx, buffered, events), nil
		case <-ctx.Done():
			return replayEvents(ctx, buffered, events), nil
		}
	}
}

func streamEventStartsResponse(event codex.StreamEvent) bool {
	return event.Done || event.Delta != "" || event.ReasoningDelta != "" || streamEventHasToolCall(event)
}

// replayEvents yields buffered events, then everything from rest (which may
// be nil when the upstream already closed).
func replayEvents(ctx context.Context, buffered []codex.StreamEvent, rest <-chan codex.StreamEvent) <-chan codex.StreamEvent {
	if len(buffered) == 0 {
		if rest == nil {
			return closedEventChannel()
		}
		return rest
	}
	out := make(chan codex.StreamEvent)
	go func() {
		defer close(out)
		for _, event := range buffered {
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
		if rest == nil {
			return
		}
		for event := range rest {
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
