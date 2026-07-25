package reportstudio

import (
	"encoding/json"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/openai"
)

const (
	ContractVersion = "report-studio.structured-inference.v1"
	Operation       = "structured_inference"
)

type Request struct {
	ContractVersion string          `json:"contract_version"`
	RequestID       string          `json:"request_id"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	Schema          SchemaRequest   `json:"schema"`
	Reasoning       Reasoning       `json:"reasoning"`
	Verbosity       string          `json:"verbosity"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	DeadlineMS      int64           `json:"deadline_ms"`
}

type SchemaRequest struct {
	Name    string          `json:"name"`
	Version string          `json:"version"`
	Strict  bool            `json:"strict"`
	Schema  json.RawMessage `json:"json_schema"`
}

type Reasoning struct {
	Effort string `json:"effort"`
}

type Success struct {
	ContractVersion    string          `json:"contract_version"`
	RequestID          string          `json:"request_id"`
	Data               json.RawMessage `json:"data"`
	Model              string          `json:"model"`
	UpstreamResponseID string          `json:"upstream_response_id"`
	Usage              openai.Usage    `json:"usage"`
	LatencyMS          int64           `json:"latency_ms"`
	Identity           Identity        `json:"identity"`
	Provenance         Provenance      `json:"provenance"`
}

type Identity struct {
	ResponseID         string `json:"response_id"`
	IdempotencyKey     string `json:"idempotency_key"`
	InputChecksum      string `json:"input_checksum"`
	SchemaVersion      string `json:"schema_version"`
	ModelPolicyVersion string `json:"model_policy_version"`
	Replayed           bool   `json:"replayed"`
}

type Provenance struct {
	SourceRevision string `json:"source_revision"`
	ImageVersion   string `json:"image_version"`
}

type ErrorResponse struct {
	ContractVersion string    `json:"contract_version"`
	Error           ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	RequestID  string `json:"request_id,omitempty"`
	Retryable  bool   `json:"retryable"`
	RetryAfter int    `json:"retry_after_seconds,omitempty"`
}

type Scope struct {
	Caller             string
	Operation          string
	InputChecksum      string
	SchemaVersion      string
	ModelPolicyVersion string
	Model              string
}

type CachedResult struct {
	Response Success
	Expires  time.Time
}
