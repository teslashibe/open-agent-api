package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/config"
	metricspkg "github.com/teslashibe/codex-chat-api/internal/metrics"
	"github.com/teslashibe/codex-chat-api/internal/openai"
	"github.com/teslashibe/codex-chat-api/internal/reportstudio"
)

func structuredTestConfig() config.Config {
	cfg := config.Defaults()
	cfg.GatewayBearerSecret = "structured-secret"
	cfg.StructuredInferenceModels = []string{"gpt-5.6-sol"}
	cfg.StructuredInferenceMaxActive = 4
	cfg.StructuredInferenceMaxQueue = 8
	cfg.StructuredInferenceRetryAfter = time.Second
	return cfg
}

func structuredTestRequest(id, key string, input any) reportstudio.Request {
	inputRaw, _ := json.Marshal(input)
	return reportstudio.Request{
		ContractVersion: reportstudio.ContractVersion,
		RequestID:       id,
		IdempotencyKey:  key,
		Model:           "gpt-5.6-sol",
		Input:           inputRaw,
		Schema: reportstudio.SchemaRequest{
			Name: "summary", Version: "summary.v1", Strict: true,
			Schema: json.RawMessage(`{
				"type":"object",
				"properties":{"title":{"type":"string"},"count":{"type":"integer"}},
				"required":["title","count"],
				"additionalProperties":false
			}`),
		},
		Reasoning:       reportstudio.Reasoning{Effort: "low"},
		Verbosity:       "low",
		MaxOutputTokens: 512,
		DeadlineMS:      5000,
	}
}

func postStructured(t *testing.T, app *fiber.App, req reportstudio.Request, token string, headers ...map[string]string) *http.Response {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, "/v1/report-studio/inference", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	for _, headerSet := range headers {
		for name, value := range headerSet {
			httpReq.Header.Set(name, value)
		}
	}
	resp, err := app.Test(httpReq, 7000)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	return resp
}

func decodeStructuredError(t *testing.T, resp *http.Response) reportstudio.ErrorResponse {
	t.Helper()
	defer resp.Body.Close()
	var body reportstudio.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestReportStudioSuccessExtractionAndReplay(t *testing.T) {
	var calls atomic.Int32
	service := fakeCodexService{complete: func(_ context.Context, req codex.Request) (codex.Completion, error) {
		calls.Add(1)
		if req.Faithful || req.Prewarm || len(req.Tools) != 0 || len(req.ToolChoice) != 0 {
			t.Fatalf("extraction mode leaked coding behavior: %#v", req)
		}
		if req.ResponseSchemaName != "summary" || len(req.ResponseSchema) == 0 || req.MaxOutputTokens != 512 {
			t.Fatalf("structured fields = %#v", req)
		}
		return codex.Completion{
			Text:  `{"title":"Revenue","count":42}`,
			Model: "gpt-5.6-sol-actual", ID: "resp-upstream",
			Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}, nil
	}}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(io.Discard), fixedServerOptions())

	req := structuredTestRequest("request-1", "idempotency-1", map[string]any{"source": "revenue"})
	firstResp := postStructured(t, app, req, "structured-secret")
	defer firstResp.Body.Close()
	if firstResp.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, body=%s", firstResp.StatusCode, readString(t, firstResp.Body))
	}
	var first reportstudio.Success
	if err := json.NewDecoder(firstResp.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	if first.ContractVersion != reportstudio.ContractVersion || first.Model != "gpt-5.6-sol-actual" ||
		first.UpstreamResponseID != "resp-upstream" || first.Usage.TotalTokens != 15 ||
		first.Identity.ResponseID == "" || first.Identity.SchemaChecksum == "" || first.Identity.Replayed {
		t.Fatalf("first response = %#v", first)
	}

	replayReq := req
	replayReq.RequestID = "request-2"
	replayResp := postStructured(t, app, replayReq, "structured-secret")
	defer replayResp.Body.Close()
	var replay reportstudio.Success
	if err := json.NewDecoder(replayResp.Body).Decode(&replay); err != nil {
		t.Fatal(err)
	}
	if replayResp.StatusCode != http.StatusOK || replay.RequestID != "request-2" || !replay.Identity.Replayed ||
		replay.Identity.ResponseID != first.Identity.ResponseID || calls.Load() != 1 {
		t.Fatalf("replay = %#v status=%d calls=%d", replay, replayResp.StatusCode, calls.Load())
	}
}

func TestReportStudioInvalidRequestDoesNotCallUpstream(t *testing.T) {
	var calls atomic.Int32
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		calls.Add(1)
		return codex.Completion{}, nil
	}}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(io.Discard))
	req := structuredTestRequest("request-invalid", "key-invalid", "input")
	req.MaxOutputTokens = 0
	resp := postStructured(t, app, req, "structured-secret")
	body := decodeStructuredError(t, resp)
	if resp.StatusCode != http.StatusBadRequest || body.Error.Code != "invalid_request" {
		t.Fatalf("invalid request = status %d body %#v", resp.StatusCode, body)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0", calls.Load())
	}
}

func TestReportStudioDeadlineIncludesSchemaPreprocessing(t *testing.T) {
	var calls atomic.Int32
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		calls.Add(1)
		return codex.Completion{}, nil
	}}
	slowCompiler := func(opts *options) {
		compile := opts.structuredCompile
		opts.structuredCompile = func(req reportstudio.SchemaRequest) (*reportstudio.Validator, error) {
			time.Sleep(20 * time.Millisecond)
			return compile(req)
		}
	}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(io.Discard), slowCompiler)
	req := structuredTestRequest("request-preprocess-timeout", "key-preprocess-timeout", "input")
	req.DeadlineMS = 5
	resp := postStructured(t, app, req, "structured-secret")
	body := decodeStructuredError(t, resp)
	if resp.StatusCode != http.StatusGatewayTimeout || body.Error.Code != "timeout" {
		t.Fatalf("preprocessing timeout = status %d body %#v", resp.StatusCode, body)
	}
	if calls.Load() != 0 {
		t.Fatalf("expired request made %d upstream calls", calls.Load())
	}
}

func TestReportStudioSameVersionSchemaChangeConflicts(t *testing.T) {
	var calls atomic.Int32
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		calls.Add(1)
		return codex.Completion{Text: `{"title":"ok","count":1}`, ID: "resp-schema"}, nil
	}}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(io.Discard))
	req := structuredTestRequest("request-schema-a", "key-schema", "input")
	first := postStructured(t, app, req, "structured-secret")
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d", first.StatusCode)
	}

	changed := req
	changed.RequestID = "request-schema-b"
	changed.Schema.Schema = json.RawMessage(`{
		"type":"object",
		"properties":{"title":{"type":"string"}},
		"required":["title"],
		"additionalProperties":false
	}`)
	conflict := postStructured(t, app, changed, "structured-secret")
	body := decodeStructuredError(t, conflict)
	if conflict.StatusCode != http.StatusConflict || body.Error.Code != "idempotency_conflict" {
		t.Fatalf("schema conflict = status %d body %#v", conflict.StatusCode, body)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
}

func TestReportStudioTenantHeaderCannotSelectCallerNamespace(t *testing.T) {
	var calls atomic.Int32
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		calls.Add(1)
		return codex.Completion{Text: `{"title":"ok","count":1}`, ID: "resp-caller"}, nil
	}}
	cfg := structuredTestConfig()
	app := New(cfg, WithCodexService(service), WithLogOutput(io.Discard))
	firstReq := structuredTestRequest("request-caller-a", "key-caller", "input-a")
	first := postStructured(t, app, firstReq, "structured-secret", map[string]string{
		cfg.GatewayTenantHeader: "caller-a",
	})
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d", first.StatusCode)
	}

	secondReq := structuredTestRequest("request-caller-b", "key-caller", "input-b")
	second := postStructured(t, app, secondReq, "structured-secret", map[string]string{
		cfg.GatewayTenantHeader: "caller-b",
	})
	body := decodeStructuredError(t, second)
	if second.StatusCode != http.StatusConflict || body.Error.Code != "idempotency_conflict" {
		t.Fatalf("caller scope = status %d body %#v", second.StatusCode, body)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
}

func TestReportStudioRejectsUnsupportedStrictSchemaBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		calls.Add(1)
		return codex.Completion{}, nil
	}}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(io.Discard))
	tests := []reportstudio.Request{
		structuredTestRequest("request-bad-name", "key-bad-name", "input"),
		structuredTestRequest("request-bad-keyword", "key-bad-keyword", "input"),
	}
	tests[0].Schema.Name = "bad schema name"
	tests[1].Schema.Schema = json.RawMessage(`{
		"type":"object",
		"properties":{"title":{"type":"string","minLength":1}},
		"required":["title"],
		"additionalProperties":false
	}`)
	for _, req := range tests {
		resp := postStructured(t, app, req, "structured-secret")
		body := decodeStructuredError(t, resp)
		if resp.StatusCode != http.StatusBadRequest || body.Error.Code != "invalid_schema" {
			t.Fatalf("invalid schema = status %d body %#v", resp.StatusCode, body)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid schemas made %d upstream calls", calls.Load())
	}
}

func TestReportStudioAuthUnsupportedSchemaAndConflictErrors(t *testing.T) {
	cfg := structuredTestConfig()
	app := New(cfg, WithCodexService(fakeCodexService{}), WithLogOutput(io.Discard), fixedServerOptions())
	req := structuredTestRequest("request-1", "key-1", map[string]any{"source": "x"})

	authResp := postStructured(t, app, req, "")
	authBody := decodeStructuredError(t, authResp)
	if authResp.StatusCode != http.StatusUnauthorized || authBody.Error.Code != "authentication_error" {
		t.Fatalf("auth = status %d body %#v", authResp.StatusCode, authBody)
	}

	unsupported := req
	unsupported.RequestID = "request-2"
	unsupported.IdempotencyKey = "key-2"
	unsupported.Model = "unknown-model"
	unsupportedResp := postStructured(t, app, unsupported, "structured-secret")
	unsupportedBody := decodeStructuredError(t, unsupportedResp)
	if unsupportedBody.Error.Code != "unsupported_model" {
		t.Fatalf("unsupported body = %#v", unsupportedBody)
	}

	invalidSchema := req
	invalidSchema.RequestID = "request-3"
	invalidSchema.IdempotencyKey = "key-3"
	invalidSchema.Schema.Strict = false
	invalidResp := postStructured(t, app, invalidSchema, "structured-secret")
	invalidBody := decodeStructuredError(t, invalidResp)
	if invalidBody.Error.Code != "invalid_schema" {
		t.Fatalf("invalid schema body = %#v", invalidBody)
	}

	successService := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		return codex.Completion{Text: `{"title":"ok","count":1}`, ID: "resp-success"}, nil
	}}
	app = New(cfg, WithCodexService(successService), WithLogOutput(io.Discard), fixedServerOptions())
	okResp := postStructured(t, app, req, "structured-secret")
	okResp.Body.Close()
	conflict := req
	conflict.Input = json.RawMessage(`{"source":"different"}`)
	conflictResp := postStructured(t, app, conflict, "structured-secret")
	conflictBody := decodeStructuredError(t, conflictResp)
	if conflictResp.StatusCode != http.StatusConflict || conflictBody.Error.Code != "idempotency_conflict" {
		t.Fatalf("conflict = status %d body %#v", conflictResp.StatusCode, conflictBody)
	}
}

func TestReportStudioServiceAndMalformedOutputErrors(t *testing.T) {
	tests := []struct {
		name       string
		complete   func(context.Context, codex.Request) (codex.Completion, error)
		wantStatus int
		wantCode   string
	}{
		{
			name: "rate limit",
			complete: func(context.Context, codex.Request) (codex.Completion, error) {
				return codex.Completion{}, codex.NewError(codex.ErrorKindUpstream, http.StatusTooManyRequests, "limited", nil)
			},
			wantStatus: http.StatusTooManyRequests, wantCode: "rate_limit",
		},
		{
			name: "upstream",
			complete: func(context.Context, codex.Request) (codex.Completion, error) {
				return codex.Completion{}, errors.New("transport secret detail")
			},
			wantStatus: http.StatusBadGateway, wantCode: "upstream_error",
		},
		{
			name: "upstream schema rejection",
			complete: func(context.Context, codex.Request) (codex.Completion, error) {
				return codex.Completion{}, codex.NewError(codex.ErrorKindClient, http.StatusBadRequest, "unsupported schema detail", nil)
			},
			wantStatus: http.StatusBadRequest, wantCode: "invalid_schema",
		},
		{
			name: "malformed json",
			complete: func(context.Context, codex.Request) (codex.Completion, error) {
				return codex.Completion{Text: "```json\n{}\n```", ID: "resp-malformed"}, nil
			},
			wantStatus: http.StatusBadGateway, wantCode: "validation_error",
		},
		{
			name: "schema mismatch",
			complete: func(context.Context, codex.Request) (codex.Completion, error) {
				return codex.Completion{Text: `{"title":"ok","count":"wrong"}`, ID: "resp-mismatch"}, nil
			},
			wantStatus: http.StatusBadGateway, wantCode: "validation_error",
		},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := New(structuredTestConfig(), WithCodexService(fakeCodexService{complete: tc.complete}), WithLogOutput(io.Discard), fixedServerOptions())
			req := structuredTestRequest(fmt.Sprintf("request-%d", i), fmt.Sprintf("key-%d", i), map[string]any{"source": tc.name})
			resp := postStructured(t, app, req, "structured-secret")
			body := decodeStructuredError(t, resp)
			if resp.StatusCode != tc.wantStatus || body.Error.Code != tc.wantCode {
				t.Fatalf("status=%d body=%#v", resp.StatusCode, body)
			}
			if tc.wantCode == "rate_limit" && resp.Header.Get("Retry-After") == "" {
				t.Fatal("rate limit missing Retry-After")
			}
		})
	}
}

func TestReportStudioMetricsRecordEndpointOutcomes(t *testing.T) {
	metrics := metricspkg.New(true)
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		return codex.Completion{
			Text: `{"title":"ok","count":1}`, ID: "resp-metrics",
			Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		}, nil
	}}
	cfg := structuredTestConfig()
	app := New(cfg, WithCodexService(service), WithMetrics(metrics), WithLogOutput(io.Discard))
	success := postStructured(t, app, structuredTestRequest("request-metrics", "key-metrics", "input"), "structured-secret")
	success.Body.Close()
	if success.StatusCode != http.StatusOK {
		t.Fatalf("success status = %d", success.StatusCode)
	}

	invalid := structuredTestRequest("request-metrics-invalid", "key-metrics-invalid", "input")
	invalid.Schema.Name = "invalid name"
	invalidResp := postStructured(t, app, invalid, "structured-secret")
	invalidResp.Body.Close()
	if invalidResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid status = %d", invalidResp.StatusCode)
	}

	body := scrapeServerMetrics(t, app, map[string]string{
		"Authorization": "Bearer structured-secret",
	})
	for _, want := range []string{
		`codex_chat_api_structured_inference_latency_seconds_count{provider="codex",result="success"} 1`,
		`codex_chat_api_structured_inference_tokens_total{kind="total",provider="codex"} 15`,
		`codex_chat_api_structured_inference_failures_total{code="invalid_schema"} 1`,
		`codex_chat_api_structured_inference_validation_total{result="success"} 1`,
		`codex_chat_api_structured_inference_saturation_total{result="admitted"} 1`,
		`codex_chat_api_structured_inference_active 0`,
		`codex_chat_api_structured_inference_queued 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestReportStudioTimeoutError(t *testing.T) {
	service := fakeCodexService{complete: func(ctx context.Context, _ codex.Request) (codex.Completion, error) {
		<-ctx.Done()
		return codex.Completion{}, ctx.Err()
	}}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(io.Discard))
	req := structuredTestRequest("request-timeout", "key-timeout", map[string]any{"source": "slow"})
	req.DeadlineMS = 5
	resp := postStructured(t, app, req, "structured-secret")
	body := decodeStructuredError(t, resp)
	if resp.StatusCode != http.StatusGatewayTimeout || body.Error.Code != "timeout" {
		t.Fatalf("timeout = status %d body %#v", resp.StatusCode, body)
	}
}

func TestReportStudioAdmissionOverload(t *testing.T) {
	cfg := structuredTestConfig()
	cfg.StructuredInferenceMaxActive = 1
	cfg.StructuredInferenceMaxQueue = 0
	started := make(chan struct{})
	release := make(chan struct{})
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return codex.Completion{Text: `{"title":"ok","count":1}`, ID: "resp-overload"}, nil
	}}
	app := New(cfg, WithCodexService(service), WithLogOutput(io.Discard))
	firstDone := make(chan *http.Response, 1)
	go func() {
		firstDone <- postStructured(t, app, structuredTestRequest("request-first", "key-first", "first"), "structured-secret")
	}()
	<-started
	overload := postStructured(t, app, structuredTestRequest("request-second", "key-second", "second"), "structured-secret")
	body := decodeStructuredError(t, overload)
	if overload.StatusCode != http.StatusTooManyRequests || body.Error.Code != "overloaded" ||
		overload.Header.Get("Retry-After") != "1" {
		t.Fatalf("overload = status %d body %#v retry=%q", overload.StatusCode, body, overload.Header.Get("Retry-After"))
	}
	close(release)
	resp := <-firstDone
	resp.Body.Close()
}

func TestReportStudioIdempotencyCapacityReturns503(t *testing.T) {
	cfg := structuredTestConfig()
	cfg.StructuredInferenceIdempotencyLimit = 1
	cfg.StructuredInferenceMaxActive = 2
	started := make(chan struct{})
	release := make(chan struct{})
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return codex.Completion{Text: `{"title":"ok","count":1}`, ID: "resp-capacity"}, nil
	}}
	app := New(cfg, WithCodexService(service), WithLogOutput(io.Discard))
	firstDone := make(chan *http.Response, 1)
	go func() {
		firstDone <- postStructured(t, app, structuredTestRequest("request-first", "key-first", "first"), "structured-secret")
	}()
	<-started
	overload := postStructured(t, app, structuredTestRequest("request-second", "key-second", "second"), "structured-secret")
	body := decodeStructuredError(t, overload)
	if overload.StatusCode != http.StatusServiceUnavailable || body.Error.Code != "overloaded" ||
		overload.Header.Get("Retry-After") != "1" {
		t.Fatalf("overload = status %d body %#v retry=%q", overload.StatusCode, body, overload.Header.Get("Retry-After"))
	}
	close(release)
	resp := <-firstDone
	resp.Body.Close()
}

func TestReportStudioConcurrencyLevels(t *testing.T) {
	for _, level := range []int{1, 2, 4, 8} {
		t.Run(fmt.Sprintf("level_%d", level), func(t *testing.T) {
			cfg := structuredTestConfig()
			cfg.StructuredInferenceMaxActive = level
			cfg.StructuredInferenceMaxQueue = level
			started := make(chan struct{}, level)
			release := make(chan struct{})
			var active atomic.Int32
			var maximum atomic.Int32
			service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
				current := active.Add(1)
				for {
					seen := maximum.Load()
					if current <= seen || maximum.CompareAndSwap(seen, current) {
						break
					}
				}
				started <- struct{}{}
				<-release
				active.Add(-1)
				return codex.Completion{Text: `{"title":"ok","count":1}`, ID: "resp-concurrent"}, nil
			}}
			app := New(cfg, WithCodexService(service), WithLogOutput(io.Discard))
			var wg sync.WaitGroup
			statuses := make(chan int, level)
			for i := range level {
				wg.Add(1)
				go func() {
					defer wg.Done()
					req := structuredTestRequest(fmt.Sprintf("request-%d", i), fmt.Sprintf("key-%d", i), map[string]int{"index": i})
					resp := postStructured(t, app, req, "structured-secret")
					statuses <- resp.StatusCode
					resp.Body.Close()
				}()
			}
			for range level {
				<-started
			}
			close(release)
			wg.Wait()
			close(statuses)
			for status := range statuses {
				if status != http.StatusOK {
					t.Fatalf("status = %d", status)
				}
			}
			if got := int(maximum.Load()); got != level {
				t.Fatalf("maximum concurrency = %d, want %d", got, level)
			}
		})
	}
}

func TestReportStudioRouteDoesNotChangeChatCompatibility(t *testing.T) {
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		return codex.Completion{Text: "legacy chat", ID: "legacy-id", Model: "gpt-5.6-sol"}, nil
	}}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(io.Discard), fixedServerOptions())
	resp := doJSON(t, app, `{"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"Authorization": "Bearer structured-secret"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy status = %d", resp.StatusCode)
	}
	var body openai.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.ID != "legacy-id" || openai.MessageText(body.Choices[0].Message.Content) != "legacy chat" {
		t.Fatalf("legacy response = %#v", body)
	}
}
