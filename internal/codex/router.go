package codex

import (
	"context"
	"strings"
)

// Router sends requests to Gemini by model prefix and keeps Codex as the
// default service. It deliberately implements Service so the server layer does
// not need provider-specific branching.
type Router struct {
	Codex  Service
	Gemini Service
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
	if r.Codex != nil {
		return r.Codex
	}
	return UnavailableService{}
}
