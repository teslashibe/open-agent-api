package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/teslashibe/open-agent-api/internal/buildinfo"
	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
	"github.com/teslashibe/open-agent-api/internal/structured"
)

// StructuredPath is the structured inference route. It is registered only when
// cfg.StructuredEnabled is set, so a gateway that has not opted in serves the
// exact pre-ticket surface.
const StructuredPath = "/v1/structured/inference"

// structuredExtractionInstruction is belt-and-braces alongside the upstream
// strict json_schema format: some Codex models honour text.format loosely, and
// a prompt-level contract makes non-conforming output rarer. Output that still
// does not conform is an output_validation_failed, never a 5xx.
const structuredExtractionInstruction = "You are a strict data extraction engine. Read the user's input and reply with a single JSON object that satisfies the provided JSON Schema. Reply with JSON only: no prose, no explanation, and no markdown code fences."

// defaultStructuredRetryAfter is the Retry-After hint used when admission
// control sheds load and upstream gave us no better number.
const defaultStructuredRetryAfter = 5 * time.Second

// structuredAuthMiddleware applies the same gateway secret as the /v1 gate but
// reports failure in the structured vocabulary, so a client can branch on
// code == "auth_error" instead of string-matching the OpenAI error shape. It is
// registered ahead of bearerAuthMiddleware and only for the structured path;
// requests it admits are still re-checked by the unchanged /v1 gate.
func structuredAuthMiddleware(opts options, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if secret == "" {
			return c.Next()
		}
		token, ok := bearerToken(c.Get(fiber.HeaderAuthorization))
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
			return writeStructuredError(c, opts, "", structured.NewError(structured.CodeAuth, "authentication failed"), fiber.StatusUnauthorized, 0)
		}
		return c.Next()
	}
}

// structuredInference implements the Report Studio structured contract. It
// deliberately never falls through to mapServiceError: the two error
// vocabularies stay separate so a chat-surface change cannot silently alter a
// structured error code.
func structuredInference(opts options) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := opts.now()

		// Draining sheds new structured work before any parsing or upstream
		// call, and advertises when to come back (AC5).
		if opts.drain.Load() {
			return writeStructuredError(c, opts, "", structured.NewError(structured.CodeUnavailable, "server draining"), fiber.StatusServiceUnavailable, defaultStructuredRetryAfter)
		}

		var req structured.Request
		if err := c.BodyParser(&req); err != nil {
			return writeStructuredError(c, opts, "", structured.NewError(structured.CodeInvalidRequest, "invalid JSON request body"), fiber.StatusBadRequest, 0)
		}
		req.Normalize()
		if err := req.Validate(); err != nil {
			return writeStructuredError(c, opts, req.RequestID, err, fiber.StatusBadRequest, 0)
		}

		schema, schemaErr := structured.CompileSchema(req.Schema)
		if schemaErr != nil {
			return writeStructuredError(c, opts, req.RequestID, schemaErr, fiber.StatusBadRequest, 0)
		}

		model, modelErr := opts.structuredPolicy.Resolve(req.Model)
		if modelErr != nil {
			return writeStructuredError(c, opts, req.RequestID, modelErr, fiber.StatusNotFound, 0)
		}

		caller := structuredCaller(opts, c)
		inputChecksum := structured.InputChecksum(req.Input)
		policyVersion := opts.structuredPolicy.Version()
		key := structured.KeyParts{
			Caller:             caller.Value,
			Operation:          req.Operation,
			InputChecksum:      inputChecksum,
			SchemaVersion:      req.SchemaVersion,
			ModelPolicyVersion: policyVersion,
			IdempotencyKey:     req.IdempotencyKey,
		}.Key()
		// The key scopes storage; the fingerprint binds that slot to the exact
		// inference asked for. Effort, verbosity, and the output limit are the
		// effective values actually sent upstream, so a request that clamps to
		// the same call is a replay rather than a conflict.
		fingerprint := structured.Fingerprint{
			Caller:             caller.Value,
			Operation:          req.Operation,
			Model:              req.Model,
			ResolvedModel:      model.ID,
			ReasoningEffort:    defaultString(req.ReasoningEffort, model.ReasoningEffort),
			Verbosity:          defaultString(req.Verbosity, model.Verbosity),
			SchemaVersion:      req.SchemaVersion,
			ModelPolicyVersion: policyVersion,
			InputChecksum:      inputChecksum,
			SchemaChecksum:     structured.SchemaChecksum(req.Schema),
			MaxOutputTokens:    structuredOutputTokens(req.MaxOutputTokens, opts.contextConfig.StructuredMaxOutputTokens),
		}.String()

		ctx, cancel := requestContext(c, opts.requestContext(c))
		defer cancel()
		deadline := structuredDeadline(req.DeadlineMS, opts.contextConfig.StructuredMaxDeadline)
		ctx, cancelDeadline := context.WithTimeout(ctx, deadline)
		defer cancelDeadline()

		release, queueWait, queueErr := opts.structuredQueue.acquire(ctx, req.RequestID, caller, turnClassSimpleNoTool)
		if queueErr != nil {
			contractErr, status, retryAfter := structuredAdmissionFailure(queueErr)
			logLine(opts, "structured_shed request_id=%s operation=%s model=%s code=%s\n", req.RequestID, req.Operation, model.ID, contractErr.Code)
			return writeStructuredError(c, opts, req.RequestID, contractErr, status, retryAfter)
		}
		defer release()
		opts.metrics.IncStructuredInflight()
		defer opts.metrics.DecStructuredInflight()

		logLine(opts, "structured_admit request_id=%s operation=%s model=%s caller=%s queue_wait_ms=%d deadline_ms=%d\n",
			req.RequestID, req.Operation, model.ID, caller.Hash, queueWait.Milliseconds(), deadline.Milliseconds())

		response, replay, err := opts.structuredIdempotency.Do(ctx, key, fingerprint, func() (structured.Response, error) {
			return runStructuredInference(ctx, opts, req, schema, model, start)
		})
		if err != nil {
			contractErr, status, retryAfter := structuredFailure(err)
			if contractErr.Code == structured.CodeIdempotencyConflict {
				// A conflict is a caller mistake, not a gateway or upstream
				// fault, and it made no upstream call. Its own greppable prefix
				// keeps it out of the error-rate signal.
				logLine(opts, "structured_conflict request_id=%s operation=%s model=%s\n", req.RequestID, req.Operation, model.ID)
			} else {
				logLine(opts, "structured_error request_id=%s operation=%s model=%s code=%s\n", req.RequestID, req.Operation, model.ID, contractErr.Code)
			}
			opts.metrics.ObserveStructuredLatency(model.ID, string(contractErr.Code), opts.now().Sub(start))
			return writeStructuredError(c, opts, req.RequestID, contractErr, status, retryAfter)
		}

		response.IdempotentReplay = replay
		response.RequestID = req.RequestID
		response.IdempotencyKey = req.IdempotencyKey
		if replay {
			// A replay reports the latency of serving the replay, not of the
			// original upstream call, so callers can distinguish the two.
			response.LatencyMS = durationMillis(opts.now().Sub(start))
		}
		opts.metrics.ObserveStructuredLatency(model.ID, "success", opts.now().Sub(start))
		logLine(opts, "structured_success request_id=%s operation=%s model=%s replay=%t latency_ms=%d\n",
			req.RequestID, req.Operation, model.ID, replay, response.LatencyMS)
		return c.JSON(response)
	}
}

// runStructuredInference performs one upstream call and validates its output.
// It runs inside the idempotency single-flight, so a concurrent duplicate waits
// here instead of issuing a second upstream request.
func runStructuredInference(
	ctx context.Context,
	opts options,
	req structured.Request,
	schema *structured.Schema,
	model structured.ResolvedModel,
	start time.Time,
) (structured.Response, error) {
	format, formatErr := structuredResponseFormat(req)
	if formatErr != nil {
		return structured.Response{}, formatErr
	}

	serviceReq := codex.Request{
		Model: model.Upstream,
		Messages: []openai.ChatMessage{
			{Role: "system", Content: openai.TextContent(structuredExtractionInstruction)},
			{Role: "user", Content: openai.TextContent(req.Input)},
		},
		ReasoningEffort: defaultString(req.ReasoningEffort, model.ReasoningEffort),
		Verbosity:       defaultString(req.Verbosity, model.Verbosity),
		RequestID:       req.RequestID,
		// Extraction turns off tools, the faithful profile/scaffold, and prewarm.
		Extraction:      true,
		ResponseFormat:  format,
		MaxOutputTokens: structuredOutputTokens(req.MaxOutputTokens, opts.contextConfig.StructuredMaxOutputTokens),
	}

	completion, err := opts.codexService.Complete(ctx, serviceReq)
	if err != nil {
		return structured.Response{}, err
	}

	data, extractErr := structured.ExtractJSON(completion.Text)
	if extractErr != nil {
		opts.metrics.ObserveStructuredValidation("unparsable")
		return structured.Response{}, extractErr
	}
	if validationErr := structured.Validate(schema, data); validationErr != nil {
		opts.metrics.ObserveStructuredValidation("invalid")
		return structured.Response{}, validationErr
	}
	opts.metrics.ObserveStructuredValidation("valid")
	opts.metrics.ObserveStructuredUsage(model.ID, completion.Usage.PromptTokens, completion.Usage.CompletionTokens, completion.Usage.TotalTokens)

	return structured.Response{
		Data:               data,
		Model:              defaultString(completion.Model, model.ID),
		UpstreamResponseID: completion.ID,
		Usage:              completion.Usage,
		LatencyMS:          durationMillis(opts.now().Sub(start)),
		RequestID:          req.RequestID,
		IdempotencyKey:     req.IdempotencyKey,
		Operation:          req.Operation,
		SchemaVersion:      req.SchemaVersion,
		ModelPolicyVersion: opts.structuredPolicy.Version(),
		ContractVersion:    structured.ContractVersion,
		Build:              buildinfo.Get(),
	}, nil
}

// structuredResponseFormat builds the upstream strict json_schema text.format
// object from the already-compiled request schema.
func structuredResponseFormat(req structured.Request) (json.RawMessage, *structured.Error) {
	name := structuredSchemaName(req)
	format, err := json.Marshal(map[string]any{
		"type":   "json_schema",
		"name":   name,
		"strict": true,
		"schema": json.RawMessage(req.Schema),
	})
	if err != nil {
		return nil, structured.NewError(structured.CodeInvalidSchema, "schema could not be encoded for upstream")
	}
	return format, nil
}

// structuredSchemaName derives a stable, upstream-safe json_schema name from
// the caller's schema_name (or operation). Upstream only accepts
// [A-Za-z0-9_-], so everything else collapses to an underscore.
func structuredSchemaName(req structured.Request) string {
	name := req.SchemaName
	if name == "" {
		name = req.Operation
	}
	safe := make([]rune, 0, len(name))
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			safe = append(safe, r)
		default:
			safe = append(safe, '_')
		}
		if len(safe) == 64 {
			break
		}
	}
	if len(safe) == 0 {
		return "structured_output"
	}
	return string(safe)
}

// structuredDeadline clamps the caller-supplied deadline to the configured
// maximum. A missing deadline gets the maximum.
func structuredDeadline(deadlineMS int, max time.Duration) time.Duration {
	if max <= 0 {
		max = 5 * time.Minute
	}
	if deadlineMS <= 0 {
		return max
	}
	requested := time.Duration(deadlineMS) * time.Millisecond
	if requested > max {
		return max
	}
	return requested
}

// structuredOutputTokens clamps the caller-supplied output cap.
func structuredOutputTokens(requested int, max int) int {
	if max <= 0 {
		max = math.MaxInt32
	}
	if requested <= 0 || requested > max {
		return max
	}
	return requested
}

// structuredCaller resolves the tenant identity used for both per-caller
// admission and idempotency scoping. The tenant header wins; otherwise the
// Authorization header is hashed, never stored or logged raw.
func structuredCaller(opts options, c *fiber.Ctx) agentQueueKey {
	if name := strings.TrimSpace(opts.contextConfig.GatewayTenantHeader); name != "" {
		if tenant := strings.TrimSpace(c.Get(name)); tenant != "" {
			return newAgentQueueKey("structured:tenant", tenant)
		}
	}
	if authorization := strings.TrimSpace(c.Get(fiber.HeaderAuthorization)); authorization != "" {
		return newAgentQueueKey("structured:auth_hash", safeHash(authorization))
	}
	return newAgentQueueKey("structured:global", defaultAgentQueueKeyValue)
}

// structuredAdmissionFailure maps an admission-control failure onto the
// contract. Only the back-pressure branches advertise Retry-After; a timeout or
// a client cancellation is not something the caller should retry on a schedule.
func structuredAdmissionFailure(err error) (*structured.Error, int, time.Duration) {
	switch {
	case errors.Is(err, errAgentQueueFull):
		return structured.NewError(structured.CodeRateLimited, "structured inference queue is full"), fiber.StatusTooManyRequests, defaultStructuredRetryAfter
	case errors.Is(err, errAgentQueueTimeout):
		return structured.NewError(structured.CodeRateLimited, "structured inference queue wait timed out"), fiber.StatusTooManyRequests, defaultStructuredRetryAfter
	case errors.Is(err, context.DeadlineExceeded):
		return structured.NewError(structured.CodeTimeout, "deadline elapsed while waiting for admission"), fiber.StatusGatewayTimeout, 0
	case errors.Is(err, context.Canceled):
		return structured.NewError(structured.CodeInvalidRequest, "request canceled"), 499, 0
	default:
		return structured.NewError(structured.CodeUnavailable, "admission control unavailable"), fiber.StatusServiceUnavailable, defaultStructuredRetryAfter
	}
}

// structuredFailure maps an inference failure onto the structured contract.
// Every branch produces one of the documented machine-readable codes.
func structuredFailure(err error) (*structured.Error, int, time.Duration) {
	if contractErr, ok := structured.ErrorAs(err); ok {
		switch contractErr.Code {
		case structured.CodeOutputValidation:
			return contractErr, fiber.StatusUnprocessableEntity, 0
		case structured.CodeInvalidSchema, structured.CodeInvalidRequest:
			return contractErr, fiber.StatusBadRequest, 0
		case structured.CodeUnsupportedModel:
			return contractErr, fiber.StatusNotFound, 0
		case structured.CodeIdempotencyConflict:
			// 409 so a client that only branches on status still distinguishes
			// "your key means something else" from a retryable failure.
			return contractErr, fiber.StatusConflict, 0
		default:
			return contractErr, fiber.StatusBadGateway, 0
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return structured.NewError(structured.CodeTimeout, "deadline elapsed before the model responded"), fiber.StatusGatewayTimeout, 0
	}
	if errors.Is(err, context.Canceled) {
		return structured.NewError(structured.CodeInvalidRequest, "request canceled"), 499, 0
	}
	if errors.Is(err, codex.ErrUsageLimitReached) {
		return structured.NewError(structured.CodeRateLimited, "upstream usage limit reached"), fiber.StatusTooManyRequests, upstreamRetryAfter(err)
	}
	if serviceErr, ok := codex.ErrorAs(err); ok {
		switch {
		case serviceErr.Kind == codex.ErrorKindAuth:
			return structured.NewError(structured.CodeAuth, "upstream authentication failed"), fiber.StatusUnauthorized, 0
		case serviceErr.Status == fiber.StatusTooManyRequests:
			return structured.NewError(structured.CodeRateLimited, "upstream rate limited this request"), fiber.StatusTooManyRequests, upstreamRetryAfter(err)
		default:
			return structured.NewError(structured.CodeUpstream, "upstream error"), fiber.StatusBadGateway, 0
		}
	}
	return structured.NewError(structured.CodeUpstream, "upstream error"), fiber.StatusBadGateway, 0
}

// upstreamRetryAfter reuses the upstream hint when there is one so callers back
// off for as long as the provider actually asked for.
func upstreamRetryAfter(err error) time.Duration {
	if serviceErr, ok := codex.ErrorAs(err); ok && serviceErr.RetryAfter > 0 {
		return serviceErr.RetryAfter
	}
	return defaultStructuredRetryAfter
}

func writeStructuredError(c *fiber.Ctx, opts options, requestID string, contractErr *structured.Error, status int, retryAfter time.Duration) error {
	opts.metrics.ObserveStructuredFailure(string(contractErr.Code))
	body := structured.ErrorResponse{
		Error: structured.ErrorBody{
			Code:    contractErr.Code,
			Message: contractErr.Message,
			Details: contractErr.Details,
		},
		RequestID:       requestID,
		ContractVersion: structured.ContractVersion,
		Build:           buildinfo.Get(),
	}
	if retryAfter > 0 {
		seconds := int(math.Ceil(retryAfter.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		body.Error.RetryAfterSeconds = seconds
		c.Set(fiber.HeaderRetryAfter, strconv.Itoa(seconds))
	}
	return c.Status(status).JSON(body)
}
