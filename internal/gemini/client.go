package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/teslashibe/open-agent-api/internal/codex"
)

const defaultModel = "gemini-2.5-flash"

type Config struct {
	AuthPath string
	Endpoint string
	Project  string
	Timeout  time.Duration
	// HeaderTimeout bounds the wait for response headers; the stream idle
	// guard only attaches after headers arrive, so without this a stalled
	// upstream could sit silent until the full Timeout.
	HeaderTimeout time.Duration
	HTTPClient    *http.Client
}

type Client struct {
	endpoint string
	project  string
	timeout  time.Duration
	http     *http.Client
	tokens   interface {
		Token(context.Context) (string, error)
	}

	projectMu sync.Mutex
	loaded    string
}

func NewClient(cfg Config) (*Client, error) {
	if cfg.AuthPath == "" {
		return nil, fmt.Errorf("gemini auth path is required")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("gemini endpoint is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		// No http.Client.Timeout: it caps the whole exchange including the
		// streaming body, which kills long streams. The per-request context
		// (cfg.Timeout total, plus the server's idle guard) governs instead;
		// HeaderTimeout covers the pre-headers window the idle guard cannot.
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if cfg.HeaderTimeout > 0 {
			transport.ResponseHeaderTimeout = cfg.HeaderTimeout
		}
		hc = &http.Client{Transport: transport}
	}
	return &Client{
		endpoint: strings.TrimRight(cfg.Endpoint, "/"),
		project:  cfg.Project,
		timeout:  cfg.Timeout,
		http:     hc,
		tokens:   newTokenSource(cfg.AuthPath, hc, time.Now),
	}, nil
}

func (c *Client) Stream(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	project, err := c.projectID(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	if req.Model == "" {
		req.Model = defaultModel
	}
	customTools := customToolNames(req.Tools)
	body := buildGenerateContentRequest(req, project, req.RequestID)
	raw, err := json.Marshal(body)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("encode gemini request: %w", err)
	}
	httpReq, err := c.newJSONRequest(ctx, http.MethodPost, c.endpoint+":streamGenerateContent?alt=sse", raw)
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		cancel()
		return nil, codex.NewError(codex.ErrorKindUpstream, 502, "gemini stream request failed", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		err := decodeErrorResponse(resp)
		cancel()
		return nil, err
	}

	events := make(chan codex.StreamEvent)
	go c.readSSE(ctx, cancel, resp.Body, events, customTools)
	return events, nil
}

func (c *Client) Complete(ctx context.Context, req codex.Request) (codex.Completion, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	project, err := c.projectID(ctx)
	if err != nil {
		return codex.Completion{}, err
	}
	if req.Model == "" {
		req.Model = defaultModel
	}
	customTools := customToolNames(req.Tools)
	body := buildGenerateContentRequest(req, project, req.RequestID)
	raw, err := json.Marshal(body)
	if err != nil {
		return codex.Completion{}, fmt.Errorf("encode gemini request: %w", err)
	}
	httpReq, err := c.newJSONRequest(ctx, http.MethodPost, c.endpoint+":generateContent", raw)
	if err != nil {
		return codex.Completion{}, err
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return codex.Completion{}, codex.NewError(codex.ErrorKindUpstream, 502, "gemini request failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return codex.Completion{}, decodeErrorResponse(resp)
	}
	var frame streamFrame
	if err := json.NewDecoder(resp.Body).Decode(&frame); err != nil {
		return codex.Completion{}, fmt.Errorf("decode gemini response: %w", err)
	}
	if frame.Error != nil {
		return codex.Completion{}, geminiAPIError(frame.Error)
	}
	if frame.Response == nil {
		return codex.Completion{}, nil
	}
	completion := codex.Completion{Model: frame.Response.ModelVersion, ID: frame.Response.ResponseID}
	completion.Usage.PromptTokens = frame.Response.UsageMetadata.PromptTokenCount
	completion.Usage.CompletionTokens = frame.Response.UsageMetadata.CandidatesTokenCount
	completion.Usage.TotalTokens = frame.Response.UsageMetadata.TotalTokenCount
	for _, cand := range frame.Response.Candidates {
		for i, p := range cand.Content.Parts {
			if p.Text != "" && !p.Thought {
				completion.Text += p.Text
			}
			if p.FunctionCall != nil {
				delta := geminiToolCallDelta(frame.Response.ResponseID, i, p.FunctionCall, p.ThoughtSignature, customTools)
				completion.ToolCalls = append(completion.ToolCalls, codex.ToolCall{
					ID:               delta.ID,
					Type:             delta.Type,
					ThoughtSignature: delta.ThoughtSignature,
					Function: codex.ToolCallFunction{
						Name:      delta.Function.Name,
						Arguments: delta.Function.Arguments,
					},
				})
			}
		}
	}
	return completion, nil
}

func (c *Client) readSSE(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, out chan<- codex.StreamEvent, customTools map[string]bool) {
	defer cancel()
	defer close(out)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var data bytes.Buffer
	flush := func() bool {
		if data.Len() == 0 {
			return true
		}
		events, err := parseStreamEvent(bytes.TrimSpace(data.Bytes()), customTools)
		data.Reset()
		if err != nil {
			select {
			case out <- codex.StreamEvent{Err: err}:
			case <-ctx.Done():
			}
			return false
		}
		for _, event := range events {
			select {
			case out <- event:
			case <-ctx.Done():
				return false
			}
		}
		return true
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !flush() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if data.Len() > 0 {
		_ = flush()
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		select {
		case out <- codex.StreamEvent{Err: codex.NewError(codex.ErrorKindUpstream, 502, "gemini stream read failed", err)}:
		case <-ctx.Done():
		}
	}
}

func (c *Client) projectID(ctx context.Context) (string, error) {
	if c.project != "" {
		return c.project, nil
	}
	c.projectMu.Lock()
	defer c.projectMu.Unlock()
	if c.loaded != "" {
		return c.loaded, nil
	}
	raw := []byte(`{"metadata":{"ideType":"ANTIGRAVITY","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}}`)
	req, err := c.newJSONRequest(ctx, http.MethodPost, c.endpoint+":loadCodeAssist", raw)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", codex.NewError(codex.ErrorKindUpstream, 502, "gemini project discovery failed", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", decodeErrorResponse(resp)
	}
	var body struct {
		Project string `json:"cloudaicompanionProject"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode gemini project discovery response: %w", err)
	}
	if body.Project == "" {
		return "", codex.NewError(codex.ErrorKindUpstream, 502, "gemini project discovery returned no project", nil)
	}
	c.loaded = body.Project
	return c.loaded, nil
}

func (c *Client) newJSONRequest(ctx context.Context, method, url string, body []byte) (*http.Request, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, codex.NewError(codex.ErrorKindAuth, 401, err.Error(), err)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build gemini request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "antigravity/1.15.8 darwin/arm64")
	req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
	req.Header.Set("Client-Metadata", `{"ideType":"ANTIGRAVITY","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`)
	return req, nil
}

func decodeErrorResponse(resp *http.Response) error {
	var frame streamFrame
	if err := json.NewDecoder(resp.Body).Decode(&frame); err == nil && frame.Error != nil {
		return geminiAPIError(frame.Error)
	}
	message := fmt.Sprintf("gemini upstream returned status %d", resp.StatusCode)
	var wrap error
	if resp.StatusCode == http.StatusTooManyRequests {
		wrap = fmt.Errorf("%w: %s", codex.ErrUsageLimitReached, message)
	}
	return codex.NewError(codex.ErrorKindUpstream, resp.StatusCode, message, wrap)
}
