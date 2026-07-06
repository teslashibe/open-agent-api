package server

import (
	"context"
	"errors"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/config"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

// buildQuotaFallbackRequest rewrites a codex request onto the configured
// overflow model (default spark), applying that model's hard context budget
// so large conversations fit its smaller window. Returns false when fallback
// is disabled, the request already targets the fallback model, or the
// fallback would cross providers.
func buildQuotaFallbackRequest(req codex.Request, cfg config.Config) (codex.Request, bool) {
	if cfg.QuotaFallbackModel == "" {
		return req, false
	}
	alias := openai.ResolveModelAlias(cfg.QuotaFallbackModel)
	if alias.UpstreamModel == req.Model {
		return req, false
	}
	if codex.ProviderForModel(req.Model) != codex.ProviderCodex ||
		codex.ProviderForModel(alias.UpstreamModel) != codex.ProviderCodex {
		return req, false
	}
	messages := req.Messages
	if alias.ContextHardMaxBytes > 0 {
		managed := manageContext(messages, hardContextConfig(cfg, alias.ContextHardMaxBytes))
		messages = managed.Messages
		messages, _ = dropOldestToFit(messages, alias.ContextHardMaxBytes, hardContextProtectRecent)
	}
	fallback := req
	fallback.Model = alias.UpstreamModel
	fallback.Messages = messages
	fallback.ReasoningEffort = alias.ReasoningEffort
	fallback.Verbosity = alias.Verbosity
	return fallback, true
}

// applyQuotaFallback peeks at the first upstream event; when it is a usage
// limit rejection it restarts the turn on the overflow model and returns that
// stream instead. Any other first event is transparently prepended back onto
// the stream. Single-shot: a fallback stream that itself hits a quota error
// flows through as a normal error.
func applyQuotaFallback(
	ctx context.Context,
	opts options,
	service codex.Service,
	req codex.Request,
	events <-chan codex.StreamEvent,
	streamID string,
) (<-chan codex.StreamEvent, codex.Request) {
	if opts.contextConfig.QuotaFallbackModel == "" {
		return events, req
	}
	var first codex.StreamEvent
	select {
	case event, ok := <-events:
		if !ok {
			return closedEventChannel(), req
		}
		first = event
	case <-ctx.Done():
		return closedEventChannel(), req
	}
	if first.Err == nil || !errors.Is(first.Err, codex.ErrUsageLimitReached) {
		return prependEvent(ctx, first, events), req
	}
	fallbackReq, ok := buildQuotaFallbackRequest(req, opts.contextConfig)
	if !ok {
		return prependEvent(ctx, first, events), req
	}
	fallbackEvents, err := service.Stream(ctx, fallbackReq)
	if err != nil {
		logLine(opts, "quota_fallback_error request_id=%s from=%s to=%s err=%s\n", streamID, req.Model, fallbackReq.Model, detailedError(err))
		return prependEvent(ctx, first, events), req
	}
	logLine(opts, "quota_fallback request_id=%s from=%s to=%s messages=%d\n", streamID, req.Model, fallbackReq.Model, len(fallbackReq.Messages))
	return withStreamIdleTimeout(ctx, fallbackEvents, opts.contextConfig.StreamIdleTimeout), fallbackReq
}

func prependEvent(ctx context.Context, first codex.StreamEvent, rest <-chan codex.StreamEvent) <-chan codex.StreamEvent {
	out := make(chan codex.StreamEvent)
	go func() {
		defer close(out)
		select {
		case out <- first:
		case <-ctx.Done():
			return
		}
		for event := range rest {
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func closedEventChannel() <-chan codex.StreamEvent {
	out := make(chan codex.StreamEvent)
	close(out)
	return out
}
