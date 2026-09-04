package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/google/uuid"

	"github.com/teslashibe/open-agent-api/internal/buildinfo"
	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/config"
	metricspkg "github.com/teslashibe/open-agent-api/internal/metrics"
	"github.com/teslashibe/open-agent-api/internal/openai"
	"github.com/teslashibe/open-agent-api/internal/sse"
	"github.com/teslashibe/open-agent-api/internal/structured"
)

type Option func(*options)

type options struct {
	codexService       codex.Service
	requestContext     func(*fiber.Ctx) context.Context
	now                func() time.Time
	newID              func() string
	logOutput          io.Writer
	logMu              *sync.Mutex
	logBodyShape       bool
	logRequestIdentity bool
	agentQueueKeyMode  string
	agentQueues        map[string]*agentQueue
	contextConfig      config.Config
	drain              *atomic.Bool
	isLocal            func(*fiber.Ctx) bool
	metrics            *metricspkg.Metrics
	usageMonitor       *codex.UsageMonitor
	// Structured inference shares the Codex admission queue with interactive
	// traffic. Interactive requests receive a higher queue priority while
	// queued batch extraction uses every otherwise-idle slot.
	structuredQueue       *agentQueue
	structuredPolicy      structured.Policy
	structuredIdempotency *structured.IdempotencyStore
}

var structuredIdempotencyEntries = structured.DefaultIdempotencyEntries

// agentQueueFor returns the provider's queue. Each provider gets its own queue
// so heavy traffic to one upstream never starves the others.
func (o options) agentQueueFor(provider string) *agentQueue {
	if queue, ok := o.agentQueues[provider]; ok {
		return queue
	}
	return o.agentQueues[codex.ProviderCodex]
}

func WithCodexService(service codex.Service) Option {
	return func(opts *options) {
		opts.codexService = service
	}
}

func WithLogOutput(output io.Writer) Option {
	return func(opts *options) {
		opts.logOutput = output
	}
}

// WithMetrics shares one process-local registry across the server, queues, and
// Codex client pool.
func WithMetrics(metrics *metricspkg.Metrics) Option {
	return func(opts *options) {
		opts.metrics = metrics
	}
}

func WithUsageMonitor(monitor *codex.UsageMonitor) Option {
	return func(opts *options) {
		opts.usageMonitor = monitor
	}
}

// withStructuredIdempotency injects a pre-seeded or shorter-TTL idempotency
// store so tests can exercise replay without waiting on the real TTL.
func withStructuredIdempotency(store *structured.IdempotencyStore) Option {
	return func(opts *options) {
		opts.structuredIdempotency = store
	}
}

// withLocalCheck overrides the loopback detection used by the drain controls.
// app.Test() connections are not loopback, so tests that need to exercise the
// positive drain path inject their own predicate.
func withLocalCheck(isLocal func(*fiber.Ctx) bool) Option {
	return func(opts *options) {
		opts.isLocal = isLocal
	}
}

func newStructuredIdempotencyStore(cfg config.Config, opts options) *structured.IdempotencyStore {
	if cfg.StructuredEnabled {
		logLine(opts, "structured_idempotency backend=memory scope=process-local replicas=1 rollout_requirement=%q\n",
			"maxSurge=0 or strategy Recreate")
	}
	return structured.NewIdempotencyStore(cfg.StructuredIdempotencyTTL, structuredIdempotencyEntries, opts.now)
}

func New(cfg config.Config, setters ...Option) *fiber.App {
	opts := options{
		codexService: codex.UnavailableService{},
		requestContext: func(c *fiber.Ctx) context.Context {
			return c.UserContext()
		},
		now: time.Now,
		newID: func() string {
			return "chatcmpl-" + uuid.NewString()
		},
		logOutput:          os.Stdout,
		logMu:              &sync.Mutex{},
		logBodyShape:       cfg.LogBodyShape,
		logRequestIdentity: cfg.LogRequestIdentity,
		agentQueueKeyMode:  cfg.AgentQueueKeyMode,
		contextConfig:      cfg,
		drain:              &atomic.Bool{},
		isLocal: func(c *fiber.Ctx) bool {
			return net.ParseIP(c.IP()).IsLoopback()
		},
		metrics: metricspkg.New(cfg.MetricsEnabled),
	}
	for _, setter := range setters {
		setter(&opts)
	}
	if opts.agentQueues == nil {
		opts.agentQueues = map[string]*agentQueue{}
		for _, provider := range []string{codex.ProviderCodex, codex.ProviderGemini, codex.ProviderClaude} {
			limits := cfg.AgentQueueLimitsFor(provider)
			opts.agentQueues[provider] = newAgentQueue(
				cfg.AgentQueueEnabled,
				limits.MaxActive,
				limits.MaxActivePerKey,
				limits.QueueLimit,
				cfg.AgentQueueTimeout,
				agentQueueLockDirFor(cfg.AgentQueueLockDir, provider),
				cfg.AgentQueuePriorityEnabled,
				opts.now,
				func(format string, args ...any) {
					logLine(opts, "provider="+provider+" "+format, args...)
				},
			).withMetrics(provider, opts.metrics)
		}
	}
	if opts.structuredQueue == nil {
		if cfg.AgentQueueEnabled {
			opts.structuredQueue = opts.agentQueueFor(codex.ProviderCodex)
		} else {
			opts.structuredQueue = newAgentQueue(
				true,
				cfg.StructuredMaxActive,
				cfg.StructuredMaxActivePerKey,
				cfg.StructuredQueueLimit,
				cfg.StructuredQueueTimeout,
				"",
				false,
				opts.now,
				func(format string, args ...any) {
					logLine(opts, "structured "+format, args...)
				},
			).withMetrics("structured", opts.metrics)
		}
	}
	if len(opts.structuredPolicy.Models()) == 0 {
		models := cfg.StructuredModels
		if len(models) == 0 {
			models = structured.DefaultModels()
		}
		opts.structuredPolicy = structured.NewPolicy(models, cfg.ProviderEnabled)
	}
	if opts.structuredIdempotency == nil {
		opts.structuredIdempotency = newStructuredIdempotencyStore(cfg, opts)
	}
	opts.structuredIdempotency.WithObserver(opts.metrics.ObserveStructuredIdempotency)
	opts.metrics.AllowStructuredModels(opts.structuredPolicy.Models())

	app := fiber.New(fiber.Config{
		AppName:               "open-agent-api",
		DisableStartupMessage: true,
	})

	app.Use(recover.New())
	// gzip / brotli / deflate when the client sends Accept-Encoding. SSE chat
	// streams stay uncompressed so flushes are not buffered into one blob.
	app.Use(compress.New(compress.Config{
		Level: compress.LevelDefault,
		Next:  skipResponseCompression,
	}))
	app.Use(requestLogger(opts))

	// live reports that the process is up. It must never depend on upstream
	// ChatGPT so an OpenAI outage does not get the pod killed.
	// Provenance is additive: "status" keeps its existing value and position so
	// every existing probe and assertion still passes.
	build := buildinfo.Get()
	live := func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":           "ok",
			"build":            build,
			"contract_version": structured.ContractVersion,
		})
	}
	// /health is kept as a live alias so existing k8s probes need no change.
	app.Get("/health", live)
	app.Get("/health/live", live)
	// ready fails while draining so Services stop routing new traffic during
	// rollouts. It deliberately does not ping upstream ChatGPT.
	app.Get("/health/ready", func(c *fiber.Ctx) error {
		if opts.drain.Load() {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"status": "draining",
			})
		}
		return c.JSON(fiber.Map{
			"status": "ok",
		})
	})
	// Drain controls are localhost-only: a new pod signals the old one to stop
	// accepting work before SIGTERM. Non-loopback callers get a 404 so the
	// endpoint is not discoverable and the drain state is left untouched.
	app.Post("/drain/start", drainControl(opts, true))
	app.Post("/drain/stop", drainControl(opts, false))
	if opts.metrics.Enabled() {
		app.Get("/metrics", bearerAuthMiddleware(cfg.GatewayBearerSecret), adaptor.HTTPHandler(opts.metrics.Handler()))
	}
	// The structured route answers auth failures in its own error vocabulary.
	// It is registered ahead of the /v1 gate, which still runs unchanged for
	// every request it admits.
	if cfg.StructuredEnabled {
		app.Use(StructuredPath, structuredAuthMiddleware(opts, cfg.GatewayBearerSecret))
	}
	// Bearer auth guards every /v1 route but never /health, which k8s probes
	// must reach unauthenticated.
	app.Use("/v1", bearerAuthMiddleware(cfg.GatewayBearerSecret))
	app.Get("/v1/models", models(cfg))
	if opts.usageMonitor != nil {
		app.Get("/v1/accounts/usage", accountUsage(opts))
	}
	app.Post("/v1/chat/completions", chatCompletions(opts))
	// Registered only when explicitly enabled. While disabled the path falls
	// through to the 404 handler, so the pre-ticket surface is unchanged.
	if cfg.StructuredEnabled {
		app.Post(StructuredPath, structuredInference(opts))
	}
	app.Use(func(c *fiber.Ctx) error {
		return writeError(c, fiber.StatusNotFound, "invalid_request_error", "not found")
	})

	return app
}

func accountUsage(opts options) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.JSON(opts.usageMonitor.Usage(opts.requestContext(c)))
	}
}

// skipResponseCompression disables Fiber compress for streaming chat
// completions. Compressing SSE would buffer the stream and break Cursor Agent.
func skipResponseCompression(c *fiber.Ctx) bool {
	if c.Method() != fiber.MethodPost || c.Path() != "/v1/chat/completions" {
		return false
	}
	body := c.Body()
	if len(body) == 0 {
		return true
	}
	trimmed := bytes.ReplaceAll(body, []byte(" "), nil)
	return bytes.Contains(trimmed, []byte(`"stream":true`))
}

// drainControl sets or clears the draining flag, but only for loopback callers.
// Remote callers get a 404 and the state is left untouched (AC3).
func drainControl(opts options, draining bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !opts.isLocal(c) {
			return writeError(c, fiber.StatusNotFound, "invalid_request_error", "not found")
		}
		opts.drain.Store(draining)
		status := "ok"
		if draining {
			status = "draining"
		}
		return c.JSON(fiber.Map{
			"status":   status,
			"draining": draining,
		})
	}
}

func models(cfg config.Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		aliases := openai.ListedModelAliases()
		models := make([]openai.Model, 0, len(aliases))
		for _, alias := range aliases {
			if !cfg.ProviderEnabled(codex.ProviderForModel(alias.UpstreamModel)) {
				continue
			}
			models = append(models, openai.Model{
				ID:      alias.ID,
				Object:  "model",
				Created: 0,
				OwnedBy: "open-agent-api",
			})
		}
		return c.JSON(openai.ModelListResponse{
			Object: "list",
			Data:   models,
		})
	}
}

func chatCompletions(opts options) fiber.Handler {
	return func(c *fiber.Ctx) error {
		provider := "unknown"
		serviceTier := ""
		requestStart := opts.now()
		var telemetryRecorded atomic.Bool
		defer func() {
			status := c.Response().StatusCode()
			result := requestMetricResult(status)
			opts.metrics.ObserveRequest(provider, result)
			if !telemetryRecorded.Swap(true) {
				opts.metrics.ObserveChatDuration(provider, result, opts.now().Sub(requestStart))
				opts.metrics.ObserveFastTierRequest(provider, serviceTier, result)
			}
			if status == fiber.StatusTooManyRequests {
				failureClass, _ := c.Locals(metricsFailureClassLocal).(string)
				if failureClass == "" {
					failureClass = string(codex.FailureRateLimit)
				}
				opts.metrics.ObserveRateLimit(provider, failureClass)
			}
		}()
		// Reject new work while draining so in-flight requests can finish before
		// SIGTERM. Placed before any parsing or upstream call so no ChatGPT
		// request is made (AC2).
		if opts.drain.Load() {
			return writeError(c, fiber.StatusServiceUnavailable, "server_error", "server draining")
		}
		requestID := opts.newID()
		if opts.logBodyShape {
			logLine(opts, "body_shape path=%s %s %s\n", c.Path(), redactedBodyShape(c.Body()), summarizeCursorWire(c.Body()).logFields())
		}
		if opts.logRequestIdentity {
			logLine(opts, "%s\n", redactedRequestIdentity(c, requestID))
		}

		var req openai.ChatCompletionRequest
		if err := c.BodyParser(&req); err != nil {
			return writeError(c, fiber.StatusBadRequest, "invalid_request_error", "invalid JSON request body")
		}
		if err := validateChatRequest(req); err != nil {
			return writeError(c, fiber.StatusBadRequest, "invalid_request_error", err.Error())
		}

		model := req.Model
		if model == "" {
			model = openai.DefaultModel
		}
		modelAlias := openai.ResolveModelAlias(model)
		provider = codex.ProviderForModel(modelAlias.UpstreamModel)
		serviceTier = modelAlias.ServiceTier
		if !opts.contextConfig.ProviderEnabled(provider) {
			logLine(opts, "provider_disabled model=%s provider=%s\n", model, provider)
			return writeError(c, fiber.StatusNotFound, "invalid_request_error", "model not found")
		}
		toolsPresent := rawJSONPresent(req.Tools)
		if !toolsPresent {
			// Non-Agent requests intentionally bypass admission control. Record the
			// zero wait so ordinary chat traffic still advances the histogram and
			// operators can distinguish bypasses from queued acquisitions.
			opts.metrics.ObserveQueueWait(provider, "bypassed", 0)
		}
		turnClass := classifyTurn(req, toolsPresent)
		logLine(opts, "chat_completion model=%s provider=%s stream=%t tools_present=%t turn_class=%s\n", model, provider, req.Stream, toolsPresent, turnClass)

		ctx, cancel := requestContext(c, opts.requestContext(c))
		queueKey := resolveAgentQueueKey(opts.agentQueueKeyMode, opts.contextConfig.GatewayTenantHeader, c, c.Body())
		// Cursor and other OpenAI clients send their own tools. Faithful Codex mode
		// injects the captured CLI profile/tools and often makes those requests fail upstream.
		faithful := defaultBool(req.Faithful, !toolsPresent)
		prewarm := defaultBool(req.Prewarm, faithful)
		messages := req.Messages
		contextDuration := time.Duration(0)
		if toolsPresent && !faithful {
			contextStart := opts.now()
			contextCfg := opts.contextConfig
			if modelAlias.ContextHardMaxBytes > 0 {
				contextCfg = hardContextConfig(contextCfg, modelAlias.ContextHardMaxBytes)
			}
			managed := manageContext(req.Messages, contextCfg)
			if managed.Changed {
				logLine(
					opts,
					"context_manage request_id=%s before_messages=%d before_bytes=%d before_tool_outputs=%d before_oversized_tool_outputs=%d after_messages=%d after_bytes=%d after_tool_outputs=%d after_oversized_tool_outputs=%d truncated_tools=%d compacted_tools=%d recent_messages_kept=%d\n",
					requestID,
					managed.Before.Messages,
					managed.Before.Bytes,
					managed.Before.ToolOutputs,
					managed.Before.OversizedToolOutputs,
					managed.After.Messages,
					managed.After.Bytes,
					managed.After.ToolOutputs,
					managed.After.OversizedToolOutputs,
					managed.TruncatedTools,
					managed.CompactedTools,
					opts.contextConfig.ContextRecentMessages,
				)
			}
			messages = managed.Messages
			if modelAlias.ContextHardMaxBytes > 0 {
				trimmed, droppedMessages := dropOldestToFit(messages, modelAlias.ContextHardMaxBytes, hardContextProtectRecent)
				if droppedMessages > 0 {
					messages = trimmed
					logLine(opts, "context_hard_drop request_id=%s model=%s dropped_messages=%d kept_messages=%d\n", requestID, model, droppedMessages, len(messages))
				}
			}
			contextDuration = opts.now().Sub(contextStart)
		}
		serviceReq := applyAgentTurnToolChoice(codex.Request{
			Model:             modelAlias.UpstreamModel,
			Messages:          messages,
			Tools:             req.Tools,
			ToolChoice:        req.ToolChoice,
			ParallelToolCalls: req.ParallelToolCalls,
			ReasoningEffort:   defaultString(req.ReasoningEffort, modelAlias.ReasoningEffort),
			Verbosity:         defaultString(req.Verbosity, modelAlias.Verbosity),
			ServiceTier:       modelAlias.ServiceTier,
			Faithful:          faithful,
			Prewarm:           prewarm,
			RequestID:         requestID,
			AffinityKey:       queueKey.Value,
			AffinityKeyHash:   queueKey.Hash,
			AffinityKeyMode:   queueKey.Mode,
		})

		releaseQueue := func() {}
		queueWait := time.Duration(0)
		if toolsPresent {
			release, wait, err := opts.agentQueueFor(provider).acquireWithPriority(ctx, requestID, queueKey, turnClass, agentQueuePriorityInteractive)
			queueWait = wait
			if err != nil {
				cancel()
				logRequestTiming(opts, requestID, contextDuration, queueWait, -1, -1, opts.now().Sub(requestStart))
				return mapAgentQueueError(c, err)
			}
			releaseQueue = release
		}

		if req.Stream {
			telemetryRecorded.Store(true)
			return streamChatCompletion(c, opts, ctx, cancel, serviceReq, provider, requestID, releaseQueue, requestStart, contextDuration, queueWait, &telemetryRecorded)
		}
		defer cancel()
		defer releaseQueue()

		upstreamStart := opts.now()
		completion, err := completeWithDegenerateRetry(ctx, opts, opts.codexService, serviceReq, toolsPresent, requestID)
		if err != nil && errors.Is(err, codex.ErrUsageLimitReached) {
			if fallbackReq, ok := buildQuotaFallbackRequest(serviceReq, opts.contextConfig); ok {
				logLine(opts, "quota_fallback request_id=%s from=%s to=%s messages=%d\n", requestID, serviceReq.Model, fallbackReq.Model, len(fallbackReq.Messages))
				completion, err = completeWithDegenerateRetry(ctx, opts, opts.codexService, fallbackReq, toolsPresent, requestID)
			}
		}
		upstreamDuration := opts.now().Sub(upstreamStart)
		if err != nil {
			logLine(opts, "complete_error model=%s err=%s failure_class=%s failure_phase=%s\n", model, detailedError(err), codex.ClassifyFailure(err), codex.PhaseConnect)
			logRequestTiming(opts, requestID, contextDuration, queueWait, upstreamDuration, -1, opts.now().Sub(requestStart))
			return mapServiceError(c, err)
		}
		opts.metrics.ObserveChatUsage(provider, completion.Usage.PromptTokens, completion.Usage.CompletionTokens, completion.Usage.TotalTokens)

		message := openai.ChatMessage{
			Role:    "assistant",
			Content: openai.TextContent(completion.Text),
		}
		finishReason := "stop"
		if len(completion.ToolCalls) > 0 {
			message.Content = json.RawMessage("null")
			message.ToolCalls = openAIToolCalls(completion.ToolCalls, opts.contextConfig.CustomToolWire)
			finishReason = "tool_calls"
		}

		logRequestTiming(opts, requestID, contextDuration, queueWait, upstreamDuration, -1, opts.now().Sub(requestStart))
		return c.JSON(openai.ChatCompletionResponse{
			ID:      completionID(completion.ID, func() string { return requestID }),
			Object:  "chat.completion",
			Created: opts.now().Unix(),
			Model:   defaultString(completion.Model, model),
			Choices: []openai.ChatCompletionChoice{
				{
					Index:        0,
					Message:      message,
					FinishReason: finishReason,
				},
			},
			Usage: completion.Usage,
		})
	}
}

func streamChatCompletion(c *fiber.Ctx, opts options, ctx context.Context, cancel context.CancelFunc, req codex.Request, provider string, requestID string, releaseQueue func(), requestStart time.Time, contextDuration time.Duration, queueWait time.Duration, telemetryRecorded *atomic.Bool) error {
	upstreamStart := opts.now()
	events, err := opts.codexService.Stream(ctx, req)
	if err != nil && errors.Is(err, codex.ErrUsageLimitReached) {
		if fallbackReq, ok := buildQuotaFallbackRequest(req, opts.contextConfig); ok {
			fallbackEvents, fallbackErr := opts.codexService.Stream(ctx, fallbackReq)
			if fallbackErr != nil {
				logLine(opts, "quota_fallback_error request_id=%s from=%s to=%s err=%s\n", requestID, req.Model, fallbackReq.Model, detailedError(fallbackErr))
				err = fallbackErr
			} else {
				logLine(opts, "quota_fallback request_id=%s from=%s to=%s messages=%d\n", requestID, req.Model, fallbackReq.Model, len(fallbackReq.Messages))
				req = fallbackReq
				events = fallbackEvents
				err = nil
			}
		}
	}
	if err != nil {
		cancel()
		releaseQueue()
		logLine(opts, "stream_error id=%s model=%s err=%s failure_class=%s failure_phase=%s\n", requestID, req.Model, detailedError(err), codex.ClassifyFailure(err), codex.PhaseConnect)
		logRequestTiming(opts, requestID, contextDuration, queueWait, opts.now().Sub(upstreamStart), -1, opts.now().Sub(requestStart))
		result := serviceErrorMetricResult(err)
		opts.metrics.ObserveChatDuration(provider, result, opts.now().Sub(requestStart))
		opts.metrics.ObserveFastTierRequest(provider, req.ServiceTier, result)
		return mapServiceError(c, err)
	}
	events = withStreamIdleTimeout(ctx, events, opts.contextConfig.StreamIdleTimeout)

	id := requestID
	created := opts.now().Unix()
	model := req.Model
	streamID := id

	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		opts.metrics.IncActiveStreams(provider)
		defer opts.metrics.DecActiveStreams(provider)
		defer releaseQueue()
		defer cancel()

		logLine(opts, "stream_start id=%s model=%s tools_present=%t\n", streamID, model, rawJSONPresent(req.Tools))

		if !writeSSE(ctx, cancel, w, openai.ChatCompletionChunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []openai.ChatCompletionChunkChoice{
				{Index: 0, Delta: openai.ChatDelta{Role: "assistant"}},
			},
		}) {
			logLine(opts,
				"stream_end id=%s model=%s outcome=client_disconnect deltas=0 tool_deltas=0 upstream_events=0 finish=none ctx_err=%s duration_ms=0\n",
				streamID, model, ctxErrString(ctx),
			)
			logRequestTiming(opts, streamID, contextDuration, queueWait, opts.now().Sub(upstreamStart), -1, opts.now().Sub(requestStart))
			opts.metrics.ObserveChatDuration(provider, "canceled", opts.now().Sub(requestStart))
			opts.metrics.ObserveFastTierRequest(provider, req.ServiceTier, "canceled")
			return
		}

		outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, toolCallCount, assistantText, start, firstDeltaLatency := deliverToolStream(
			ctx, opts, w, cancel, req, opts.codexService, events, id, created, model, streamID, provider, upstreamStart,
		)

		finish := streamFinish(outcome, toolCallCount > 0)
		if opts.logBodyShape {
			logStreamOutput(opts, streamID, textBytes, toolCallCount, toolArgChars, detectLoopPhrase(assistantText))
		}
		logLine(opts,
			"stream_end id=%s model=%s outcome=%s deltas=%d tool_deltas=%d upstream_events=%d finish=%s ctx_err=%s duration_ms=%d\n",
			streamID, model, outcome, deltas, toolDeltas, upstreamEvents, finish,
			ctxErrString(ctx), opts.now().Sub(start).Milliseconds(),
		)
		logRequestTiming(opts, streamID, contextDuration, queueWait, opts.now().Sub(upstreamStart), firstDeltaLatency, opts.now().Sub(requestStart))
		result := streamMetricResult(outcome)
		opts.metrics.ObserveChatDuration(provider, result, opts.now().Sub(requestStart))
		opts.metrics.ObserveFastTierRequest(provider, req.ServiceTier, result)
		telemetryRecorded.Store(true)
	})
	return nil
}

func streamMetricResult(outcome string) string {
	switch outcome {
	case "completed":
		return "success"
	case "client_disconnect":
		return "canceled"
	default:
		return "server_error"
	}
}

func serviceErrorMetricResult(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, codex.ErrUsageLimitReached):
		return "rate_limited"
	default:
		return "server_error"
	}
}

func logRequestTiming(opts options, requestID string, contextDuration, queueWait, upstreamDuration, firstDeltaLatency, totalDuration time.Duration) {
	logLine(
		opts,
		"request_timing request_id=%s context_ms=%d queue_wait_ms=%d upstream_stream_ms=%d first_delta_ms=%d total_ms=%d\n",
		requestID,
		durationMillis(contextDuration),
		durationMillis(queueWait),
		durationMillis(upstreamDuration),
		durationMillis(firstDeltaLatency),
		durationMillis(totalDuration),
	)
}

func durationMillis(duration time.Duration) int64 {
	if duration < 0 {
		return -1
	}
	return duration.Milliseconds()
}

func requestMetricResult(status int) string {
	switch {
	case status == fiber.StatusTooManyRequests:
		return "rate_limited"
	case status >= fiber.StatusOK && status < fiber.StatusBadRequest:
		return "success"
	case status >= fiber.StatusBadRequest && status < fiber.StatusInternalServerError:
		return "client_error"
	default:
		return "server_error"
	}
}

func streamFinish(outcome string, toolCallEmitted bool) string {
	if outcome != "completed" {
		return "none"
	}
	if toolCallEmitted {
		return "tool_calls"
	}
	return "stop"
}

func ctxErrString(ctx context.Context) string {
	if err := ctx.Err(); err != nil {
		return err.Error()
	}
	return "none"
}

func openAIToolCalls(toolCalls []codex.ToolCall, customWire string) []openai.ToolCall {
	out := make([]openai.ToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		mapped := openai.ToolCall{
			ID:           toolCall.ID,
			Type:         defaultString(toolCall.Type, "function"),
			ExtraContent: openai.GoogleThoughtSignatureExtra(toolCall.ThoughtSignature),
		}
		if mapped.Type == "custom" && customWire == "function" {
			mapped.Type = "function"
			mapped.Function = openai.ToolCallFunction{
				Name:      toolCall.Function.Name,
				Arguments: toolCall.Function.Arguments,
			}
		} else if mapped.Type == "custom" {
			mapped.Custom = &openai.ToolCallCustom{
				Name:  toolCall.Function.Name,
				Input: toolCall.Function.Arguments,
			}
		} else {
			mapped.Function = openai.ToolCallFunction{
				Name:      toolCall.Function.Name,
				Arguments: toolCall.Function.Arguments,
			}
		}
		out = append(out, mapped)
	}
	return out
}

func requestLogger(opts options) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		status := c.Response().StatusCode()
		if status == 0 {
			status = fiber.StatusOK
		}
		notFound := ""
		if status == fiber.StatusNotFound {
			notFound = " not_found=true"
		}
		logLine(
			opts,
			"%s status=%d method=%s path=%s latency=%s authorization_present=%t%s\n",
			time.Now().Format(time.RFC3339),
			status,
			c.Method(),
			c.Path(),
			time.Since(start),
			c.Get(fiber.HeaderAuthorization) != "",
			notFound,
		)
		return err
	}
}

func logLine(opts options, format string, args ...any) {
	if opts.logOutput == nil {
		return
	}
	if opts.logMu != nil {
		opts.logMu.Lock()
		defer opts.logMu.Unlock()
	}
	_, _ = fmt.Fprintf(opts.logOutput, format, args...)
}

func detailedError(err error) string {
	if err == nil {
		return ""
	}
	parts := []string{err.Error()}
	for unwrapped := errors.Unwrap(err); unwrapped != nil; unwrapped = errors.Unwrap(unwrapped) {
		msg := unwrapped.Error()
		if msg != "" && msg != parts[len(parts)-1] {
			parts = append(parts, msg)
		}
	}
	return strings.Join(parts, ": ")
}

func redactedBodyShape(raw []byte) string {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return "valid_json=false"
	}

	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	roles := []string{}
	if rawMessages, ok := body["messages"]; ok {
		var messages []struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(rawMessages, &messages); err == nil {
			for _, message := range messages {
				roles = append(roles, message.Role)
			}
		}
	}

	toolsPresent := false
	toolsCount := 0
	if rawTools, ok := body["tools"]; ok {
		toolsPresent = rawJSONPresent(rawTools)
		var tools []json.RawMessage
		if err := json.Unmarshal(rawTools, &tools); err == nil {
			toolsCount = len(tools)
		}
	}

	return fmt.Sprintf(
		"valid_json=true fields=%s messages=%d message_roles=%s tools_present=%t tools_count=%d",
		strings.Join(keys, ","),
		len(roles),
		strings.Join(roles, ","),
		toolsPresent,
		toolsCount,
	)
}

func rawJSONPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func validateChatRequest(req openai.ChatCompletionRequest) error {
	if len(req.Messages) == 0 {
		return errors.New("messages is required")
	}
	for i, msg := range req.Messages {
		if msg.Role == "" {
			return fmt.Errorf("messages[%d].role is required", i)
		}
	}
	return nil
}

func mapServiceError(c *fiber.Ctx, err error) error {
	c.Locals(metricsFailureClassLocal, string(codex.ClassifyFailure(err)))
	if errors.Is(err, context.Canceled) {
		return writeError(c, 499, "request_canceled", "request canceled")
	}
	if errors.Is(err, codex.ErrContextWindowExceeded) {
		return writeError(c, fiber.StatusBadRequest, "invalid_request_error", "conversation exceeds this model's context window - switch this chat to a larger model")
	}
	if errors.Is(err, codex.ErrUsageLimitReached) {
		return writeError(c, fiber.StatusTooManyRequests, "rate_limit_error", publicErrorMessage(err))
	}
	if serviceErr, ok := codex.ErrorAs(err); ok {
		status := serviceErr.Status
		errorType := "api_error"
		switch serviceErr.Kind {
		case codex.ErrorKindAuth:
			status = defaultStatus(status, fiber.StatusUnauthorized)
			errorType = "authentication_error"
		case codex.ErrorKindClient:
			status = defaultStatus(status, fiber.StatusBadRequest)
			errorType = "invalid_request_error"
		default:
			status = defaultStatus(status, fiber.StatusBadGateway)
		}
		if status == fiber.StatusTooManyRequests {
			errorType = "rate_limit_error"
			return writeError(c, status, errorType, publicErrorMessage(err))
		}
		return writeError(c, status, errorType, publicServiceMessage(serviceErr.Kind))
	}
	return writeError(c, fiber.StatusInternalServerError, "api_error", "internal server error")
}

func mapAgentQueueError(c *fiber.Ctx, err error) error {
	c.Locals(metricsFailureClassLocal, string(codex.FailureRateLimit))
	switch {
	case errors.Is(err, errAgentQueueFull):
		return writeError(c, fiber.StatusTooManyRequests, "rate_limit_error", "agent queue full")
	case errors.Is(err, errAgentQueueTimeout):
		return writeError(c, fiber.StatusTooManyRequests, "rate_limit_error", "agent queue timeout")
	default:
		return mapServiceError(c, err)
	}
}

const metricsFailureClassLocal = "codex_chat_api.metrics_failure_class"

func writeError(c *fiber.Ctx, status int, errorType string, message string) error {
	return c.Status(status).JSON(openai.ErrorResponse{
		Error: openai.ErrorBody{
			Message: message,
			Type:    errorType,
		},
	})
}

func requestContext(c *fiber.Ctx, parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	done := c.Context().Done()
	if done != nil {
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}

func writeSSE(ctx context.Context, cancel context.CancelFunc, w *bufio.Writer, v any) bool {
	select {
	case <-ctx.Done():
		return false
	default:
	}

	data, err := sse.Data(v)
	if err != nil {
		cancel()
		return false
	}
	if _, err := w.Write(data); err != nil {
		cancel()
		return false
	}
	if err := w.Flush(); err != nil {
		cancel()
		return false
	}
	return true
}

func errorChunk(id string, created int64, model string, message string) openai.ChatCompletionChunk {
	finish := "stop"
	return openai.ChatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []openai.ChatCompletionChunkChoice{
			{
				Index:        0,
				Delta:        openai.ChatDelta{Content: "[error: " + message + "]"},
				FinishReason: &finish,
			},
		},
	}
}

func publicErrorMessage(err error) string {
	if errors.Is(err, codex.ErrClientPoolSaturated) {
		return codex.ErrClientPoolSaturated.Error()
	}
	if errors.Is(err, codex.ErrContextWindowExceeded) {
		return "conversation exceeds this model's context window - switch this chat to a larger model"
	}
	if errors.Is(err, codex.ErrUsageLimitReached) {
		if serviceErr, ok := codex.ErrorAs(err); ok {
			if message := publicUsageLimitMessage(serviceErr.Message); message != "" {
				return message
			}
		}
		return "usage limit reached for this model - try again later or switch model"
	}
	if serviceErr, ok := codex.ErrorAs(err); ok {
		if serviceErr.Status == fiber.StatusTooManyRequests {
			if message := publicUsageLimitMessage(serviceErr.Message); message != "" {
				return message
			}
			return "usage limit reached for this model - try again later or switch model"
		}
		return publicServiceMessage(serviceErr.Kind)
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	return "upstream error"
}

func publicUsageLimitMessage(upstream string) string {
	lower := strings.ToLower(strings.TrimSpace(upstream))
	switch {
	case strings.Contains(lower, "exhausted your capacity"),
		strings.Contains(lower, "capacity on this model"):
		return "usage limit reached for this model - try gemini-2.5-flash or try again later"
	case strings.Contains(lower, "usage limit"),
		strings.Contains(lower, "rate limit"),
		strings.Contains(lower, "quota"),
		strings.Contains(lower, "resource_exhausted"),
		strings.Contains(lower, "resource exhausted"),
		strings.Contains(lower, "too many requests"):
		return "usage limit reached for this model - try again later or switch model"
	default:
		return ""
	}
}

func publicServiceMessage(kind codex.ErrorKind) string {
	switch kind {
	case codex.ErrorKindAuth:
		return "authentication failed"
	case codex.ErrorKindClient:
		return "invalid request"
	default:
		return "upstream error"
	}
}

// agentQueueLockDirFor scopes the distributed lock directory per provider so
// separate queues never contend on the same lock files. Codex keeps the
// legacy unscoped path: binaries from before the per-provider split lock
// there, and mixed-version processes must contend on the same files.
func agentQueueLockDirFor(base string, provider string) string {
	if base == "" {
		return ""
	}
	if provider == codex.ProviderCodex {
		return base
	}
	return filepath.Join(base, provider)
}

func completionID(id string, newID func() string) string {
	if id != "" {
		return id
	}
	return newID()
}

func defaultString(value string, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func defaultBool(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func defaultStatus(value int, fallback int) int {
	if value >= 400 && value <= 599 {
		return value
	}
	return fallback
}
