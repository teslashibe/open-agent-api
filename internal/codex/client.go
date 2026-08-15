package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"

	"github.com/teslashibe/open-agent-api/internal/auth"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

const (
	codexVersion      = "0.144.1"
	codexUserAgent    = "codex_cli_rs/0.144.1 (Mac OS 26.2.0; arm64) dumb"
	codexAPIAgent     = "codex_cli_rs/0.144.1 (api wrapper) dumb"
	codexBeta         = "responses_websockets=2026-02-06"
	codexBetaFeatures = "remote_compaction_v2"
)

type ClientConfig struct {
	AuthPath      string
	CodexHome     string
	ProfilePath   string
	ScaffoldPath  string
	WebsocketURL  string
	Timeout       time.Duration
	LogOutput     io.Writer
	LogBodyShape  bool
	LogToolEvents bool
}

type Client struct {
	authPath      string
	tokens        *auth.Source
	codexHome     string
	websocketURL  string
	timeout       time.Duration
	dial          websocketDialFunc
	builder       requestBuilder
	logOutput     io.Writer
	logBodyShape  bool
	logToolEvents bool
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
		authPath:      cfg.AuthPath,
		tokens:        auth.NewSource(cfg.AuthPath),
		codexHome:     cfg.CodexHome,
		websocketURL:  cfg.WebsocketURL,
		timeout:       cfg.Timeout,
		dial:          defaultDial(websocket.DefaultDialer),
		builder:       builder,
		logOutput:     cfg.LogOutput,
		logBodyShape:  cfg.LogBodyShape,
		logToolEvents: cfg.LogToolEvents,
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
		if len(event.ToolCalls) > 0 {
			completion.ToolCalls = append(completion.ToolCalls, event.ToolCalls...)
		}
		if event.ToolCallDelta != nil {
			applyToolCallDelta(&completion.ToolCalls, *event.ToolCallDelta)
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
	// applyToolCallDelta grows the slice to the upstream delta index, which is
	// the codex output_index and can be offset by preceding reasoning items.
	// Drop the resulting phantom (empty) tool calls so callers never see a
	// tool call with no id/name/arguments.
	completion.ToolCalls = compactToolCalls(completion.ToolCalls)
	return completion, nil
}

func compactToolCalls(toolCalls []ToolCall) []ToolCall {
	if len(toolCalls) == 0 {
		return toolCalls
	}
	compact := make([]ToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		if toolCall.ID == "" && toolCall.Function.Name == "" && toolCall.Function.Arguments == "" {
			continue
		}
		compact = append(compact, toolCall)
	}
	return compact
}

func (c *Client) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	sessionID := c.builder.newSessionID()

	// Extraction turns never take the faithful path: the prewarm connection and
	// the 168 KB captured CLI profile/scaffold are pure overhead for structured
	// inference and would reintroduce a tool surface.
	faithful := req.Faithful && !req.Extraction

	if faithful && req.Prewarm {
		c.prewarm(ctx, req, sessionID)
	}

	// Do not bound the entire extraction turn with CODEX_TIMEOUT. Admission and
	// websocket-connect retries must survive transient upstream pressure; the
	// socket read deadline remains the idle-between-frames guard.
	ctx, cancel := context.WithCancel(ctx)

	var payload map[string]any
	var kind requestKind = requestKindTurn
	if faithful {
		payload = c.builder.buildFaithful(req.Messages, req.Model, sessionID, kind, req.ReasoningEffort, req.Verbosity, req.ServiceTier)
	} else {
		var err error
		payload, err = c.builder.buildMinimal(req)
		if err != nil {
			cancel()
			return nil, err
		}
	}

	conn, err := c.openWithRetry(ctx, faithful, sessionID, kind)
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

func (c *Client) openWithRetry(ctx context.Context, faithful bool, sessionID string, kind requestKind) (websocketConn, error) {
	backoff := 250 * time.Millisecond
	for {
		conn, err := c.open(ctx, faithful, sessionID, kind)
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retryableConnectError(err) {
			return nil, err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func retryableConnectError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if codexErr, ok := ErrorAs(err); ok {
		return codexErr.Kind == ErrorKindUpstream && codexErr.Status != http.StatusUnauthorized
	}
	return false
}

func (c *Client) prewarm(ctx context.Context, req Request, sessionID string) {
	ctx, cancel := context.WithTimeout(ctx, minDuration(c.timeout, 2*time.Second))
	defer cancel()

	payload := c.builder.buildFaithful(nil, req.Model, sessionID, requestKindPrewarm, req.ReasoningEffort, req.Verbosity, req.ServiceTier)
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
	creds, err := c.tokens.Get(ctx)
	if err != nil {
		return nil, NewError(ErrorKindAuth, http.StatusUnauthorized, "load codex credentials", err)
	}

	conn, resp, err := c.dial(ctx, c.websocketURL, c.headers(creds, faithful, sessionID, kind))
	if err == nil {
		return conn, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Access token may still look valid but ChatGPT rejected the WS upgrade —
	// force a refresh once and retry before surfacing auth failure.
	if isAuthHandshake(resp) {
		if refreshed, refreshErr := c.tokens.ForceRefresh(ctx); refreshErr == nil {
			conn, resp, err = c.dial(ctx, c.websocketURL, c.headers(refreshed, faithful, sessionID, kind))
			if err == nil {
				return conn, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
	}

	status := http.StatusBadGateway
	kindErr := ErrorKindUpstream
	if resp != nil {
		status = resp.StatusCode
		// A 401 means the loaded credential is invalid. Codex also returns
		// handshake-time 403s transiently when websocket/session capacity is
		// saturated, even while the same account is completing other requests;
		// keep those retryable as an upstream failure.
		if status == http.StatusUnauthorized {
			kindErr = ErrorKindAuth
		}
	}
	connectErr := NewError(kindErr, status, "connect to codex websocket", err)
	if resp != nil {
		retryAfter, resetAt := retryHintFromHeader(resp.Header.Get("Retry-After"), time.Now())
		connectErr = withRetryHint(connectErr, retryAfter, resetAt)
	}
	return nil, connectErr
}

func isAuthHandshake(resp *http.Response) bool {
	return resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden)
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
				trySendStreamEvent(events, StreamEvent{Err: ctx.Err()})
				return
			}
			sendStreamEvent(ctx, events, StreamEvent{Err: NewError(ErrorKindUpstream, http.StatusBadGateway, "read codex websocket", err)})
			return
		}

		c.logCodexToolEvent(raw)
		event, terminal, err := parseStreamEvent(raw)
		if err != nil {
			sendStreamEvent(ctx, events, StreamEvent{Err: err})
			return
		}
		if hasStreamEvent(event) {
			if !sendStreamEvent(ctx, events, event) {
				trySendStreamEvent(events, StreamEvent{Err: ctx.Err()})
				return
			}
		}
		if terminal {
			return
		}
	}
}

func (c *Client) logCodexToolEvent(raw []byte) {
	if !c.logToolEvents || c.logOutput == nil || !isCodexToolEvent(raw) {
		return
	}
	_, _ = fmt.Fprintf(c.logOutput, "codex_tool_event %s\n", redactedCodexToolEventShape(raw))
}

func writePayload(ctx context.Context, conn websocketConn, payload map[string]any, timeout time.Duration) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return NewError(ErrorKindClient, http.StatusBadRequest, "encode codex request", err)
	}
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return NewError(ErrorKindUpstream, http.StatusBadGateway, "set codex websocket write deadline", err)
	}
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- conn.WriteMessage(websocket.TextMessage, data)
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return NewError(ErrorKindUpstream, http.StatusBadGateway, "write codex websocket", err)
		}
		return nil
	case <-ctx.Done():
		_ = conn.Close()
		return ctx.Err()
	}
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
	return event.Delta != "" ||
		event.ReasoningDelta != "" ||
		len(event.ToolCalls) > 0 ||
		event.ToolCallDelta != nil ||
		event.Done ||
		event.Model != "" ||
		event.ID != "" ||
		event.Usage != (openai.Usage{}) ||
		event.Err != nil
}

func applyToolCallDelta(toolCalls *[]ToolCall, delta ToolCallDelta) {
	for len(*toolCalls) <= delta.Index {
		*toolCalls = append(*toolCalls, ToolCall{Type: "function"})
	}

	toolCall := &(*toolCalls)[delta.Index]
	if delta.ID != "" {
		toolCall.ID = delta.ID
	}
	if delta.Type != "" {
		toolCall.Type = delta.Type
	}
	if delta.ThoughtSignature != "" {
		toolCall.ThoughtSignature = delta.ThoughtSignature
	}
	if delta.Function.Name != "" {
		toolCall.Function.Name = delta.Function.Name
	}
	if delta.Function.Arguments != "" {
		if delta.Final {
			toolCall.Function.Arguments = delta.Function.Arguments
		} else {
			toolCall.Function.Arguments += delta.Function.Arguments
		}
	}
}

func sendStreamEvent(ctx context.Context, events chan<- StreamEvent, event StreamEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func trySendStreamEvent(events chan<- StreamEvent, event StreamEvent) bool {
	select {
	case events <- event:
		return true
	default:
		return false
	}
}

func defaultDial(dialer *websocket.Dialer) websocketDialFunc {
	return func(ctx context.Context, url string, headers http.Header) (websocketConn, *http.Response, error) {
		return dialer.DialContext(ctx, url, headers)
	}
}
