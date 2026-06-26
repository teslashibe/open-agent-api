package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"

	"github.com/teslashibe/codex-chat-api/internal/auth"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

const (
	codexVersion      = "0.142.0"
	codexUserAgent    = "codex_cli_rs/0.142.0 (Mac OS 26.2.0; arm64) dumb"
	codexAPIAgent     = "codex_cli_rs/0.142.0 (api wrapper) dumb"
	codexBeta         = "responses_websockets=2026-02-06"
	codexBetaFeatures = "remote_compaction_v2"
)

type ClientConfig struct {
	AuthPath     string
	CodexHome    string
	ProfilePath  string
	ScaffoldPath string
	WebsocketURL string
	Timeout      time.Duration
}

type Client struct {
	authPath     string
	codexHome    string
	websocketURL string
	timeout      time.Duration
	dial         websocketDialFunc
	builder      requestBuilder
}

type websocketConn interface {
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	WriteMessage(int, []byte) error
	ReadMessage() (int, []byte, error)
	WriteControl(int, []byte, time.Time) error
	Close() error
}

type websocketDialFunc func(context.Context, string, http.Header) (websocketConn, *http.Response, error)

func NewClient(cfg ClientConfig) (*Client, error) {
	profile, err := LoadProfile(cfg.ProfilePath)
	if err != nil {
		return nil, err
	}
	scaffold, err := LoadScaffold(cfg.ScaffoldPath)
	if err != nil {
		return nil, err
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = time.Minute
	}

	builder := newRequestBuilder(profile, scaffold, cfg.CodexHome)
	builder.cwd = func() string {
		cwd, err := os.Getwd()
		if err != nil || cwd == "" {
			return "."
		}
		return cwd
	}

	return &Client{
		authPath:     cfg.AuthPath,
		codexHome:    cfg.CodexHome,
		websocketURL: cfg.WebsocketURL,
		timeout:      cfg.Timeout,
		dial:         defaultDial(websocket.DefaultDialer),
		builder:      builder,
	}, nil
}

func (c *Client) Complete(ctx context.Context, req Request) (Completion, error) {
	events, err := c.Stream(ctx, req)
	if err != nil {
		return Completion{}, err
	}

	var completion Completion
	for event := range events {
		if event.Err != nil {
			return Completion{}, event.Err
		}
		if event.Delta != "" {
			completion.Text += event.Delta
		}
		if event.ID != "" {
			completion.ID = event.ID
		}
		if event.Model != "" {
			completion.Model = event.Model
		}
		if event.Usage.TotalTokens != 0 || event.Usage.PromptTokens != 0 || event.Usage.CompletionTokens != 0 {
			completion.Usage = event.Usage
		}
	}
	return completion, nil
}

func (c *Client) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	sessionID := c.builder.newSessionID()

	if req.Faithful && req.Prewarm {
		c.prewarm(ctx, req, sessionID)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)

	var payload map[string]any
	var kind requestKind = requestKindTurn
	if req.Faithful {
		payload = c.builder.buildFaithful(req.Messages, req.Model, sessionID, kind, req.ReasoningEffort, req.Verbosity)
	} else {
		payload = c.builder.buildMinimal(req.Messages, req.Model, req.ReasoningEffort, req.Verbosity)
	}

	conn, err := c.open(ctx, req.Faithful, sessionID, kind)
	if err != nil {
		cancel()
		return nil, err
	}
	if err := writePayload(ctx, conn, payload, c.timeout); err != nil {
		cancel()
		closeConn(conn)
		return nil, err
	}

	events := make(chan StreamEvent, 2)
	go c.readLoop(ctx, cancel, conn, events)
	return events, nil
}

func (c *Client) prewarm(ctx context.Context, req Request, sessionID string) {
	ctx, cancel := context.WithTimeout(ctx, minDuration(c.timeout, 2*time.Second))
	defer cancel()

	payload := c.builder.buildFaithful(nil, req.Model, sessionID, requestKindPrewarm, req.ReasoningEffort, req.Verbosity)
	conn, err := c.open(ctx, true, sessionID, requestKindPrewarm)
	if err != nil {
		return
	}
	defer closeConn(conn)

	if err := writePayload(ctx, conn, payload, minDuration(c.timeout, 2*time.Second)); err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(minDuration(c.timeout, 2*time.Second)))
	_, _, _ = conn.ReadMessage()
}

func (c *Client) open(ctx context.Context, faithful bool, sessionID string, kind requestKind) (websocketConn, error) {
	creds, err := auth.Load(c.authPath)
	if err != nil {
		return nil, NewError(ErrorKindAuth, http.StatusUnauthorized, "load codex credentials", err)
	}

	headers := c.headers(creds, faithful, sessionID, kind)
	conn, resp, err := c.dial(ctx, c.websocketURL, headers)
	if err == nil {
		return conn, nil
	}
	status := http.StatusBadGateway
	kindErr := ErrorKindUpstream
	if resp != nil {
		status = resp.StatusCode
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			kindErr = ErrorKindAuth
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, NewError(kindErr, status, "connect to codex websocket", err)
}

func (c *Client) headers(creds auth.Credentials, faithful bool, sessionID string, kind requestKind) http.Header {
	header := http.Header{}
	if !faithful {
		header.Set("authorization", "Bearer "+creds.AccessToken)
		header.Set("chatgpt-account-id", creds.AccountID)
		header.Set("originator", "codex_cli_rs")
		header.Set("user-agent", codexAPIAgent)
		header.Set("version", codexVersion)
		header.Set("openai-beta", codexBeta)
		header.Set("session-id", sessionID)
		return header
	}

	header.Set("chatgpt-account-id", creds.AccountID)
	header.Set("authorization", "Bearer "+creds.AccessToken)
	header.Set("user-agent", codexUserAgent)
	header.Set("originator", "codex_cli_rs")
	header.Set("openai-beta", codexBeta)
	header.Set("version", codexVersion)
	header.Set("x-codex-beta-features", codexBetaFeatures)
	header.Set("x-client-request-id", sessionID)
	header.Set("session-id", sessionID)
	header.Set("thread-id", sessionID)
	header.Set("x-codex-window-id", sessionID+":0")
	header.Set("x-codex-turn-metadata", turnMetadata(c.builder.installationID(), sessionID, string(kind), ""))
	return header
}

func (c *Client) readLoop(ctx context.Context, cancel context.CancelFunc, conn websocketConn, events chan<- StreamEvent) {
	defer cancel()
	defer close(events)
	defer closeConn(conn)

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)

	for {
		_ = conn.SetReadDeadline(time.Now().Add(c.timeout))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				sendStreamEvent(context.Background(), events, StreamEvent{Err: ctx.Err()})
				return
			}
			sendStreamEvent(ctx, events, StreamEvent{Err: NewError(ErrorKindUpstream, http.StatusBadGateway, "read codex websocket", err)})
			return
		}

		event, terminal, err := parseStreamEvent(raw)
		if err != nil {
			sendStreamEvent(ctx, events, StreamEvent{Err: err})
			return
		}
		if hasStreamEvent(event) {
			if !sendStreamEvent(ctx, events, event) {
				sendStreamEvent(context.Background(), events, StreamEvent{Err: ctx.Err()})
				return
			}
		}
		if terminal {
			return
		}
	}
}

func writePayload(ctx context.Context, conn websocketConn, payload map[string]any, timeout time.Duration) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return NewError(ErrorKindClient, http.StatusBadRequest, "encode codex request", err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return NewError(ErrorKindUpstream, http.StatusBadGateway, "set codex websocket write deadline", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return NewError(ErrorKindUpstream, http.StatusBadGateway, "write codex websocket", err)
	}
	return nil
}

func closeConn(conn websocketConn) {
	deadline := time.Now().Add(time.Second)
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), deadline)
	_ = conn.Close()
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func hasStreamEvent(event StreamEvent) bool {
	return event.Delta != "" || event.Done || event.Model != "" || event.ID != "" || event.Usage != (openai.Usage{}) || event.Err != nil
}

func sendStreamEvent(ctx context.Context, events chan<- StreamEvent, event StreamEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func defaultDial(dialer *websocket.Dialer) websocketDialFunc {
	return func(ctx context.Context, url string, headers http.Header) (websocketConn, *http.Response, error) {
		return dialer.DialContext(ctx, url, headers)
	}
}
