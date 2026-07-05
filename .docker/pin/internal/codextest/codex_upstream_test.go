package codextest

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWaitRequestsRemovesTimedOutWaiters(t *testing.T) {
	requireLocalListener(t)
	upstream := NewUpstream()
	defer upstream.Close()

	if upstream.WaitRequests(1, time.Millisecond) {
		t.Fatal("WaitRequests returned true without a request")
	}

	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.waiters) != 0 {
		t.Fatalf("waiters len = %d, want 0", len(upstream.waiters))
	}
}

func TestDelayedFrameStopsWhenConnectionCloses(t *testing.T) {
	requireLocalListener(t)
	upstream := NewUpstream(Script{
		DelayedFrame(time.Hour, `{"type":"response.completed"}`),
	})
	defer upstream.Close()

	conn, _, err := websocket.DefaultDialer.Dial(upstream.URL(), http.Header{})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatalf("WriteMessage() error = %v", err)
	}
	if !upstream.WaitRequests(1, time.Second) {
		t.Fatal("upstream did not record request")
	}
	req := upstream.Requests()[0]
	if err := conn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := WaitClosed(ctx, req.Closed); err != nil {
		t.Fatal(err)
	}
}

func requireLocalListener(t *testing.T) {
	t.Helper()

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listeners are unavailable in this environment: %v", err)
	}
	_ = ln.Close()
}
