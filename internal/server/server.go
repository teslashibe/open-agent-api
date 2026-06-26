package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
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
}

func WithCodexService(service codex.Service) Option {
	return func(opts *options) {
		opts.codexService = service
	}
}

func New(cfg config.Config, setters ...Option) *fiber.App {
	_ = cfg
	opts := options{
		codexService: codex.UnavailableService{},
		requestContext: func(c *fiber.Ctx) context.Context {
			return c.UserContext()
		},
		now: time.Now,
		newID: func() string {
			return "chatcmpl-" + uuid.NewString()
		},
	}
	for _, setter := range setters {
		setter(&opts)
	}

	app := fiber.New(fiber.Config{
		AppName:               "codex-chat-api",
		DisableStartupMessage: true,
	})

	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format:     "${time} ${status} ${method} ${path} ${latency}\n",
		TimeFormat: "2006-01-02T15:04:05Z07:00",
	}))

	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status": "ok",
		})
	})
	app.Post("/v1/chat/completions", chatCompletions(opts))

	return app
}

func chatCompletions(opts options) fiber.Handler {
	return func(c *fiber.Ctx) error {
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
		ctx, cancel := requestContext(c, opts.requestContext(c))
		serviceReq := codex.Request{
			Model:           model,
			Messages:        req.Messages,
			ReasoningEffort: defaultString(req.ReasoningEffort, "medium"),
			Verbosity:       defaultString(req.Verbosity, "medium"),
			Faithful:        defaultBool(req.Faithful, true),
			Prewarm:         defaultBool(req.Prewarm, true),
		}

		if req.Stream {
			return streamChatCompletion(c, opts, ctx, cancel, serviceReq)
		}
		defer cancel()

		completion, err := opts.codexService.Complete(ctx, serviceReq)
		if err != nil {
			return mapServiceError(c, err)
		}

		return c.JSON(openai.ChatCompletionResponse{
			ID:      completionID(completion.ID, opts.newID),
			Object:  "chat.completion",
			Created: opts.now().Unix(),
			Model:   defaultString(completion.Model, model),
			Choices: []openai.ChatCompletionChoice{
				{
					Index: 0,
					Message: openai.ChatMessage{
						Role:    "assistant",
						Content: openai.TextContent(completion.Text),
					},
					FinishReason: "stop",
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

	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer cancel()
		if !writeSSE(ctx, cancel, w, openai.ChatCompletionChunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []openai.ChatCompletionChunkChoice{
				{Index: 0, Delta: openai.ChatDelta{Role: "assistant"}},
			},
		}) {
			return
		}

		for event := range events {
			if event.Err != nil {
				_ = writeSSE(ctx, cancel, w, errorChunk(id, created, defaultString(event.Model, model), publicErrorMessage(event.Err)))
				break
			}
			if event.ID != "" {
				id = event.ID
			}
			if event.Model != "" {
				model = event.Model
			}
			if event.Delta != "" {
				if !writeSSE(ctx, cancel, w, openai.ChatCompletionChunk{
					ID:      id,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   model,
					Choices: []openai.ChatCompletionChunkChoice{
						{Index: 0, Delta: openai.ChatDelta{Content: event.Delta}},
					},
				}) {
					return
				}
			}
			if event.Done {
				break
			}
		}

		finish := "stop"
		if !writeSSE(ctx, cancel, w, openai.ChatCompletionChunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []openai.ChatCompletionChunkChoice{
				{Index: 0, Delta: openai.ChatDelta{}, FinishReason: &finish},
			},
		}) {
			return
		}
		_, _ = w.Write(sse.Done())
		_ = w.Flush()
	})
	return nil
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
