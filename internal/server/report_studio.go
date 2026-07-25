package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/teslashibe/codex-chat-api/internal/buildinfo"
	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/openai"
	"github.com/teslashibe/codex-chat-api/internal/reportstudio"
)

const maxStructuredRequestBytes = 1024 * 1024

type structuredError struct {
	status     int
	code       string
	message    string
	retryable  bool
	retryAfter time.Duration
}

func (e *structuredError) Error() string { return e.message }

func reportStudioInference(opts options) fiber.Handler {
	allowedModels := make(map[string]bool, len(opts.contextConfig.StructuredInferenceModels))
	for _, model := range opts.contextConfig.StructuredInferenceModels {
		allowedModels[model] = true
	}
	return func(c *fiber.Ctx) error {
		start := opts.now()
		provider := "unknown"
		result := "invalid_request"
		defer func() {
			opts.metrics.ObserveStructuredLatency(provider, result, opts.now().Sub(start))
			opts.metrics.SetStructuredAdmission(opts.structuredAdmission.Active(), opts.structuredAdmission.Waiting())
		}()

		caller, authErr := structuredCaller(c, opts.contextConfig.GatewayBearerSecret, opts.contextConfig.GatewayTenantHeader)
		if authErr != nil {
			result = "auth_error"
			return writeStructuredError(c, opts, "", authErr)
		}
		if opts.drain.Load() {
			result = "overloaded"
			return writeStructuredError(c, opts, "", &structuredError{
				status: http.StatusServiceUnavailable, code: "overloaded",
				message: "server draining", retryable: true,
				retryAfter: opts.contextConfig.StructuredInferenceRetryAfter,
			})
		}
		if len(c.Body()) > maxStructuredRequestBytes {
			return writeStructuredError(c, opts, "", &structuredError{
				status: http.StatusRequestEntityTooLarge, code: "invalid_request",
				message: "request body exceeds maximum size",
			})
		}

		var req reportstudio.Request
		if err := decodeStrictJSON(c.Body(), &req); err != nil {
			return writeStructuredError(c, opts, req.RequestID, &structuredError{
				status: http.StatusBadRequest, code: "invalid_request",
				message: "invalid JSON request body",
			})
		}
		if err := validateStructuredRequest(req, opts); err != nil {
			result = err.code
			return writeStructuredError(c, opts, req.RequestID, err)
		}
		if !allowedModels[req.Model] {
			result = "unsupported_model"
			return writeStructuredError(c, opts, req.RequestID, &structuredError{
				status: http.StatusBadRequest, code: "unsupported_model",
				message: "model is not allowed by the structured inference policy",
			})
		}
		alias, ok := openai.LookupModelAlias(req.Model)
		if !ok {
			result = "unsupported_model"
			return writeStructuredError(c, opts, req.RequestID, &structuredError{
				status: http.StatusBadRequest, code: "unsupported_model",
				message: "model is not supported",
			})
		}
		provider = codex.ProviderForModel(alias.UpstreamModel)
		if provider != codex.ProviderCodex || !opts.contextConfig.ProviderEnabled(provider) {
			result = "unsupported_model"
			return writeStructuredError(c, opts, req.RequestID, &structuredError{
				status: http.StatusBadRequest, code: "unsupported_model",
				message: "model does not support structured inference",
			})
		}

		validator, err := reportstudio.CompileSchema(req.Schema)
		if err != nil {
			result = "invalid_schema"
			return writeStructuredError(c, opts, req.RequestID, &structuredError{
				status: http.StatusBadRequest, code: "invalid_schema", message: err.Error(),
			})
		}
		canonicalInput, err := canonicalJSON(req.Input)
		if err != nil {
			return writeStructuredError(c, opts, req.RequestID, &structuredError{
				status: http.StatusBadRequest, code: "invalid_request", message: "input must be one valid JSON value",
			})
		}
		inputChecksum := reportstudio.Checksum(canonicalInput)
		scope := reportstudio.Scope{
			Caller: caller, Operation: reportstudio.Operation, InputChecksum: inputChecksum,
			SchemaVersion:      req.Schema.Version,
			ModelPolicyVersion: opts.contextConfig.StructuredModelPolicyVersion,
			Model:              req.Model,
		}

		parent, cancelRequest := requestContext(c, opts.requestContext(c))
		defer cancelRequest()
		deadline := time.Duration(req.DeadlineMS) * time.Millisecond
		ctx, cancelDeadline := context.WithTimeout(parent, deadline)
		defer cancelDeadline()

		response, replayed, execErr := opts.structuredStore.Execute(ctx, caller, req.IdempotencyKey, scope, func() (reportstudio.Success, error) {
			opts.metrics.SetStructuredAdmission(opts.structuredAdmission.Active(), opts.structuredAdmission.Waiting())
			release, err := opts.structuredAdmission.Acquire(ctx)
			if err != nil {
				if errors.Is(err, reportstudio.ErrAdmissionFull) {
					opts.metrics.ObserveStructuredSaturation("rejected")
					return reportstudio.Success{}, &structuredError{
						status: http.StatusTooManyRequests, code: "overloaded",
						message: "structured inference admission is full", retryable: true,
						retryAfter: opts.contextConfig.StructuredInferenceRetryAfter,
					}
				}
				opts.metrics.ObserveStructuredSaturation("timeout")
				return reportstudio.Success{}, &structuredError{
					status: http.StatusGatewayTimeout, code: "timeout",
					message: "deadline expired while waiting for admission", retryable: true,
				}
			}
			opts.metrics.ObserveStructuredSaturation("admitted")
			opts.metrics.SetStructuredAdmission(opts.structuredAdmission.Active(), opts.structuredAdmission.Waiting())
			defer func() {
				release()
				opts.metrics.SetStructuredAdmission(opts.structuredAdmission.Active(), opts.structuredAdmission.Waiting())
			}()

			upstreamStart := opts.now()
			completion, err := opts.codexService.Complete(ctx, codex.Request{
				Model: alias.UpstreamModel,
				Messages: []openai.ChatMessage{
					{Role: "system", Content: openai.TextContent("Extract the requested data. Return only JSON matching the supplied schema.")},
					{Role: "user", Content: openai.TextContent(string(canonicalInput))},
				},
				ReasoningEffort:    req.Reasoning.Effort,
				Verbosity:          req.Verbosity,
				ResponseSchema:     req.Schema.Schema,
				ResponseSchemaName: req.Schema.Name,
				MaxOutputTokens:    req.MaxOutputTokens,
				Faithful:           false,
				Prewarm:            false,
				RequestID:          req.RequestID,
				AffinityKey:        reportstudio.ScopeFingerprint(scope),
				AffinityKeyHash:    reportstudio.ScopeFingerprint(scope),
				AffinityKeyMode:    "structured_inference",
			})
			if err != nil {
				return reportstudio.Success{}, mapStructuredServiceError(err, opts.contextConfig.StructuredInferenceRetryAfter)
			}
			if strings.TrimSpace(completion.ID) == "" {
				return reportstudio.Success{}, &structuredError{
					status: http.StatusBadGateway, code: "upstream_error",
					message: "upstream response did not include an identity", retryable: true,
				}
			}
			data, err := validator.ValidateRaw([]byte(completion.Text))
			if err != nil {
				opts.metrics.ObserveStructuredValidation("failure")
				return reportstudio.Success{}, &structuredError{
					status: http.StatusBadGateway, code: "validation_error",
					message: "model output did not satisfy the requested schema", retryable: true,
				}
			}
			opts.metrics.ObserveStructuredValidation("success")
			opts.metrics.AddStructuredUsage(provider, completion.Usage.PromptTokens, completion.Usage.CompletionTokens, completion.Usage.TotalTokens)
			return reportstudio.Success{
				ContractVersion:    reportstudio.ContractVersion,
				RequestID:          req.RequestID,
				Data:               data,
				Model:              defaultString(completion.Model, alias.UpstreamModel),
				UpstreamResponseID: completion.ID,
				Usage:              completion.Usage,
				LatencyMS:          opts.now().Sub(upstreamStart).Milliseconds(),
				Identity: reportstudio.Identity{
					ResponseID:         reportstudio.ResponseID(scope, req.IdempotencyKey),
					IdempotencyKey:     req.IdempotencyKey,
					InputChecksum:      inputChecksum,
					SchemaVersion:      req.Schema.Version,
					ModelPolicyVersion: opts.contextConfig.StructuredModelPolicyVersion,
				},
				Provenance: reportstudio.Provenance{
					SourceRevision: buildinfo.SourceRevision,
					ImageVersion:   buildinfo.ImageVersion,
				},
			}, nil
		})
		if execErr != nil {
			var mapped *structuredError
			switch {
			case errors.Is(execErr, reportstudio.ErrIdempotencyConflict):
				mapped = &structuredError{
					status: http.StatusConflict, code: "idempotency_conflict",
					message: execErr.Error(),
				}
			case errors.Is(execErr, reportstudio.ErrIdempotencyStoreFull):
				mapped = &structuredError{
					status: http.StatusServiceUnavailable, code: "overloaded",
					message: "idempotency capacity is exhausted", retryable: true,
					retryAfter: opts.contextConfig.StructuredInferenceRetryAfter,
				}
			case errors.Is(execErr, context.DeadlineExceeded), errors.Is(execErr, context.Canceled):
				mapped = &structuredError{
					status: http.StatusGatewayTimeout, code: "timeout",
					message: "structured inference deadline exceeded", retryable: true,
				}
			case errors.As(execErr, &mapped):
			default:
				mapped = &structuredError{
					status: http.StatusBadGateway, code: "upstream_error",
					message: "structured inference failed", retryable: true,
				}
			}
			result = mapped.code
			return writeStructuredError(c, opts, req.RequestID, mapped)
		}
		response.RequestID = req.RequestID
		response.Identity.Replayed = replayed
		result = "success"
		return c.JSON(response)
	}
}

func validateStructuredRequest(req reportstudio.Request, opts options) *structuredError {
	switch {
	case req.ContractVersion != reportstudio.ContractVersion:
		return &structuredError{status: http.StatusBadRequest, code: "invalid_request", message: "unsupported contract_version"}
	case !validContractID(req.RequestID):
		return &structuredError{status: http.StatusBadRequest, code: "invalid_request", message: "request_id is required and must be 1-128 safe characters"}
	case !validContractID(req.IdempotencyKey):
		return &structuredError{status: http.StatusBadRequest, code: "invalid_request", message: "idempotency_key is required and must be 1-128 safe characters"}
	case strings.TrimSpace(req.Model) == "":
		return &structuredError{status: http.StatusBadRequest, code: "invalid_request", message: "model is required"}
	case len(req.Input) == 0:
		return &structuredError{status: http.StatusBadRequest, code: "invalid_request", message: "input is required"}
	case req.MaxOutputTokens < 1 || req.MaxOutputTokens > opts.contextConfig.StructuredInferenceMaxOutputTokens:
		return &structuredError{status: http.StatusBadRequest, code: "invalid_request", message: "max_output_tokens is outside the allowed range"}
	case req.DeadlineMS < 1 || req.DeadlineMS > opts.contextConfig.StructuredInferenceMaxDeadline.Milliseconds():
		return &structuredError{status: http.StatusBadRequest, code: "invalid_request", message: "deadline_ms is outside the allowed range"}
	case !allowedReasoning(req.Reasoning.Effort):
		return &structuredError{status: http.StatusBadRequest, code: "invalid_request", message: "reasoning.effort is invalid"}
	case req.Verbosity != "low" && req.Verbosity != "medium" && req.Verbosity != "high":
		return &structuredError{status: http.StatusBadRequest, code: "invalid_request", message: "verbosity is invalid"}
	default:
		return nil
	}
}

func validContractID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char == '-' || char == '_' || char == '.' || char == ':' ||
			char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

func allowedReasoning(value string) bool {
	switch value {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func structuredCaller(c *fiber.Ctx, secret, tenantHeader string) (string, *structuredError) {
	token, ok := bearerToken(c.Get(fiber.HeaderAuthorization))
	if secret == "" || !ok || subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
		return "", &structuredError{
			status: http.StatusUnauthorized, code: "authentication_error",
			message: "authentication failed",
		}
	}
	if tenant := strings.TrimSpace(c.Get(tenantHeader)); tenant != "" {
		return "tenant:" + safeStructuredHash(tenant), nil
	}
	return "bearer:" + safeStructuredHash(token), nil
}

func safeStructuredHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func decodeStrictJSON(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func canonicalJSON(raw []byte) ([]byte, error) {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing content")
	}
	return json.Marshal(value)
}

func mapStructuredServiceError(err error, retryDefault time.Duration) *structuredError {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &structuredError{status: http.StatusGatewayTimeout, code: "timeout", message: "upstream deadline exceeded", retryable: true}
	}
	if errors.Is(err, codex.ErrUsageLimitReached) {
		return &structuredError{status: http.StatusTooManyRequests, code: "rate_limit", message: "upstream rate limit", retryable: true, retryAfter: retryDefault}
	}
	if serviceErr, ok := codex.ErrorAs(err); ok {
		switch {
		case serviceErr.Kind == codex.ErrorKindAuth:
			return &structuredError{status: http.StatusBadGateway, code: "upstream_error", message: "upstream authentication failed", retryable: true}
		case serviceErr.Status == http.StatusTooManyRequests:
			retry := serviceErr.RetryAfter
			if retry <= 0 {
				retry = retryDefault
			}
			return &structuredError{status: http.StatusTooManyRequests, code: "rate_limit", message: "upstream rate limit", retryable: true, retryAfter: retry}
		case serviceErr.Kind == codex.ErrorKindClient:
			return &structuredError{status: http.StatusBadGateway, code: "upstream_error", message: "upstream rejected structured request", retryable: false}
		default:
			return &structuredError{status: http.StatusBadGateway, code: "upstream_error", message: "upstream inference failed", retryable: true}
		}
	}
	return &structuredError{status: http.StatusBadGateway, code: "upstream_error", message: "upstream inference failed", retryable: true}
}

func writeStructuredError(c *fiber.Ctx, opts options, requestID string, err *structuredError) error {
	if err == nil {
		err = &structuredError{status: http.StatusInternalServerError, code: "upstream_error", message: "internal server error"}
	}
	retrySeconds := 0
	if err.retryAfter > 0 {
		retrySeconds = int(math.Ceil(err.retryAfter.Seconds()))
		if retrySeconds < 1 {
			retrySeconds = 1
		}
		c.Set(fiber.HeaderRetryAfter, fmt.Sprintf("%d", retrySeconds))
	}
	opts.metrics.ObserveStructuredFailure(err.code)
	return c.Status(err.status).JSON(reportstudio.ErrorResponse{
		ContractVersion: reportstudio.ContractVersion,
		Error: reportstudio.ErrorBody{
			Code: err.code, Message: err.message, RequestID: requestID,
			Retryable: err.retryable, RetryAfter: retrySeconds,
		},
	})
}
