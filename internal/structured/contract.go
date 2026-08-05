// Package structured owns the Report Studio structured-inference contract:
// the request/response envelope, the strict JSON Schema subset, the model
// policy, and the idempotency store. It is deliberately independent of the
// OpenAI-compatible surface in internal/openai so the two wire vocabularies can
// evolve separately and the public chat proxy stays byte-identical.
package structured

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/teslashibe/open-agent-api/internal/buildinfo"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

// ContractVersion is the semantic version of the wire envelope below. Bump the
// minor for additive fields and the major for anything a consumer could break
// on. It is echoed on every success and error response.
// 1.1.0 added the additive error code "idempotency_conflict"; no field changed
// shape, so a client that branches on HTTP status or ignores unknown codes was
// unaffected.
// 2.0.0 removes the documented request field "max_output_tokens": Codex
// Responses has no output-token cap, so the field promised a cost ceiling it
// could never enforce. A body that still carries it is now ignored by the JSON
// decoder rather than honoured — no field a client sends is newly rejected, but
// a documented public field disappearing is a major by this file's own rule.
// The endpoint ships dark (cfg.StructuredEnabled defaults off), so no released
// client is affected.
const ContractVersion = "2.0.0"

// Operation is the caller-declared logical operation. It scopes idempotency so
// two different extractions of the same input never collide.
const (
	maxOperationLength     = 128
	maxRequestIDLength     = 128
	maxIdempotencyKeyLen   = 200
	maxSchemaVersionLength = 64
	maxInputBytes          = 1 << 20 // 1 MiB
)

// Request is the structured-inference request envelope.
type Request struct {
	// RequestID is the caller's trace identity. It is echoed back verbatim.
	RequestID string `json:"request_id"`
	// IdempotencyKey makes a retry safe: an identical replay returns the stored
	// response instead of calling upstream again.
	IdempotencyKey string `json:"idempotency_key"`
	// Operation names the logical extraction (for example "report_summary").
	Operation string `json:"operation"`
	// Model is a public alias from the structured allowlist.
	Model string `json:"model"`
	// Input is the free-form text to extract from.
	Input string `json:"input"`
	// Schema is a strict JSON Schema subset (see schema.go).
	Schema json.RawMessage `json:"schema"`
	// SchemaVersion is the caller's version of Schema; it scopes idempotency.
	SchemaVersion string `json:"schema_version"`
	// SchemaName is optional and only used to label the upstream json_schema.
	SchemaName string `json:"schema_name,omitempty"`
	// ReasoningEffort and Verbosity override the alias defaults.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Verbosity       string `json:"verbosity,omitempty"`
	// DeadlineMS bounds the whole request, upstream call included.
	DeadlineMS int `json:"deadline_ms,omitempty"`
}

// Response is the structured-inference success envelope. Data is guaranteed to
// have been validated against the request schema before this is written.
type Response struct {
	Data               json.RawMessage `json:"data"`
	Model              string          `json:"model"`
	UpstreamResponseID string          `json:"upstream_response_id"`
	Usage              openai.Usage    `json:"usage"`
	LatencyMS          int64           `json:"latency_ms"`
	RequestID          string          `json:"request_id"`
	IdempotencyKey     string          `json:"idempotency_key"`
	IdempotentReplay   bool            `json:"idempotent_replay"`
	Operation          string          `json:"operation"`
	SchemaVersion      string          `json:"schema_version"`
	ModelPolicyVersion string          `json:"model_policy_version"`
	ContractVersion    string          `json:"contract_version"`
	Build              buildinfo.Info  `json:"build"`
}

// ErrorCode is the machine-readable failure class. Clients branch on this and
// never on the human-readable message.
type ErrorCode string

const (
	// CodeAuth is an authentication or authorization failure, inbound or upstream.
	CodeAuth ErrorCode = "auth_error"
	// CodeUnsupportedModel is a model outside the structured allowlist or behind
	// a disabled provider. The gateway never passes unknown models upstream.
	CodeUnsupportedModel ErrorCode = "unsupported_model"
	// CodeInvalidSchema is a schema outside the supported strict subset.
	CodeInvalidSchema ErrorCode = "invalid_schema"
	// CodeRateLimited is admission-control or upstream quota back-pressure.
	CodeRateLimited ErrorCode = "rate_limited"
	// CodeTimeout is the request deadline elapsing.
	CodeTimeout ErrorCode = "timeout"
	// CodeUpstream is any other upstream failure.
	CodeUpstream ErrorCode = "upstream_error"
	// CodeOutputValidation is model output that is not parseable JSON or does
	// not satisfy the request schema. It is deliberately a 4xx-shaped contract
	// failure, not a 5xx: the gateway worked, the model did not comply.
	CodeOutputValidation ErrorCode = "output_validation_failed"
	// CodeInvalidRequest is a malformed request envelope.
	CodeInvalidRequest ErrorCode = "invalid_request"
	// CodeUnavailable is the gateway refusing new work (draining).
	CodeUnavailable ErrorCode = "unavailable"
	// CodeIdempotencyConflict is an idempotency_key reused, within its TTL and
	// the same scope, with materially different inference parameters. It is a
	// deterministic 409: the gateway makes no upstream call and never replays a
	// response produced under different parameters. Retry with a fresh key, or
	// send the original parameters.
	CodeIdempotencyConflict ErrorCode = "idempotency_conflict"
)

// ErrorResponse is the structured-inference failure envelope.
type ErrorResponse struct {
	Error           ErrorBody      `json:"error"`
	RequestID       string         `json:"request_id,omitempty"`
	ContractVersion string         `json:"contract_version"`
	Build           buildinfo.Info `json:"build"`
}

// ErrorBody carries the machine-readable code plus optional violation detail.
type ErrorBody struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	// Details lists schema or output violations. It never contains model output
	// or caller credentials.
	Details []string `json:"details,omitempty"`
	// RetryAfterSeconds mirrors the Retry-After header when one is set.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
}

// Error is a contract failure carrying its machine-readable code.
type Error struct {
	Code    ErrorCode
	Message string
	Details []string
	Err     error
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Code)
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Err
}

// NewError builds a contract failure.
func NewError(code ErrorCode, message string, details ...string) *Error {
	return &Error{Code: code, Message: message, Details: details}
}

// ErrorAs extracts a contract failure from an error chain.
func ErrorAs(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// Normalize trims the caller-controlled string fields in place so downstream
// hashing, logging, and metrics see a canonical value.
func (r *Request) Normalize() {
	r.RequestID = strings.TrimSpace(r.RequestID)
	r.IdempotencyKey = strings.TrimSpace(r.IdempotencyKey)
	r.Operation = strings.TrimSpace(r.Operation)
	r.Model = strings.TrimSpace(r.Model)
	r.SchemaVersion = strings.TrimSpace(r.SchemaVersion)
	r.SchemaName = strings.TrimSpace(r.SchemaName)
	r.ReasoningEffort = strings.TrimSpace(r.ReasoningEffort)
	r.Verbosity = strings.TrimSpace(r.Verbosity)
}

// Validate checks the envelope itself. Schema compilation and model policy are
// separate steps so they can report their own error codes.
func (r Request) Validate() *Error {
	switch {
	case r.RequestID == "":
		return NewError(CodeInvalidRequest, "request_id is required")
	case len(r.RequestID) > maxRequestIDLength:
		return NewError(CodeInvalidRequest, "request_id is too long")
	case r.IdempotencyKey == "":
		return NewError(CodeInvalidRequest, "idempotency_key is required")
	case len(r.IdempotencyKey) > maxIdempotencyKeyLen:
		return NewError(CodeInvalidRequest, "idempotency_key is too long")
	case r.Operation == "":
		return NewError(CodeInvalidRequest, "operation is required")
	case len(r.Operation) > maxOperationLength:
		return NewError(CodeInvalidRequest, "operation is too long")
	case r.Model == "":
		return NewError(CodeInvalidRequest, "model is required")
	case strings.TrimSpace(r.Input) == "":
		return NewError(CodeInvalidRequest, "input is required")
	case len(r.Input) > maxInputBytes:
		return NewError(CodeInvalidRequest, "input exceeds the maximum size")
	case len(r.Schema) == 0:
		return NewError(CodeInvalidRequest, "schema is required")
	case r.SchemaVersion == "":
		return NewError(CodeInvalidRequest, "schema_version is required")
	case len(r.SchemaVersion) > maxSchemaVersionLength:
		return NewError(CodeInvalidRequest, "schema_version is too long")
	case r.DeadlineMS < 0:
		return NewError(CodeInvalidRequest, "deadline_ms must be non-negative")
	}
	if err := validateEffort(r.ReasoningEffort); err != nil {
		return err
	}
	return validateVerbosity(r.Verbosity)
}

func validateEffort(effort string) *Error {
	switch effort {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return nil
	default:
		return NewError(CodeInvalidRequest, "unsupported reasoning_effort")
	}
}

func validateVerbosity(verbosity string) *Error {
	switch verbosity {
	case "", "low", "medium", "high":
		return nil
	default:
		return NewError(CodeInvalidRequest, "unsupported verbosity")
	}
}
