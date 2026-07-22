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

func TestRoutedScriptsFollowRequestKeyNotArrivalOrder(t *testing.T) {
	requireLocalListener(t)
	releaseA := make(chan struct{})
	upstream := NewRoutedUpstream(
		func(req Request) string { return req.Headers.Get("Chatgpt-Account-Id") },
		map[string][]Script{
			"account-a": {{
				GatedFrame(releaseA, `{"type":"response.output_text.delta","delta":"from-a"}`),
				TextFrame(`{"type":"response.completed"}`),
			}},
			"account-b": {{
				TextFrame(`{"type":"response.output_text.delta","delta":"from-b"}`),
				TextFrame(`{"type":"response.completed"}`),
			}},
		},
	)
	defer upstream.Close()

	connA := dialTestUpstream(t, upstream, "account-a")
	defer connA.Close()
	connB := dialTestUpstream(t, upstream, "account-b")
	defer connB.Close()

	if got := readTestFrame(t, connB); got != `{"type":"response.output_text.delta","delta":"from-b"}` {
		t.Fatalf("account-b frame = %q", got)
	}
	close(releaseA)
	if got := readTestFrame(t, connA); got != `{"type":"response.output_text.delta","delta":"from-a"}` {
		t.Fatalf("account-a frame = %q", got)
	}
}

func TestGatedFrameCancellationAndIdleAccounting(t *testing.T) {
	requireLocalListener(t)
	gate := make(chan struct{})
	upstream := NewUpstream(Script{
		GatedFrame(gate, `{"type":"response.completed"}`),
	})
	defer upstream.Close()

	conn := dialTestUpstream(t, upstream, "account-a")
	if !upstream.WaitRequests(1, time.Second) {
		t.Fatal("upstream did not record request")
	}
	if got := upstream.ActiveConnections(); got != 1 {
		t.Fatalf("ActiveConnections() = %d, want 1", got)
	}
	if upstream.WaitIdle(time.Millisecond) {
		t.Fatal("WaitIdle returned true while gated connection was active")
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
	if !upstream.WaitIdle(time.Second) {
		t.Fatalf("active connections = %d, want idle", upstream.ActiveConnections())
	}
}

func dialTestUpstream(t *testing.T, upstream *Upstream, account string) *websocket.Conn {
	t.Helper()
	header := http.Header{"Chatgpt-Account-Id": []string{account}}
	conn, _, err := websocket.DefaultDialer.Dial(upstream.URL(), header)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
		_ = conn.Close()
		t.Fatalf("WriteMessage() error = %v", err)
	}
	return conn
}

func readTestFrame(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("ReadMessage() returned an empty frame")
	}
	return string(raw)
}

func requireLocalListener(t *testing.T) {
	t.Helper()

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listeners are unavailable in this environment: %v", err)
	}
	_ = ln.Close()
}
