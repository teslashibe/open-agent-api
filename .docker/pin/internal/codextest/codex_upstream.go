package codextest

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Frame struct {
	Raw        []byte
	Delay      time.Duration
	Disconnect bool
}

type Script []Frame

type Request struct {
	Headers http.Header
	Payload []byte
	Closed  <-chan struct{}
}

type Upstream struct {
	server   *http.Server
	listener net.Listener
	upgrader websocket.Upgrader

	mu       sync.Mutex
	scripts  []Script
	requests []Request
	waiters  map[chan struct{}]struct{}
}

func NewUpstream(scripts ...Script) *Upstream {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		panic("codextest: listen: " + err.Error())
	}
	u := &Upstream{
		listener: ln,
		scripts:  scripts,
		waiters:  map[chan struct{}]struct{}{},
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
	u.server = &http.Server{Handler: http.HandlerFunc(u.serve)}
	go func() {
		_ = u.server.Serve(ln)
	}()
	return u
}

func (u *Upstream) Close() {
	_ = u.server.Close()
}

func (u *Upstream) URL() string {
	return "ws://" + u.listener.Addr().String()
}

func (u *Upstream) Enqueue(script Script) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.scripts = append(u.scripts, script)
}

func (u *Upstream) Requests() []Request {
	u.mu.Lock()
	defer u.mu.Unlock()

	requests := make([]Request, len(u.requests))
	for i, req := range u.requests {
		requests[i] = Request{
			Headers: req.Headers.Clone(),
			Payload: append([]byte(nil), req.Payload...),
			Closed:  req.Closed,
		}
	}
	return requests
}

func (u *Upstream) WaitRequests(n int, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		u.mu.Lock()
		if len(u.requests) >= n {
			u.mu.Unlock()
			return true
		}
		waiter := make(chan struct{})
		u.waiters[waiter] = struct{}{}
		u.mu.Unlock()

		select {
		case <-waiter:
		case <-timer.C:
			u.removeWaiter(waiter)
			return false
		}
	}
}

func DecodePayload(raw []byte) (map[string]any, error) {
	var payload map[string]any
	err := json.Unmarshal(raw, &payload)
	return payload, err
}

func TextFrame(raw string) Frame {
	return Frame{Raw: []byte(raw)}
}

func DelayedFrame(delay time.Duration, raw string) Frame {
	return Frame{Delay: delay, Raw: []byte(raw)}
}

func DisconnectFrame() Frame {
	return Frame{Disconnect: true}
}

func WaitClosed(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return errors.New("connection did not close before deadline")
	}
}

func (u *Upstream) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := u.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	closed := make(chan struct{})
	defer close(closed)

	script := u.nextScript()
	_, payload, err := conn.ReadMessage()
	if err != nil {
		return
	}
	u.record(Request{
		Headers: r.Header.Clone(),
		Payload: append([]byte(nil), payload...),
		Closed:  closed,
	})
	peerClosed := make(chan struct{})
	go func() {
		defer close(peerClosed)
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	for _, frame := range script {
		if !waitFrameDelay(frame.Delay, peerClosed) {
			return
		}
		if frame.Disconnect {
			return
		}
		if len(frame.Raw) == 0 {
			continue
		}
		if err := conn.WriteMessage(websocket.TextMessage, frame.Raw); err != nil {
			return
		}
	}
}

func (u *Upstream) nextScript() Script {
	u.mu.Lock()
	defer u.mu.Unlock()

	if len(u.scripts) == 0 {
		return nil
	}
	script := u.scripts[0]
	u.scripts = u.scripts[1:]
	return script
}

func (u *Upstream) record(req Request) {
	u.mu.Lock()
	u.requests = append(u.requests, req)
	waiters := u.waiters
	u.waiters = map[chan struct{}]struct{}{}
	u.mu.Unlock()

	for waiter := range waiters {
		close(waiter)
	}
}

func (u *Upstream) removeWaiter(waiter chan struct{}) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.waiters, waiter)
}

func waitFrameDelay(delay time.Duration, closed <-chan struct{}) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-closed:
		return false
	}
}
