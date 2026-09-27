// Package codex runs ChatGPT Codex completions in-process through the same
// pooled account client the open-agent-api gateway uses, for programs that
// embed Codex instead of calling the HTTP gateway. It resolves the gateway's
// model aliases (effort and fast tiers) the same way the gateway does.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	internal "github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/config"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

// Account is one `codex login` credential set.
type Account struct {
	Label     string
	AuthPath  string // e.g. ~/.codex/auth.json; refreshed tokens are written back
	CodexHome string // e.g. ~/.codex
}

type Config struct {
	Accounts []Account
	// ProfilePath and ScaffoldPath are the gateway's codex_profile.json and
	// codex_scaffold.json, shipped at the module root.
	ProfilePath  string
	ScaffoldPath string
	// MaxInflight bounds concurrent requests per account; zero uses the gateway default.
	MaxInflight int
	// Timeout bounds one upstream request; zero uses the gateway default.
	Timeout   time.Duration
	LogOutput io.Writer
}

// Result is one completion. Usage is false when upstream reported no token
// counts; the counts are then zero and must not be treated as measured.
type Result struct {
	Text         string
	Model        string // upstream model that served the request, e.g. gpt-5.6-luna
	Usage        bool
	InputTokens  int
	OutputTokens int
}

type Client struct {
	service internal.Service
}

func New(cfg Config) (*Client, error) {
	if len(cfg.Accounts) == 0 {
		return nil, errors.New("at least one Codex account is required")
	}
	if cfg.LogOutput == nil {
		cfg.LogOutput = io.Discard
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = config.DefaultCodexTimeout
	}
	clients := make([]internal.PooledClientConfig, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		client, err := internal.NewClient(internal.ClientConfig{
			AuthPath:     a.AuthPath,
			CodexHome:    a.CodexHome,
			ProfilePath:  cfg.ProfilePath,
			ScaffoldPath: cfg.ScaffoldPath,
			WebsocketURL: config.DefaultCodexWebsocketURL,
			Timeout:      cfg.Timeout,
			LogOutput:    cfg.LogOutput,
			ClientLabel:  a.Label,
		})
		if err != nil {
			return nil, err
		}
		clients = append(clients, internal.PooledClientConfig{Label: a.Label, Service: client})
	}
	service, err := internal.NewPooledService(internal.PooledServiceConfig{
		Clients:     clients,
		MaxInflight: cfg.MaxInflight,
		LogOutput:   cfg.LogOutput,
	})
	if err != nil {
		return nil, err
	}
	return &Client{service: service}, nil
}

// Complete sends prompt as a single user message to the model alias, as the
// gateway does for a tool-free chat completion.
func (c *Client) Complete(ctx context.Context, model, prompt string) (Result, error) {
	alias := openai.ResolveModelAlias(model)
	content, err := json.Marshal(prompt)
	if err != nil {
		return Result{}, err
	}
	completion, err := c.service.Complete(ctx, internal.Request{
		Model:           alias.UpstreamModel,
		Messages:        []openai.ChatMessage{{Role: "user", Content: content}},
		ReasoningEffort: alias.ReasoningEffort,
		Verbosity:       alias.Verbosity,
		ServiceTier:     alias.ServiceTier,
		Faithful:        true,
		Prewarm:         true,
	})
	if err != nil {
		return Result{}, err
	}
	u := completion.Usage
	return Result{
		Text:         completion.Text,
		Model:        completion.Model,
		Usage:        u.PromptTokens > 0 || u.CompletionTokens > 0,
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
	}, nil
}

// IsCapacity reports whether err means the accounts are out of capacity
// (usage limit, rate limit or no available account) rather than a bad request.
func IsCapacity(err error) bool {
	if errors.Is(err, internal.ErrUsageLimitReached) {
		return true
	}
	var e *internal.Error
	return errors.As(err, &e) && (e.Status == http.StatusTooManyRequests || e.Status == http.StatusServiceUnavailable)
}
