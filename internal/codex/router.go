package codex

import (
	"context"
	"strings"
)

// Router sends requests to provider services by model prefix and keeps Codex as
// the default service. It deliberately implements Service so the server layer
// does not need provider-specific branching.
type Router struct {
	Codex  Service
	Gemini Service
	Claude Service
}

func (r Router) Complete(ctx context.Context, req Request) (Completion, error) {
	return r.route(req).Complete(ctx, req)
}

func (r Router) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	return r.route(req).Stream(ctx, req)
}

// Provider names shared by routing and per-provider concerns such as the
// server's agent queues.
const (
	ProviderCodex  = "codex"
	ProviderGemini = "gemini"
	ProviderClaude = "claude"
)

// ProviderForModel is the single source of truth for routing decisions so
// layers above the Router (queueing, logging) agree with where the request
// actually goes.
func ProviderForModel(model string) string {
	// Antigravity gateway IDs (Claude/GPT-OSS via Cloud Code Assist) must win
	// over the Claude Code CLI prefix match.
	if isAntigravityGatewayModel(model) {
		return ProviderGemini
	}
	if strings.HasPrefix(model, "gemini-") {
		return ProviderGemini
	}
	if isClaudeModel(model) {
		return ProviderClaude
	}
	return ProviderCodex
}

func isAntigravityGatewayModel(model string) bool {
	switch model {
	case "claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b-medium":
		return true
	default:
		return false
	}
}

func (r Router) route(req Request) Service {
	switch ProviderForModel(req.Model) {
	case ProviderGemini:
		if r.Gemini != nil {
			return r.Gemini
		}
	case ProviderClaude:
		if r.Claude != nil {
			return r.Claude
		}
	}
	if r.Codex != nil {
		return r.Codex
	}
	return UnavailableService{}
}

func isClaudeModel(model string) bool {
	// Cursor prefixes Claude models as anthropic/claude-...; some clients use api/.
	model = strings.TrimPrefix(model, "anthropic/")
	return strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "api/claude-") || model == "sonnet" || model == "opus" || model == "haiku" || model == "fable"
}
