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
	Gate       <-chan struct{}
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
	route    func(Request) string

	mu            sync.Mutex
	scripts       []Script
	routedScripts map[string][]Script
	requests      []Request
	waiters       map[chan struct{}]struct{}
	active        int
	stateChanged  chan struct{}
}

func NewUpstream(scripts ...Script) *Upstream {
	return newUpstream(nil, nil, scripts...)
}

// NewRoutedUpstream selects scripts by a stable key derived from the recorded
// request. Unlike the FIFO NewUpstream helper, concurrent connection arrival
// order cannot change which synthetic account receives a failure or response.
func NewRoutedUpstream(route func(Request) string, scripts map[string][]Script) *Upstream {
	return newUpstream(route, scripts)
}

func newUpstream(route func(Request) string, routedScripts map[string][]Script, scripts ...Script) *Upstream {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		panic("codextest: listen: " + err.Error())
	}
	u := &Upstream{
		listener:      ln,
		route:         route,
		scripts:       scripts,
		routedScripts: cloneRoutedScripts(routedScripts),
		waiters:       map[chan struct{}]struct{}{},
		stateChanged:  make(chan struct{}),
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

// EnqueueRoute adds a script to one request-routing key.
func (u *Upstream) EnqueueRoute(key string, script Script) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.routedScripts == nil {
		u.routedScripts = map[string][]Script{}
	}
	u.routedScripts[key] = append(u.routedScripts[key], script)
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

// ActiveConnections returns the number of accepted WebSocket connections that
// have not finished or observed a peer disconnect.
func (u *Upstream) ActiveConnections() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.active
}

// WaitIdle waits until every accepted WebSocket connection has exited.
func (u *Upstream) WaitIdle(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		u.mu.Lock()
		if u.active == 0 {
			u.mu.Unlock()
			return true
		}
		changed := u.stateChanged
		u.mu.Unlock()

		select {
		case <-changed:
		case <-timer.C:
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

// GatedFrame emits raw only after gate is closed or receives a value. Closing
// the peer connection cancels the wait without requiring the gate to open.
func GatedFrame(gate <-chan struct{}, raw string) Frame {
	return Frame{Gate: gate, Raw: []byte(raw)}
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
	u.connectionOpened()
	defer u.connectionClosed()
	closed := make(chan struct{})
	defer close(closed)

	_, payload, err := conn.ReadMessage()
	if err != nil {
		return
	}
	request := Request{
		Headers: r.Header.Clone(),
		Payload: append([]byte(nil), payload...),
		Closed:  closed,
	}
	u.record(request)
	script := u.nextScript(request)
	peerClosed := make(chan struct{})
	go func() {
		defer close(peerClosed)
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()
	defer func() {
		_ = conn.Close()
		<-peerClosed
	}()

	for _, frame := range script {
		if !waitFrame(frame, peerClosed) {
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

func (u *Upstream) nextScript(req Request) Script {
	key := ""
	if u.route != nil {
		key = u.route(req)
	}
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.route != nil {
		scripts := u.routedScripts[key]
		if len(scripts) == 0 {
			return nil
		}
		script := scripts[0]
		u.routedScripts[key] = scripts[1:]
		return script
	}
	if len(u.scripts) == 0 {
		return nil
	}
	script := u.scripts[0]
	u.scripts = u.scripts[1:]
	return script
}

func (u *Upstream) connectionOpened() {
	u.mu.Lock()
	u.active++
	u.notifyStateChangedLocked()
	u.mu.Unlock()
}

func (u *Upstream) connectionClosed() {
	u.mu.Lock()
	if u.active > 0 {
		u.active--
	}
	u.notifyStateChangedLocked()
	u.mu.Unlock()
}

func (u *Upstream) notifyStateChangedLocked() {
	close(u.stateChanged)
	u.stateChanged = make(chan struct{})
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

func waitFrame(frame Frame, closed <-chan struct{}) bool {
	if frame.Delay > 0 {
		timer := time.NewTimer(frame.Delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-closed:
			return false
		}
	}
	if frame.Gate == nil {
		return true
	}
	select {
	case <-frame.Gate:
		return true
	case <-closed:
		return false
	}
}

func cloneRoutedScripts(source map[string][]Script) map[string][]Script {
	if source == nil {
		return nil
	}
	cloned := make(map[string][]Script, len(source))
	for key, scripts := range source {
		cloned[key] = append([]Script(nil), scripts...)
	}
	return cloned
}
