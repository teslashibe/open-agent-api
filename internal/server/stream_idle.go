package server

import (
	"context"
	"time"

	"github.com/teslashibe/open-agent-api/internal/codex"
)

// withStreamIdleTimeout guards a provider event stream against silent stalls.
// The timer runs only while waiting on the upstream — it is stopped during
// the downstream forward, so a slow client applying backpressure never counts
// as upstream silence. On expiry the timeout is delivered as an ordinary
// error event and the wrapper returns WITHOUT cancelling: the consumer must
// still be able to write the error chunk to the client (writeSSE checks ctx),
// and the caller's deferred cancel tears down the upstream once the stream
// finishes. idle <= 0 disables the guard.
func withStreamIdleTimeout(ctx context.Context, in <-chan codex.StreamEvent, idle time.Duration) <-chan codex.StreamEvent {
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
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				if !ok {
					return
				}
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
				timer.Reset(idle)
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
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
