package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/google/uuid"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/config"
	"github.com/teslashibe/codex-chat-api/internal/openai"
	"github.com/teslashibe/codex-chat-api/internal/sse"
)

type Option func(*options)

type options struct {
	codexService   codex.Service
	requestContext func(*fiber.Ctx) context.Context
	now            func() time.Time
	newID          func() string
	logOutput      io.Writer
	logBodyShape   bool
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
		logOutput:    os.Stdout,
		logBodyShape: cfg.LogBodyShape,
	}
	for _, setter := range setters {
		setter(&opts)
	}

	app := fiber.New(fiber.Config{
		AppName:               "codex-chat-api",
		DisableStartupMessage: true,
	})

	app.Use(recover.New())
	app.Use(requestLogger(opts))

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status": "ok",
		})
	})
	app.Get("/v1/models", models)
	app.Post("/v1/chat/completions", chatCompletions(opts))
	app.Use(func(c *fiber.Ctx) error {
		return writeError(c, fiber.StatusNotFound, "invalid_request_error", "not found")
	})

	return app
}

func models(c *fiber.Ctx) error {
	aliases := openai.ModelAliases()
	models := make([]openai.Model, 0, len(aliases))
	for _, alias := range aliases {
		models = append(models, openai.Model{
			ID:      alias.ID,
			Object:  "model",
			Created: 0,
			OwnedBy: "codex-chat-api",
		})
	}
	return c.JSON(openai.ModelListResponse{
		Object: "list",
		Data:   models,
	})
}

func chatCompletions(opts options) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if opts.logBodyShape {
			logLine(opts, "body_shape path=%s %s\n", c.Path(), redactedBodyShape(c.Body()))
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
		toolsPresent := rawJSONPresent(req.Tools)
		logLine(opts, "chat_completion model=%s stream=%t tools_present=%t\n", model, req.Stream, toolsPresent)

		ctx, cancel := requestContext(c, opts.requestContext(c))
		// Cursor and other OpenAI clients send their own tools. Faithful Codex mode
		// injects the captured CLI profile/tools and often makes those requests fail upstream.
		faithful := defaultBool(req.Faithful, !toolsPresent)
		prewarm := defaultBool(req.Prewarm, faithful)
		serviceReq := codex.Request{
			Model:             modelAlias.UpstreamModel,
			Messages:          req.Messages,
			Tools:             req.Tools,
			ToolChoice:        req.ToolChoice,
			ParallelToolCalls: req.ParallelToolCalls,
			ReasoningEffort:   defaultString(req.ReasoningEffort, modelAlias.ReasoningEffort),
			Verbosity:         defaultString(req.Verbosity, modelAlias.Verbosity),
			Faithful:          faithful,
			Prewarm:           prewarm,
		}

		if req.Stream {
			return streamChatCompletion(c, opts, ctx, cancel, serviceReq)
		}
		defer cancel()

		completion, err := opts.codexService.Complete(ctx, serviceReq)
		if err != nil {
			logLine(opts, "complete_error model=%s err=%s\n", model, detailedError(err))
			return mapServiceError(c, err)
		}

		message := openai.ChatMessage{
			Role:    "assistant",
			Content: openai.TextContent(completion.Text),
		}
		finishReason := "stop"
		if len(completion.ToolCalls) > 0 {
			message.Content = json.RawMessage("null")
			message.ToolCalls = openAIToolCalls(completion.ToolCalls)
			finishReason = "tool_calls"
		}

		return c.JSON(openai.ChatCompletionResponse{
			ID:      completionID(completion.ID, opts.newID),
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

func streamChatCompletion(c *fiber.Ctx, opts options, ctx context.Context, cancel context.CancelFunc, req codex.Request) error {
	events, err := opts.codexService.Stream(ctx, req)
	if err != nil {
		cancel()
		return mapServiceError(c, err)
	}

	id := opts.newID()
	created := opts.now().Unix()
	model := req.Model
	streamID := id

	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer cancel()

		start := opts.now()
		logLine(opts, "stream_start id=%s model=%s tools_present=%t\n", streamID, model, rawJSONPresent(req.Tools))

		var deltas, toolDeltas, upstreamEvents int
		outcome := "completed"
		toolCallEmitted := false

		// finalize logs one stream lifecycle summary regardless of how the
		// stream ends (normal completion, client disconnect, or upstream error).
		finalize := func() {
			logLine(opts,
				"stream_end id=%s model=%s outcome=%s deltas=%d tool_deltas=%d upstream_events=%d finish=%s ctx_err=%s duration_ms=%d\n",
				streamID, model, outcome, deltas, toolDeltas, upstreamEvents, streamFinish(outcome, toolCallEmitted),
				ctxErrString(ctx), opts.now().Sub(start).Milliseconds(),
			)
		}

		if !writeSSE(ctx, cancel, w, openai.ChatCompletionChunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []openai.ChatCompletionChunkChoice{
				{Index: 0, Delta: openai.ChatDelta{Role: "assistant"}},
			},
		}) {
			outcome = "client_disconnect"
			finalize()
			return
		}

		for event := range events {
			upstreamEvents++
			if event.Err != nil {
				logLine(opts, "stream_error id=%s model=%s err=%s\n", streamID, defaultString(event.Model, model), detailedError(event.Err))
				_ = writeSSE(ctx, cancel, w, errorChunk(id, created, defaultString(event.Model, model), publicErrorMessage(event.Err)))
				outcome = "upstream_error"
				break
			}
			if event.ID != "" {
				id = event.ID
			}
			if event.Model != "" {
				model = event.Model
			}
			if event.Delta != "" {
				deltas++
				if !writeSSE(ctx, cancel, w, openai.ChatCompletionChunk{
					ID:      id,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   model,
					Choices: []openai.ChatCompletionChunkChoice{
						{Index: 0, Delta: openai.ChatDelta{Content: event.Delta}},
					},
				}) {
					outcome = "client_disconnect"
					finalize()
					return
				}
			}
			for i, toolCall := range event.ToolCalls {
				toolCallEmitted = true
				toolDeltas++
				if !writeSSE(ctx, cancel, w, openai.ChatCompletionChunk{
					ID:      id,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   model,
					Choices: []openai.ChatCompletionChunkChoice{
						{Index: 0, Delta: openai.ChatDelta{ToolCalls: []openai.ToolCallDelta{openAIToolCallFullDelta(i, toolCall)}}},
					},
				}) {
					outcome = "client_disconnect"
					finalize()
					return
				}
			}
			if event.ToolCallDelta != nil {
				toolCallEmitted = true
				toolDeltas++
				if !writeSSE(ctx, cancel, w, openai.ChatCompletionChunk{
					ID:      id,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   model,
					Choices: []openai.ChatCompletionChunkChoice{
						{Index: 0, Delta: openai.ChatDelta{ToolCalls: []openai.ToolCallDelta{openAIToolCallDelta(*event.ToolCallDelta)}}},
					},
				}) {
					outcome = "client_disconnect"
					finalize()
					return
				}
			}
			if event.Done {
				break
			}
		}

		finish := "stop"
		if toolCallEmitted {
			finish = "tool_calls"
		}
		if !writeSSE(ctx, cancel, w, openai.ChatCompletionChunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []openai.ChatCompletionChunkChoice{
				{Index: 0, Delta: openai.ChatDelta{}, FinishReason: &finish},
			},
		}) {
			outcome = "client_disconnect"
			finalize()
			return
		}
		_, _ = w.Write(sse.Done())
		_ = w.Flush()
		finalize()
	})
	return nil
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

func openAIToolCalls(toolCalls []codex.ToolCall) []openai.ToolCall {
	out := make([]openai.ToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		out = append(out, openai.ToolCall{
			ID:   toolCall.ID,
			Type: defaultString(toolCall.Type, "function"),
			Function: openai.ToolCallFunction{
				Name:      toolCall.Function.Name,
				Arguments: toolCall.Function.Arguments,
			},
		})
	}
	return out
}

func openAIToolCallDelta(delta codex.ToolCallDelta) openai.ToolCallDelta {
	out := openai.ToolCallDelta{
		Index: delta.Index,
		ID:    delta.ID,
		Type:  delta.Type,
	}
	if delta.Function.Name != "" || delta.Function.Arguments != "" {
		out.Function = &openai.ToolCallFunctionDelta{
			Name:      delta.Function.Name,
			Arguments: delta.Function.Arguments,
		}
	}
	return out
}

func openAIToolCallFullDelta(index int, toolCall codex.ToolCall) openai.ToolCallDelta {
	return openai.ToolCallDelta{
		Index: index,
		ID:    toolCall.ID,
		Type:  defaultString(toolCall.Type, "function"),
		Function: &openai.ToolCallFunctionDelta{
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		},
	}
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
	if errors.Is(err, context.Canceled) {
		return writeError(c, 499, "request_canceled", "request canceled")
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
		return writeError(c, status, errorType, publicServiceMessage(serviceErr.Kind))
	}
	return writeError(c, fiber.StatusInternalServerError, "api_error", "internal server error")
}

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
	if serviceErr, ok := codex.ErrorAs(err); ok {
		return publicServiceMessage(serviceErr.Kind)
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	return "upstream error"
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
