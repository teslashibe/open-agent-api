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

func (r Router) route(req Request) Service {
	if strings.HasPrefix(req.Model, "gemini-") && r.Gemini != nil {
		return r.Gemini
	}
	if isClaudeModel(req.Model) && r.Claude != nil {
		return r.Claude
	}
	if r.Codex != nil {
		return r.Codex
	}
	return UnavailableService{}
}

func isClaudeModel(model string) bool {
	return strings.HasPrefix(model, "claude-") || model == "sonnet" || model == "opus" || model == "haiku" || model == "fable" || model == "mythos"
}
