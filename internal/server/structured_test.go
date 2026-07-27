package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/config"
	metricspkg "github.com/teslashibe/open-agent-api/internal/metrics"
	"github.com/teslashibe/open-agent-api/internal/openai"
	"github.com/teslashibe/open-agent-api/internal/structured"
)

const structuredTestSchema = `{"type":"object","additionalProperties":false,"required":["title","score"],"properties":{"title":{"type":"string"},"score":{"type":"integer"}}}`

const structuredTestOutput = `{"title":"Q3 report","score":7}`

func structuredTestConfig() config.Config {
	cfg := config.Defaults()
	cfg.StructuredEnabled = true
	cfg.DegenerateTurnRetryEnabled = false
	cfg.MetricsEnabled = true
	return cfg
}

type structuredRequestBody struct {
	RequestID       string          `json:"request_id"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Operation       string          `json:"operation"`
	Model           string          `json:"model"`
	Input           string          `json:"input"`
	Schema          json.RawMessage `json:"schema"`
	SchemaVersion   string          `json:"schema_version"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Verbosity       string          `json:"verbosity,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	DeadlineMS      int             `json:"deadline_ms,omitempty"`
}

func structuredBody() structuredRequestBody {
	return structuredRequestBody{
		RequestID:      "req-1",
		IdempotencyKey: "idem-1",
		Operation:      "report_summary",
		Model:          openai.DefaultModel,
		Input:          "Revenue rose in Q3.",
		Schema:         json.RawMessage(structuredTestSchema),
		SchemaVersion:  "v1",
	}
}

func staticStructuredService(text string) fakeCodexService {
	return fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			return codex.Completion{
				Text:  text,
				Model: openai.DefaultModel,
				ID:    "resp-upstream-1",
				Usage: openai.Usage{PromptTokens: 11, CompletionTokens: 5, TotalTokens: 16},
			}, nil
		},
	}
}

func postStructured(t *testing.T, app *fiber.App, body any, headers ...map[string]string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, StructuredPath, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-access-token")
	for _, headerSet := range headers {
		for key, value := range headerSet {
			req.Header.Set(key, value)
		}
	}
	resp, err := app.Test(req, 5000)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	return resp
}

func decodeStructuredSuccess(t *testing.T, resp *http.Response) structured.Response {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body structured.Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode success: %v", err)
	}
	return body
}

func decodeStructuredError(t *testing.T, resp *http.Response, wantStatus int, wantCode structured.ErrorCode) structured.ErrorResponse {
	t.Helper()
	defer resp.Body.Close()
	var body structured.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d (body %#v)", resp.StatusCode, wantStatus, body)
	}
	if body.Error.Code != wantCode {
		t.Fatalf("code = %q, want %q (body %#v)", body.Error.Code, wantCode, body)
	}
	if body.ContractVersion != structured.ContractVersion {
		t.Fatalf("contract_version = %q, want %q", body.ContractVersion, structured.ContractVersion)
	}
	return body
}

// AC1, AC2: the success envelope carries data, the actual model, the upstream
// response id, usage, latency, and retry-safe identity.
func TestStructuredInferenceReturnsSchemaValidSuccessEnvelope(t *testing.T) {
	app := New(structuredTestConfig(), WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(nil))

	body := decodeStructuredSuccess(t, postStructured(t, app, structuredBody()))

	if string(body.Data) != structuredTestOutput {
		t.Fatalf("data = %s, want %s", body.Data, structuredTestOutput)
	}
	if body.Model != openai.DefaultModel {
		t.Fatalf("model = %q", body.Model)
	}
	if body.UpstreamResponseID != "resp-upstream-1" {
		t.Fatalf("upstream_response_id = %q", body.UpstreamResponseID)
	}
	if body.Usage.TotalTokens != 16 || body.Usage.PromptTokens != 11 || body.Usage.CompletionTokens != 5 {
		t.Fatalf("usage = %#v", body.Usage)
	}
	if body.LatencyMS < 0 {
		t.Fatalf("latency_ms = %d", body.LatencyMS)
	}
	if body.RequestID != "req-1" || body.IdempotencyKey != "idem-1" || body.IdempotentReplay {
		t.Fatalf("identity = %#v", body)
	}
	if body.Operation != "report_summary" || body.SchemaVersion != "v1" {
		t.Fatalf("scoping = %#v", body)
	}
	if body.ContractVersion != structured.ContractVersion || body.ModelPolicyVersion == "" {
		t.Fatalf("versions = %q %q", body.ContractVersion, body.ModelPolicyVersion)
	}
	// AC9: provenance travels with every response.
	if body.Build.Version == "" || body.Build.GoVersion == "" {
		t.Fatalf("build = %#v", body.Build)
	}
	// The returned data must itself satisfy the requested schema.
	schema, schemaErr := structured.CompileSchema(json.RawMessage(structuredTestSchema))
	if schemaErr != nil {
		t.Fatal(schemaErr)
	}
	if err := structured.Validate(schema, body.Data); err != nil {
		t.Fatalf("success payload violates its own schema: %v", err.Details)
	}
}

// AC4: extraction mode disables tools, the faithful profile, and prewarm.
func TestStructuredInferenceUsesExtractionMode(t *testing.T) {
	var seen codex.Request
	service := fakeCodexService{
		complete: func(_ context.Context, req codex.Request) (codex.Completion, error) {
			seen = req
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel, ID: "resp-1"}, nil
		},
	}
	cfg := structuredTestConfig()
	cfg.StructuredMaxOutputTokens = 4096
	app := New(cfg, WithCodexService(service), WithLogOutput(nil))

	request := structuredBody()
	request.MaxOutputTokens = 256
	request.ReasoningEffort = "high"
	request.Verbosity = "low"
	decodeStructuredSuccess(t, postStructured(t, app, request))

	if !seen.Extraction {
		t.Fatal("codex request was not marked as an extraction turn")
	}
	if seen.Faithful || seen.Prewarm {
		t.Fatalf("extraction request enabled faithful/prewarm: %#v", seen)
	}
	if len(seen.Tools) != 0 || len(seen.ToolChoice) != 0 {
		t.Fatalf("extraction request carried tools: %s %s", seen.Tools, seen.ToolChoice)
	}
	if seen.MaxOutputTokens != 256 {
		t.Fatalf("max_output_tokens = %d, want the caller's 256", seen.MaxOutputTokens)
	}
	if seen.ReasoningEffort != "high" || seen.Verbosity != "low" {
		t.Fatalf("reasoning/verbosity = %q %q", seen.ReasoningEffort, seen.Verbosity)
	}
	var format map[string]any
	if err := json.Unmarshal(seen.ResponseFormat, &format); err != nil {
		t.Fatalf("response_format = %s: %v", seen.ResponseFormat, err)
	}
	if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "report_summary" {
		t.Fatalf("response_format = %#v", format)
	}
}

func TestStructuredInferenceClampsOutputTokensAndDeadline(t *testing.T) {
	var seen codex.Request
	var deadline time.Time
	service := fakeCodexService{
		complete: func(ctx context.Context, req codex.Request) (codex.Completion, error) {
			seen = req
			deadline, _ = ctx.Deadline()
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel}, nil
		},
	}
	cfg := structuredTestConfig()
	cfg.StructuredMaxOutputTokens = 1024
	cfg.StructuredMaxDeadline = 2 * time.Second
	app := New(cfg, WithCodexService(service), WithLogOutput(nil))

	request := structuredBody()
	request.MaxOutputTokens = 999999
	request.DeadlineMS = 600000
	decodeStructuredSuccess(t, postStructured(t, app, request))

	if seen.MaxOutputTokens != 1024 {
		t.Fatalf("max_output_tokens = %d, want the configured cap 1024", seen.MaxOutputTokens)
	}
	if deadline.IsZero() || time.Until(deadline) > 3*time.Second {
		t.Fatalf("deadline = %s, want the configured 2s cap", time.Until(deadline))
	}
}

// AC3: every failure class has its own machine-readable code.
func TestStructuredInferenceErrorContracts(t *testing.T) {
	invalidSchemaBody := structuredBody()
	invalidSchemaBody.Schema = json.RawMessage(`{"type":"object","properties":{"a":{"$ref":"#/x"}}}`)

	unsupportedModelBody := structuredBody()
	unsupportedModelBody.Model = "gemini-2.5-pro"

	missingFieldBody := structuredBody()
	missingFieldBody.Operation = ""

	cases := map[string]struct {
		body       any
		service    codex.Service
		wantStatus int
		wantCode   structured.ErrorCode
	}{
		"invalid schema": {
			body:       invalidSchemaBody,
			service:    staticStructuredService(structuredTestOutput),
			wantStatus: http.StatusBadRequest,
			wantCode:   structured.CodeInvalidSchema,
		},
		"unsupported model": {
			body:       unsupportedModelBody,
			service:    staticStructuredService(structuredTestOutput),
			wantStatus: http.StatusNotFound,
			wantCode:   structured.CodeUnsupportedModel,
		},
		"invalid request": {
			body:       missingFieldBody,
			service:    staticStructuredService(structuredTestOutput),
			wantStatus: http.StatusBadRequest,
			wantCode:   structured.CodeInvalidRequest,
		},
		"upstream auth": {
			body:       structuredBody(),
			service:    errorStructuredService(codex.NewError(codex.ErrorKindAuth, http.StatusUnauthorized, "bad token", nil)),
			wantStatus: http.StatusUnauthorized,
			wantCode:   structured.CodeAuth,
		},
		"upstream quota": {
			body:       structuredBody(),
			service:    errorStructuredService(fmt.Errorf("quota: %w", codex.ErrUsageLimitReached)),
			wantStatus: http.StatusTooManyRequests,
			wantCode:   structured.CodeRateLimited,
		},
		"upstream 429": {
			body:       structuredBody(),
			service:    errorStructuredService(&codex.Error{Kind: codex.ErrorKindUpstream, Status: http.StatusTooManyRequests, Message: "slow down", RetryAfter: 30 * time.Second}),
			wantStatus: http.StatusTooManyRequests,
			wantCode:   structured.CodeRateLimited,
		},
		"upstream failure": {
			body:       structuredBody(),
			service:    errorStructuredService(errors.New("socket closed")),
			wantStatus: http.StatusBadGateway,
			wantCode:   structured.CodeUpstream,
		},
		"output not json": {
			body:       structuredBody(),
			service:    staticStructuredService("I cannot help with that."),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   structured.CodeOutputValidation,
		},
		"output violates schema": {
			body:       structuredBody(),
			service:    staticStructuredService(`{"title":"ok","score":"seven"}`),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   structured.CodeOutputValidation,
		},
		"output has extra properties": {
			body:       structuredBody(),
			service:    staticStructuredService(`{"title":"ok","score":1,"extra":true}`),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   structured.CodeOutputValidation,
		},
		"output truncated": {
			body:       structuredBody(),
			service:    staticStructuredService(`{"title":"ok","score":`),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   structured.CodeOutputValidation,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			app := New(structuredTestConfig(), WithCodexService(tc.service), WithLogOutput(nil))
			body := decodeStructuredError(t, postStructured(t, app, tc.body), tc.wantStatus, tc.wantCode)
			if body.Error.Message == "" {
				t.Fatal("error message is empty")
			}
		})
	}
}

func TestStructuredInferenceUpstreamRateLimitCarriesRetryAfter(t *testing.T) {
	service := errorStructuredService(&codex.Error{
		Kind:       codex.ErrorKindUpstream,
		Status:     http.StatusTooManyRequests,
		Message:    "slow down",
		RetryAfter: 30 * time.Second,
	})
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(nil))

	resp := postStructured(t, app, structuredBody())
	retryAfter := resp.Header.Get(fiber.HeaderRetryAfter)
	body := decodeStructuredError(t, resp, http.StatusTooManyRequests, structured.CodeRateLimited)
	if retryAfter != "30" {
		t.Fatalf("Retry-After = %q, want the upstream hint 30", retryAfter)
	}
	if body.Error.RetryAfterSeconds != 30 {
		t.Fatalf("retry_after_seconds = %d, want 30", body.Error.RetryAfterSeconds)
	}
}

func TestStructuredInferenceRejectsMalformedJSONBody(t *testing.T) {
	app := New(structuredTestConfig(), WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(nil))

	req, err := http.NewRequest(http.MethodPost, StructuredPath, bytes.NewBufferString(`{"request_id":`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-access-token")
	resp, err := app.Test(req, 2000)
	if err != nil {
		t.Fatal(err)
	}
	decodeStructuredError(t, resp, http.StatusBadRequest, structured.CodeInvalidRequest)
}

// AC3: inbound auth failures also speak the structured vocabulary.
func TestStructuredInferenceAuthErrorUsesContractCode(t *testing.T) {
	cfg := structuredTestConfig()
	cfg.GatewayBearerSecret = "gateway-secret"
	app := New(cfg, WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(nil))

	resp := postStructured(t, app, structuredBody(), map[string]string{"Authorization": "Bearer wrong"})
	decodeStructuredError(t, resp, http.StatusUnauthorized, structured.CodeAuth)

	// The correct secret still reaches the handler.
	resp = postStructured(t, app, structuredBody(), map[string]string{"Authorization": "Bearer gateway-secret"})
	decodeStructuredSuccess(t, resp)
}

// AC3: an elapsed deadline is a timeout, not a generic upstream error.
func TestStructuredInferenceDeadlineReturnsTimeout(t *testing.T) {
	service := fakeCodexService{
		complete: func(ctx context.Context, _ codex.Request) (codex.Completion, error) {
			<-ctx.Done()
			return codex.Completion{}, ctx.Err()
		},
	}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(nil))

	request := structuredBody()
	request.DeadlineMS = 40
	decodeStructuredError(t, postStructured(t, app, request), http.StatusGatewayTimeout, structured.CodeTimeout)
}

// AC5: bounded admission sheds load with 429 + Retry-After.
func TestStructuredInferenceQueueFullReturns429WithRetryAfter(t *testing.T) {
	release := make(chan struct{})
	admitted := make(chan struct{}, 1)
	service := fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			admitted <- struct{}{}
			<-release
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel}, nil
		},
	}
	cfg := structuredTestConfig()
	cfg.StructuredMaxActive = 1
	cfg.StructuredMaxActivePerKey = 1
	cfg.StructuredQueueLimit = 0
	app := New(cfg, WithCodexService(service), WithLogOutput(nil))

	first := make(chan *http.Response, 1)
	go func() {
		body := structuredBody()
		body.IdempotencyKey = "idem-slow"
		first <- postStructured(t, app, body)
	}()
	<-admitted

	second := structuredBody()
	second.IdempotencyKey = "idem-shed"
	resp := postStructured(t, app, second)
	retryAfter := resp.Header.Get(fiber.HeaderRetryAfter)
	body := decodeStructuredError(t, resp, http.StatusTooManyRequests, structured.CodeRateLimited)
	if retryAfter == "" {
		t.Fatal("429 response is missing Retry-After")
	}
	if seconds, err := strconv.Atoi(retryAfter); err != nil || seconds < 1 {
		t.Fatalf("Retry-After = %q, want a positive integer", retryAfter)
	}
	if body.Error.RetryAfterSeconds < 1 {
		t.Fatalf("retry_after_seconds = %d", body.Error.RetryAfterSeconds)
	}

	close(release)
	decodeStructuredSuccess(t, <-first)
}

// AC5: draining sheds structured work with 503 + Retry-After before any
// upstream call.
func TestStructuredInferenceDrainingReturns503WithRetryAfter(t *testing.T) {
	service := fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			t.Error("draining gateway called upstream")
			return codex.Completion{}, errors.New("unexpected")
		},
	}
	app := New(
		structuredTestConfig(),
		WithCodexService(service),
		WithLogOutput(nil),
		withLocalCheck(func(*fiber.Ctx) bool { return true }),
	)
	if status := postStatus(t, app, "/drain/start"); status != http.StatusOK {
		t.Fatalf("drain start status = %d", status)
	}

	resp := postStructured(t, app, structuredBody())
	retryAfter := resp.Header.Get(fiber.HeaderRetryAfter)
	decodeStructuredError(t, resp, http.StatusServiceUnavailable, structured.CodeUnavailable)
	if retryAfter == "" {
		t.Fatal("503 response is missing Retry-After")
	}
}

// AC6: a replay of the same identity returns the stored response without a
// second upstream call.
func TestStructuredInferenceReplaysIdempotentRequests(t *testing.T) {
	var calls int32
	service := fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			atomic.AddInt32(&calls, 1)
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel, ID: "resp-idem"}, nil
		},
	}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(nil))

	first := decodeStructuredSuccess(t, postStructured(t, app, structuredBody()))
	second := decodeStructuredSuccess(t, postStructured(t, app, structuredBody()))

	if first.IdempotentReplay {
		t.Fatal("first response reported a replay")
	}
	if !second.IdempotentReplay {
		t.Fatal("second response did not report a replay")
	}
	if string(second.Data) != string(first.Data) || second.UpstreamResponseID != first.UpstreamResponseID {
		t.Fatalf("replay diverged: %#v vs %#v", second, first)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

// Issue 120 AC1, AC2, AC3: with the file backend two independent gateway
// processes over one shared directory replay each other's stored response. App
// B is both "the other pod" and "the same pod after a restart": it has its own
// empty in-memory store and its own upstream service, so a replay there can
// only have come from the durable record.
func TestStructuredInferenceReplaysAcrossRestartsAndReplicas(t *testing.T) {
	dir := t.TempDir()
	cfg := structuredTestConfig()
	cfg.StructuredIdempotencyBackend = config.IdempotencyBackendFile
	cfg.StructuredIdempotencyDir = dir

	var callsA, callsB int32
	appA := New(cfg, WithCodexService(fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			atomic.AddInt32(&callsA, 1)
			return codex.Completion{
				Text:  structuredTestOutput,
				Model: openai.DefaultModel,
				ID:    "resp-pod-a",
				Usage: openai.Usage{PromptTokens: 11, CompletionTokens: 5, TotalTokens: 16},
			}, nil
		},
	}), WithLogOutput(nil))
	appB := New(cfg, WithCodexService(fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			atomic.AddInt32(&callsB, 1)
			return codex.Completion{Text: `{"title":"divergent","score":0}`, Model: openai.DefaultModel, ID: "resp-pod-b"}, nil
		},
	}), WithLogOutput(nil))

	first := decodeStructuredSuccess(t, postStructured(t, appA, structuredBody()))
	if first.IdempotentReplay {
		t.Fatal("pod A reported a replay for the original request")
	}
	second := decodeStructuredSuccess(t, postStructured(t, appB, structuredBody()))
	if !second.IdempotentReplay {
		t.Fatal("pod B did not replay the record pod A stored")
	}
	if got := atomic.LoadInt32(&callsB); got != 0 {
		t.Fatalf("pod B upstream calls = %d, want 0", got)
	}
	if got := atomic.LoadInt32(&callsA); got != 1 {
		t.Fatalf("pod A upstream calls = %d, want 1", got)
	}

	// Everything but the freshly measured replay latency must be identical.
	normalize := func(response structured.Response) string {
		response.LatencyMS = 0
		response.IdempotentReplay = false
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if normalize(second) != normalize(first) {
		t.Fatalf("replayed body diverged:\n B: %s\n A: %s", normalize(second), normalize(first))
	}
	if second.UpstreamResponseID != "resp-pod-a" || string(second.Data) != structuredTestOutput {
		t.Fatalf("pod B served %#v, want pod A's stored response", second)
	}
}

// Issue 120 AC1: the memory backend stays process-local, which is exactly why
// Config.Validate fails closed on a multi-replica memory deployment.
func TestStructuredInferenceMemoryBackendStaysProcessLocal(t *testing.T) {
	cfg := structuredTestConfig()
	var callsB int32
	appA := New(cfg, WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(nil))
	appB := New(cfg, WithCodexService(fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			atomic.AddInt32(&callsB, 1)
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel, ID: "resp-pod-b"}, nil
		},
	}), WithLogOutput(nil))

	decodeStructuredSuccess(t, postStructured(t, appA, structuredBody()))
	second := decodeStructuredSuccess(t, postStructured(t, appB, structuredBody()))
	if second.IdempotentReplay || atomic.LoadInt32(&callsB) != 1 {
		t.Fatalf("memory backend replayed across processes (replay=%v calls=%d)", second.IdempotentReplay, callsB)
	}
}

// Issue 122 AC2: the process-local limit of the memory backend is stated at
// startup, not left for an operator to infer from the absence of an error.
func TestStructuredEnabledMemoryBackendWarnsAtStartup(t *testing.T) {
	var logs bytes.Buffer
	New(structuredTestConfig(), WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(&logs))

	line := logs.String()
	if !strings.Contains(line, "structured_idempotency_warning") {
		t.Fatalf("startup log = %q, want a structured_idempotency_warning line", line)
	}
	for _, want := range []string{"process-local", "rolling update", "maxSurge", "Recreate"} {
		if !strings.Contains(line, want) {
			t.Fatalf("warning = %q, want it to mention %q", line, want)
		}
	}

	// A dark gateway is unchanged: no warning, because nothing can double-call.
	var dark bytes.Buffer
	cfg := structuredTestConfig()
	cfg.StructuredEnabled = false
	New(cfg, WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(&dark))
	if strings.Contains(dark.String(), "structured_idempotency_warning") {
		t.Fatalf("startup log with structured off = %q, want no warning", dark.String())
	}
}

// AC6: idempotency is scoped by caller, operation, input, schema version, and
// model policy version. Changing any of them must re-run the inference.
func TestStructuredInferenceIdempotencyIsScoped(t *testing.T) {
	for name, mutate := range map[string]func(*structuredRequestBody, map[string]string){
		"caller":         func(_ *structuredRequestBody, h map[string]string) { h[config.DefaultGatewayTenantHeader] = "tenant-b" },
		"operation":      func(b *structuredRequestBody, _ map[string]string) { b.Operation = "other_operation" },
		"input":          func(b *structuredRequestBody, _ map[string]string) { b.Input = "Different input." },
		"schema version": func(b *structuredRequestBody, _ map[string]string) { b.SchemaVersion = "v2" },
	} {
		t.Run(name, func(t *testing.T) {
			var calls int32
			service := fakeCodexService{
				complete: func(context.Context, codex.Request) (codex.Completion, error) {
					atomic.AddInt32(&calls, 1)
					return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel}, nil
				},
			}
			app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(nil))

			baseHeaders := map[string]string{config.DefaultGatewayTenantHeader: "tenant-a"}
			decodeStructuredSuccess(t, postStructured(t, app, structuredBody(), baseHeaders))

			scoped := structuredBody()
			scopedHeaders := map[string]string{config.DefaultGatewayTenantHeader: "tenant-a"}
			mutate(&scoped, scopedHeaders)

			body := decodeStructuredSuccess(t, postStructured(t, app, scoped, scopedHeaders))
			if body.IdempotentReplay {
				t.Fatalf("changing the %s scope replayed a stored response", name)
			}
			if got := atomic.LoadInt32(&calls); got != 2 {
				t.Fatalf("upstream calls = %d, want 2 (%s must not share a key)", got, name)
			}
		})
	}
}

// A different model policy version invalidates stored responses (AC6).
func TestStructuredInferenceModelPolicyVersionScopesIdempotency(t *testing.T) {
	narrow := structuredTestConfig()
	narrow.StructuredModels = []string{openai.DefaultModel}
	wide := structuredTestConfig()
	wide.StructuredModels = []string{openai.DefaultModel, "gpt-5.6-terra"}

	store := structured.NewIdempotencyStore(time.Minute, 16, nil)
	var calls int32
	service := fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			atomic.AddInt32(&calls, 1)
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel}, nil
		},
	}

	narrowApp := New(narrow, WithCodexService(service), WithLogOutput(nil), withStructuredIdempotency(store))
	wideApp := New(wide, WithCodexService(service), WithLogOutput(nil), withStructuredIdempotency(store))

	first := decodeStructuredSuccess(t, postStructured(t, narrowApp, structuredBody()))
	second := decodeStructuredSuccess(t, postStructured(t, wideApp, structuredBody()))

	if first.ModelPolicyVersion == second.ModelPolicyVersion {
		t.Fatalf("model policy version did not change with the allowlist (%q)", first.ModelPolicyVersion)
	}
	if second.IdempotentReplay {
		t.Fatal("a changed model policy version replayed a stale response")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

// AC12: concurrency at 1/2/4/8 — no lost or duplicated work.
func TestStructuredInferenceConcurrency(t *testing.T) {
	for _, concurrency := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprintf("distinct/%d", concurrency), func(t *testing.T) {
			var calls int32
			service := fakeCodexService{
				complete: func(_ context.Context, req codex.Request) (codex.Completion, error) {
					atomic.AddInt32(&calls, 1)
					return codex.Completion{
						Text:  fmt.Sprintf(`{"title":%q,"score":1}`, req.RequestID),
						Model: openai.DefaultModel,
						ID:    "resp-" + req.RequestID,
					}, nil
				},
			}
			cfg := structuredTestConfig()
			cfg.StructuredMaxActive = concurrency
			cfg.StructuredMaxActivePerKey = concurrency
			cfg.StructuredQueueLimit = concurrency * 2
			app := New(cfg, WithCodexService(service), WithLogOutput(nil))

			results := make([]structured.Response, concurrency)
			var wg sync.WaitGroup
			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					body := structuredBody()
					body.RequestID = fmt.Sprintf("req-%d", index)
					body.IdempotencyKey = fmt.Sprintf("idem-%d", index)
					body.Input = fmt.Sprintf("input %d", index)
					results[index] = decodeStructuredSuccess(t, postStructured(t, app, body))
				}(i)
			}
			wg.Wait()

			if got := atomic.LoadInt32(&calls); int(got) != concurrency {
				t.Fatalf("upstream calls = %d, want %d (no lost or duplicated work)", got, concurrency)
			}
			for index, result := range results {
				wantID := fmt.Sprintf("req-%d", index)
				if result.RequestID != wantID {
					t.Fatalf("result %d request_id = %q, want %q", index, result.RequestID, wantID)
				}
				if result.IdempotentReplay {
					t.Fatalf("result %d was a replay of distinct work", index)
				}
				if want := fmt.Sprintf(`{"title":%q,"score":1}`, wantID); string(result.Data) != want {
					t.Fatalf("result %d data = %s, want %s", index, result.Data, want)
				}
			}
		})

		t.Run(fmt.Sprintf("duplicate/%d", concurrency), func(t *testing.T) {
			assertStructuredDuplicatesSingleFlight(t, concurrency, func(*config.Config) {})
		})

		// The same guarantee must hold with the durable backend in the path,
		// where single-flight also runs through a filesystem reservation.
		t.Run(fmt.Sprintf("duplicate-file-backend/%d", concurrency), func(t *testing.T) {
			dir := t.TempDir()
			assertStructuredDuplicatesSingleFlight(t, concurrency, func(cfg *config.Config) {
				cfg.StructuredIdempotencyBackend = config.IdempotencyBackendFile
				cfg.StructuredIdempotencyDir = dir
			})
		})
	}
}

// assertStructuredDuplicatesSingleFlight sends `concurrency` identical requests
// at once and asserts exactly one upstream call and one non-replay response.
func assertStructuredDuplicatesSingleFlight(t *testing.T, concurrency int, configure func(*config.Config)) {
	t.Helper()
	var calls int32
	started := make(chan struct{}, concurrency)
	release := make(chan struct{})
	service := fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			atomic.AddInt32(&calls, 1)
			started <- struct{}{}
			<-release
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel, ID: "resp-shared"}, nil
		},
	}
	cfg := structuredTestConfig()
	cfg.StructuredMaxActive = concurrency
	cfg.StructuredMaxActivePerKey = concurrency
	cfg.StructuredQueueLimit = concurrency * 2
	configure(&cfg)
	app := New(cfg, WithCodexService(service), WithLogOutput(nil))

	responses := make([]structured.Response, concurrency)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			responses[index] = decodeStructuredSuccess(t, postStructured(t, app, structuredBody()))
		}(i)
	}
	<-started
	// Let the duplicates pile up behind the single in-flight call.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 concurrent duplicates to single-flight", got)
	}
	leaders := 0
	for index, response := range responses {
		if string(response.Data) != structuredTestOutput {
			t.Fatalf("response %d data = %s", index, response.Data)
		}
		if !response.IdempotentReplay {
			leaders++
		}
	}
	if leaders != 1 {
		t.Fatalf("non-replay responses = %d, want exactly 1", leaders)
	}
}

// AC7: the structured surface has its own metrics.
func TestStructuredInferenceRecordsMetrics(t *testing.T) {
	metrics := metricspkg.New(true)
	cfg := structuredTestConfig()
	cfg.GatewayBearerSecret = "gateway-secret"
	app := New(cfg, WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(nil), WithMetrics(metrics))

	decodeStructuredSuccess(t, postStructured(t, app, structuredBody(), map[string]string{"Authorization": "Bearer gateway-secret"}))

	badOutput := New(cfg, WithCodexService(staticStructuredService("not json")), WithLogOutput(nil), WithMetrics(metrics))
	decodeStructuredError(t,
		postStructured(t, badOutput, structuredBody(), map[string]string{"Authorization": "Bearer gateway-secret"}),
		http.StatusUnprocessableEntity, structured.CodeOutputValidation)

	body := scrapeServerMetrics(t, app, map[string]string{"Authorization": "Bearer gateway-secret"})
	for _, want := range []string{
		`codex_chat_api_structured_latency_seconds_count{model="gpt-5.6-sol",result="success"} 1`,
		`codex_chat_api_structured_latency_seconds_count{model="gpt-5.6-sol",result="output_validation_failed"} 1`,
		`codex_chat_api_structured_tokens_total{kind="total",model="gpt-5.6-sol"} 16`,
		`codex_chat_api_structured_failures_total{code="output_validation_failed"} 1`,
		`codex_chat_api_structured_validation_total{result="valid"} 1`,
		`codex_chat_api_structured_validation_total{result="unparsable"} 1`,
		`codex_chat_api_structured_idempotency_total{result="miss"} 2`,
		`codex_chat_api_queue_wait_seconds_count{provider="structured",result="acquired"} 2`,
		`codex_chat_api_structured_inflight 0`,
	} {
		if !bytes.Contains([]byte(body), []byte(want)) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
	// Metrics must never leak the caller's bearer token or tenant.
	if bytes.Contains([]byte(body), []byte("gateway-secret")) {
		t.Fatal("metrics leaked the gateway secret")
	}
}

// AC8, AC13: with the feature off the route does not exist at all.
// Issue 124 AC4: reusing an idempotency_key with materially different
// inference parameters is a deterministic 409 with no upstream call — never a
// replay of a response produced by another model, and never a second bill.
func TestStructuredInferenceConflictsOnChangedParameters(t *testing.T) {
	for name, mutate := range map[string]func(*structuredRequestBody){
		"model":             func(b *structuredRequestBody) { b.Model = "gpt-5.6-terra" },
		"reasoning effort":  func(b *structuredRequestBody) { b.ReasoningEffort = "high" },
		"verbosity":         func(b *structuredRequestBody) { b.Verbosity = "high" },
		"max output tokens": func(b *structuredRequestBody) { b.MaxOutputTokens = 64 },
		"schema body": func(b *structuredRequestBody) {
			b.Schema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["title","score"],"properties":{"title":{"type":"string"},"score":{"type":"number"}}}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var calls int32
			service := fakeCodexService{
				complete: func(context.Context, codex.Request) (codex.Completion, error) {
					atomic.AddInt32(&calls, 1)
					return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel, ID: "resp-original"}, nil
				},
			}
			cfg := structuredTestConfig()
			cfg.StructuredModels = []string{openai.DefaultModel, "gpt-5.6-terra"}
			app := New(cfg, WithCodexService(service), WithLogOutput(nil))

			decodeStructuredSuccess(t, postStructured(t, app, structuredBody()))

			conflicting := structuredBody()
			mutate(&conflicting)
			decodeStructuredError(t, postStructured(t, app, conflicting), http.StatusConflict, structured.CodeIdempotencyConflict)
			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Fatalf("upstream calls = %d, want 1 (a conflict must make no upstream call)", got)
			}

			// The original key still replays: a conflict rejects the divergent
			// request, it does not poison the binding.
			replayed := decodeStructuredSuccess(t, postStructured(t, app, structuredBody()))
			if !replayed.IdempotentReplay || replayed.UpstreamResponseID != "resp-original" {
				t.Fatalf("identical retry after a conflict = %#v, want the stored replay", replayed)
			}
			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Fatalf("upstream calls = %d, want 1", got)
			}
		})
	}
}

// The binding is durable, not just process-local: pod B must reject a divergent
// reuse of a key pod A bound, which is the case that would otherwise hand back
// a response produced by another model.
func TestStructuredInferenceConflictsAcrossReplicas(t *testing.T) {
	dir := t.TempDir()
	cfg := structuredTestConfig()
	cfg.StructuredIdempotencyBackend = config.IdempotencyBackendFile
	cfg.StructuredIdempotencyDir = dir
	cfg.StructuredModels = []string{openai.DefaultModel, "gpt-5.6-terra"}

	var callsA, callsB int32
	appA := New(cfg, WithCodexService(fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			atomic.AddInt32(&callsA, 1)
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel, ID: "resp-pod-a"}, nil
		},
	}), WithLogOutput(nil))
	appB := New(cfg, WithCodexService(fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			atomic.AddInt32(&callsB, 1)
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel, ID: "resp-pod-b"}, nil
		},
	}), WithLogOutput(nil))

	decodeStructuredSuccess(t, postStructured(t, appA, structuredBody()))

	divergent := structuredBody()
	divergent.Model = "gpt-5.6-terra"
	decodeStructuredError(t, postStructured(t, appB, divergent), http.StatusConflict, structured.CodeIdempotencyConflict)
	if got := atomic.LoadInt32(&callsB); got != 0 {
		t.Fatalf("pod B upstream calls = %d, want 0", got)
	}

	// An identical retry on pod B still replays pod A's stored response.
	replayed := decodeStructuredSuccess(t, postStructured(t, appB, structuredBody()))
	if !replayed.IdempotentReplay || replayed.UpstreamResponseID != "resp-pod-a" {
		t.Fatalf("pod B identical retry = %#v, want pod A's stored response", replayed)
	}
	if got := atomic.LoadInt32(&callsA); got != 1 {
		t.Fatalf("pod A upstream calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&callsB); got != 0 {
		t.Fatalf("pod B upstream calls = %d, want 0", got)
	}
}

// A re-serialized but semantically identical schema is a retry, not a conflict:
// key order and whitespace are not part of what the caller asked for.
func TestStructuredInferenceReplaysReformattedSchemas(t *testing.T) {
	var calls int32
	service := fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			atomic.AddInt32(&calls, 1)
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel, ID: "resp-original"}, nil
		},
	}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(nil))

	decodeStructuredSuccess(t, postStructured(t, app, structuredBody()))

	reformatted := structuredBody()
	reformatted.Schema = json.RawMessage("{\n  \"properties\": {\n    \"score\": {\"type\": \"integer\"},\n    \"title\": {\"type\": \"string\"}\n  },\n  \"required\": [\"title\", \"score\"],\n  \"additionalProperties\": false,\n  \"type\": \"object\"\n}")
	response := decodeStructuredSuccess(t, postStructured(t, app, reformatted))
	if !response.IdempotentReplay {
		t.Fatal("a reformatted but identical schema was treated as a conflict")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

// Issue 124 AC1: an enabled multi-replica gateway whose shared directory is
// unusable must fail startup rather than serve with a process-local store.
func TestPreflightStructuredIdempotencyFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based preflight failures are not observable as root")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	cfg := structuredTestConfig()
	cfg.StructuredIdempotencyBackend = config.IdempotencyBackendFile
	cfg.StructuredIdempotencyDir = filepath.Join(parent, "unmounted", "idempotency")
	cfg.StructuredReplicas = 3

	err := PreflightStructuredIdempotency(cfg)
	if err == nil {
		t.Fatal("PreflightStructuredIdempotency() error = nil, want a startup failure")
	}
	if !strings.Contains(err.Error(), cfg.StructuredIdempotencyDir) || !strings.Contains(err.Error(), "STRUCTURED_IDEMPOTENCY_DIR") {
		t.Fatalf("error = %q, want it to name the dir and the setting", err)
	}

	// A usable directory passes, and so does every configuration that does not
	// depend on the shared volume.
	usable := cfg
	usable.StructuredIdempotencyDir = t.TempDir()
	if err := PreflightStructuredIdempotency(usable); err != nil {
		t.Fatalf("PreflightStructuredIdempotency() on a usable dir error = %v", err)
	}
	dark := cfg
	dark.StructuredEnabled = false
	if err := PreflightStructuredIdempotency(dark); err != nil {
		t.Fatalf("PreflightStructuredIdempotency() with structured off error = %v", err)
	}
	if err := PreflightStructuredIdempotency(structuredTestConfig()); err != nil {
		t.Fatalf("PreflightStructuredIdempotency() on the memory backend error = %v", err)
	}
}

// Issue 124 AC2: backend=file is claimed only after the preflight succeeds. An
// unusable directory says backend=memory and why, so an operator never reads a
// durability guarantee the process does not have.
func TestStructuredIdempotencyBackendLogClaimsFileOnlyAfterPreflight(t *testing.T) {
	usableDir := t.TempDir()
	usable := structuredTestConfig()
	usable.StructuredIdempotencyBackend = config.IdempotencyBackendFile
	usable.StructuredIdempotencyDir = usableDir

	var usableLogs bytes.Buffer
	New(usable, WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(&usableLogs))
	if !strings.Contains(usableLogs.String(), "structured_idempotency_backend backend=file") {
		t.Fatalf("startup log = %q, want backend=file for a usable dir", usableLogs.String())
	}

	if os.Geteuid() == 0 {
		t.Skip("permission-based preflight failures are not observable as root")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	broken := usable
	broken.StructuredIdempotencyDir = filepath.Join(parent, "unmounted", "idempotency")
	var brokenLogs bytes.Buffer
	New(broken, WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(&brokenLogs))

	line := brokenLogs.String()
	if strings.Contains(line, "backend=file") {
		t.Fatalf("startup log = %q, want no backend=file claim for an unusable dir", line)
	}
	if !strings.Contains(line, "structured_idempotency_backend backend=memory reason=") {
		t.Fatalf("startup log = %q, want backend=memory with a reason", line)
	}
	if !strings.Contains(line, "STRUCTURED_IDEMPOTENCY_DIR") {
		t.Fatalf("startup log = %q, want the actionable setting name", line)
	}
}

func TestStructuredInferenceDisabledByDefault(t *testing.T) {
	cfg := config.Defaults()
	if cfg.StructuredEnabled {
		t.Fatal("structured inference is enabled by default")
	}
	app := New(cfg, WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(nil))

	resp := postStructured(t, app, structuredBody())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while the feature is off", resp.StatusCode)
	}
	// The 404 must be the pre-existing OpenAI-shaped not-found body.
	var body openai.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode 404: %v", err)
	}
	if body.Error.Type != "invalid_request_error" || body.Error.Message != "not found" {
		t.Fatalf("404 body = %#v, want the unchanged not-found shape", body.Error)
	}
}

// AC8, AC11: existing endpoints behave identically with the feature off and on.
func TestExistingEndpointsAreUnchangedByStructuredInference(t *testing.T) {
	service := fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			return codex.Completion{Text: "hello", Model: openai.DefaultModel, ID: "resp-chat"}, nil
		},
	}

	off := config.Defaults()
	off.DegenerateTurnRetryEnabled = false
	on := off
	on.StructuredEnabled = true

	snapshot := func(cfg config.Config) map[string]string {
		app := New(cfg, WithCodexService(service), WithLogOutput(nil), fixedServerOptions())
		out := map[string]string{}

		for _, path := range []string{"/health", "/health/live", "/health/ready", "/v1/models"} {
			req, err := http.NewRequest(http.MethodGet, path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer secret-access-token")
			resp, err := app.Test(req, 2000)
			if err != nil {
				t.Fatal(err)
			}
			payload := readAllString(t, resp)
			out[path] = strconv.Itoa(resp.StatusCode) + " " + payload
		}

		resp := doJSON(t, app, mustJSON(t, openai.ChatCompletionRequest{
			Model:    openai.DefaultModel,
			Messages: []openai.ChatMessage{{Role: "user", Content: openai.TextContent("hi")}},
		}))
		out["/v1/chat/completions"] = strconv.Itoa(resp.StatusCode) + " " + readAllString(t, resp)

		// An unauthenticated /v1 request keeps the OpenAI error shape.
		unauth, err := http.NewRequest(http.MethodGet, "/v1/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		unauthResp, err := app.Test(unauth, 2000)
		if err != nil {
			t.Fatal(err)
		}
		out["/v1/models unauthenticated"] = strconv.Itoa(unauthResp.StatusCode) + " " + readAllString(t, unauthResp)
		return out
	}

	before := snapshot(off)
	after := snapshot(on)
	for path, want := range before {
		if after[path] != want {
			t.Fatalf("%s changed when structured inference was enabled:\n off: %s\n  on: %s", path, want, after[path])
		}
	}
}

func readAllString(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

func errorStructuredService(err error) fakeCodexService {
	return fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			return codex.Completion{}, err
		},
	}
}
