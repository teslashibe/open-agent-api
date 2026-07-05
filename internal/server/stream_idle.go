package server

import (
	"context"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/codex"
)

// withStreamIdleTimeout guards a provider event stream against silent stalls.
// The timer resets on every upstream event, so a long turn that keeps
// producing is never cut off; a stream that goes quiet for longer than idle
// yields an error event and cancels the upstream request. idle <= 0 disables
// the guard.
func withStreamIdleTimeout(ctx context.Context, cancel context.CancelFunc, in <-chan codex.StreamEvent, idle time.Duration) <-chan codex.StreamEvent {
	if idle <= 0 || in == nil {
		return in
	}
	out := make(chan codex.StreamEvent)
	go func() {
		defer close(out)
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			select {
			case event, ok := <-in:
				if !ok {
					return
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
			case <-timer.C:
				select {
				case out <- codex.StreamEvent{Err: codex.NewError(
					codex.ErrorKindUpstream,
					504,
					"upstream stream idle timeout",
					context.DeadlineExceeded,
				)}:
				case <-ctx.Done():
				}
				cancel()
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
