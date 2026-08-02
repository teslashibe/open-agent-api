package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	// Structured queue unit tests exercise its legacy standalone limits. Shared
	// Codex admission is covered by the agent queue integration tests.
	cfg.AgentQueueEnabled = false
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

// structuredBodyMap is structuredBody() as a raw map, for tests that need to
// send a key the contract no longer defines.
func structuredBodyMap() map[string]any {
	raw, err := json.Marshal(structuredBody())
	if err != nil {
		panic(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		panic(err)
	}
	return body
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
	app := New(cfg, WithCodexService(service), WithLogOutput(nil))

	request := structuredBody()
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

func TestStructuredInferenceClampsDeadline(t *testing.T) {
	var deadline time.Time
	service := fakeCodexService{
		complete: func(ctx context.Context, _ codex.Request) (codex.Completion, error) {
			deadline, _ = ctx.Deadline()
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel}, nil
		},
	}
	cfg := structuredTestConfig()
	cfg.StructuredMaxDeadline = 2 * time.Second
	app := New(cfg, WithCodexService(service), WithLogOutput(nil))

	request := structuredBody()
	request.DeadlineMS = 600000
	decodeStructuredSuccess(t, postStructured(t, app, request))

	if deadline.IsZero() || time.Until(deadline) > 3*time.Second {
		t.Fatalf("deadline = %s, want the configured 2s cap", time.Until(deadline))
	}
}

// gateway sends no cap upstream and the field is gone from the contract. A body
// that still carries max_output_tokens is ignored, not honoured and not
// rejected, so it cannot change the fingerprint either.
func TestStructuredInferenceSendsNoOutputCapAndIgnoresTheRemovedField(t *testing.T) {
	var seen codex.Request
	var calls int32
	service := fakeCodexService{
		complete: func(_ context.Context, req codex.Request) (codex.Completion, error) {
			seen = req
			atomic.AddInt32(&calls, 1)
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel, ID: "resp-original"}, nil
		},
	}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(nil))

	// The removed field is not on structuredRequestBody at all, so send it as a
	// raw body key: that is exactly the shape a pre-#126 client would send.
	withRemovedField := structuredBodyMap()
	withRemovedField["max_output_tokens"] = 256
	first := decodeStructuredSuccess(t, postStructured(t, app, withRemovedField))
	if first.IdempotentReplay {
		t.Fatal("first request reported a replay")
	}
	if raw, err := json.Marshal(seen); err != nil {
		t.Fatal(err)
	} else if bytes.Contains(raw, []byte("MaxOutputTokens")) || bytes.Contains(raw, []byte("max_output_tokens")) {
		t.Fatalf("codex request carried an output cap: %s", raw)
	}

	// An identical retry without the ignored field is still a replay: the
	// fingerprint never saw it.
	replayed := decodeStructuredSuccess(t, postStructured(t, app, structuredBody()))
	if !replayed.IdempotentReplay || replayed.UpstreamResponseID != "resp-original" {
		t.Fatalf("retry without the ignored field = %#v, want the stored replay", replayed)
	}
	// And a retry that varies only the ignored field is a replay too, never a
	// 409: an ignored field cannot wedge a key.
	varied := structuredBodyMap()
	varied["max_output_tokens"] = 999999
	if got := decodeStructuredSuccess(t, postStructured(t, app, varied)); !got.IdempotentReplay {
		t.Fatalf("retry varying only the ignored field = %#v, want a replay", got)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

// diagnose a generic client error, and that line must carry none of the input,
// schema, model output, or authorization material.
func TestStructuredInferenceLogsRedactionSafeProviderFailure(t *testing.T) {
	const (
		sentinelInput  = "SENTINEL_INPUT_zebra_quartz"
		sentinelSchema = "sentinel_schema_property_wombat"
		sentinelOutput = "SENTINEL_OUTPUT_marmoset"
		sentinelToken  = "sentinel-bearer-krypton"
	)
	service := fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			return codex.Completion{Text: `{"` + sentinelSchema + `":"` + sentinelOutput + `"}`},
				codex.NewError(codex.ErrorKindUpstream, http.StatusBadGateway, "codex stream failed", errors.New("upstream said "+sentinelOutput))
		},
	}
	var logs bytes.Buffer
	cfg := structuredTestConfig()
	cfg.GatewayBearerSecret = sentinelToken
	app := New(cfg, WithCodexService(service), WithLogOutput(&logs))

	request := structuredBody()
	request.Input = sentinelInput
	request.Schema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["` + sentinelSchema + `"],"properties":{"` + sentinelSchema + `":{"type":"string"}}}`)
	resp := postStructured(t, app, request, map[string]string{"Authorization": "Bearer " + sentinelToken})
	body := decodeStructuredError(t, resp, http.StatusBadGateway, structured.CodeUpstream)

	// The client body stays generic: no provider vocabulary, no detail.
	if body.Error.Message != "upstream error" || len(body.Error.Details) != 0 {
		t.Fatalf("client error body = %#v, want a generic upstream error", body.Error)
	}

	logged := logs.String()
	for _, want := range []string{
		"structured_upstream_error",
		"provider=codex",
		"provider_kind=upstream",
		"provider_status=502",
		`provider_message="codex stream failed"`,
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("logs missing %q:\n%s", want, logged)
		}
	}
	for _, forbidden := range []string{sentinelInput, sentinelSchema, sentinelOutput, sentinelToken, "Bearer "} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("logs leaked %q:\n%s", forbidden, logged)
		}
	}
}

// Issue 130 AC1: the shared sanitizer collapses every line-breaking and control
// rune, trims, and bounds length, so no caller-controlled identifier can carry
// a record separator into a log line.
func TestSanitizeLogFieldCollapsesInjection(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"newline":         {"req-1\nstructured_success request_id=forged", "req-1 structured_success request_id=forged"},
		"crlf":            {"req-1\r\nstructured_success", "req-1  structured_success"},
		"tab":             {"req-1\tkey=value", "req-1 key=value"},
		"vertical tab":    {"req-1\vkey=value", "req-1 key=value"},
		"form feed":       {"req-1\fkey=value", "req-1 key=value"},
		"next line":       {"req-1\u0085key=value", "req-1 key=value"},
		"line separator":  {"req-1\u2028key=value", "req-1 key=value"},
		"para separator":  {"req-1\u2029key=value", "req-1 key=value"},
		"nul":             {"req-1\x00key=value", "req-1 key=value"},
		"escape":          {"req-1\x1b[2Kkey=value", "req-1 [2Kkey=value"},
		"surrounding":     {"  \n req-1 \t ", "req-1"},
		"already clean":   {"req-1", "req-1"},
		"leading control": {"\nstructured_success request_id=forged", "structured_success request_id=forged"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := sanitizeLogField(tc.in); got != tc.want {
				t.Fatalf("sanitizeLogField(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.ContainsAny(sanitizeLogField(tc.in), "\n\r\t\v\f") {
				t.Fatalf("sanitizeLogField(%q) kept a line break", tc.in)
			}
		})
	}

	// The bound is exact: a maximum-length identifier is logged whole, and one
	// rune more is truncated with the same marker the provider message uses.
	atCap := strings.Repeat("a", maxLogFieldRunes)
	if got := sanitizeLogField(atCap); got != atCap {
		t.Fatalf("sanitizeLogField() truncated an at-cap value (%d runes)", len([]rune(got)))
	}
	overCap := sanitizeLogField(strings.Repeat("a", maxLogFieldRunes+1))
	if runes := []rune(overCap); len(runes) != maxLogFieldRunes+1 || runes[len(runes)-1] != '\u2026' {
		t.Fatalf("sanitizeLogField() over-cap = %q (%d runes)", overCap, len([]rune(overCap)))
	}
}

// forgedStructuredRecord is a complete, well-formed structured_success audit
// line. Embedded in a caller-controlled identifier behind a newline it would,
// unsanitized, appear in the log as an indistinguishable second record.
const forgedStructuredRecord = "structured_success request_id=forged operation=forged model=gpt-5.6-sol replay=false latency_ms=0"

// logRecordCounts summarizes a log buffer the way grep and log ingestion see
// it: one record per line, keyed by the leading token. A successful injection
// shows up here as an extra line, or an extra record of some type.
func logRecordCounts(logs string) map[string]int {
	counts := map[string]int{}
	for _, line := range strings.Split(logs, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		counts[strings.Fields(line)[0]]++
	}
	return counts
}

// Issue 130 AC2, AC3: a request_id or operation carrying an embedded newline
// plus a fully-formed audit record must not produce a second log record. The
// poisoned request has to emit exactly the records the same request emits with
// clean identifiers — no more lines, and no line of a type it did not earn.
func TestStructuredInferenceCannotForgeLogRecords(t *testing.T) {
	upstreamFailure := func() fakeCodexService {
		return errorStructuredService(codex.NewError(codex.ErrorKindUpstream, http.StatusBadGateway, "codex stream failed", nil))
	}
	for name, tc := range map[string]struct {
		service     func() fakeCodexService
		wantSuccess int
		run         func(*testing.T, *fiber.App, any)
	}{
		"success": {
			service:     func() fakeCodexService { return staticStructuredService(structuredTestOutput) },
			wantSuccess: 1,
			run: func(t *testing.T, app *fiber.App, body any) {
				decodeStructuredSuccess(t, postStructured(t, app, body))
			},
		},
		"upstream failure": {
			service:     upstreamFailure,
			wantSuccess: 0,
			run: func(t *testing.T, app *fiber.App, body any) {
				decodeStructuredError(t, postStructured(t, app, body), http.StatusBadGateway, structured.CodeUpstream)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			capture := func(requestID, operation string) string {
				var logs bytes.Buffer
				app := New(structuredTestConfig(), WithCodexService(tc.service()), WithLogOutput(&logs))
				body := structuredBody()
				body.RequestID = requestID
				body.Operation = operation
				tc.run(t, app, body)
				return logs.String()
			}

			clean := capture("req-1", "report_summary")
			poisoned := capture(
				"req-1\n"+forgedStructuredRecord,
				// Kept inside the contract's 128-char operation bound, so the
				// rejection under test is the sanitizer's and not Validate's.
				"report_summary\r\nstructured_admit request_id=forged operation=forged",
			)

			// The forged text may survive as inert data inside a field; what it
			// must never do is start a line, because a line is what grep and
			// log ingestion count as a record.
			for _, forged := range []string{
				forgedStructuredRecord,
				"structured_admit request_id=forged",
				"structured agent_queue_acquire request_id=forged",
			} {
				for _, line := range strings.Split(poisoned, "\n") {
					if strings.HasPrefix(line, forged) {
						t.Fatalf("caller forged the record %q:\n%s", forged, poisoned)
					}
				}
			}

			cleanCounts, poisonedCounts := logRecordCounts(clean), logRecordCounts(poisoned)
			if fmt.Sprint(cleanCounts) != fmt.Sprint(poisonedCounts) {
				t.Fatalf("record counts diverged:\n clean:    %v\n poisoned: %v\n%s", cleanCounts, poisonedCounts, poisoned)
			}
			if got := poisonedCounts["structured_success"]; got != tc.wantSuccess {
				t.Fatalf("structured_success records = %d, want %d:\n%s", got, tc.wantSuccess, poisoned)
			}
			if got := strings.Count(poisoned, "\n"); got != strings.Count(clean, "\n") {
				t.Fatalf("log lines = %d, want the clean run's %d:\n%s", got, strings.Count(clean, "\n"), poisoned)
			}
		})
	}
}

// The fix is log-side only: the response envelope still echoes the caller's
// request_id byte for byte, because JSON encoding already makes it inert there.
func TestStructuredInferenceEchoesRawRequestIDVerbatim(t *testing.T) {
	raw := "req-1\n" + forgedStructuredRecord
	var logs bytes.Buffer
	app := New(structuredTestConfig(), WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(&logs))

	body := structuredBody()
	body.RequestID = raw
	response := decodeStructuredSuccess(t, postStructured(t, app, body))
	if response.RequestID != raw {
		t.Fatalf("response request_id = %q, want the caller's value verbatim", response.RequestID)
	}
	if strings.Contains(logs.String(), "\n"+forgedStructuredRecord) {
		t.Fatalf("logs carried the raw value:\n%s", logs.String())
	}
}

// The same guarantee has to hold for the admission-control log lines, which are
// written by the shared agent queue rather than by the structured handler.
func TestStructuredInferenceQueueRecordsAreSingleLine(t *testing.T) {
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
	cfg.StructuredQueueTimeout = 20 * time.Millisecond
	var logs bytes.Buffer
	app := New(cfg, WithCodexService(service), WithLogOutput(&logs))

	first := make(chan *http.Response, 1)
	go func() {
		body := structuredBody()
		body.IdempotencyKey = "idem-slow"
		first <- postStructured(t, app, body)
	}()
	<-admitted

	// The timed-out request reaches the queue diagnostics with poisoned
	// identifiers after soft-waiting instead of failing immediately.
	shed := structuredBody()
	shed.IdempotencyKey = "idem-shed"
	shed.RequestID = "req-shed\nagent_queue_acquire request_id=forged key_mode=forged key_hash=forged"
	shed.Operation = "report_summary\n" + forgedStructuredRecord
	decodeStructuredError(t, postStructured(t, app, shed), http.StatusTooManyRequests, structured.CodeRateLimited)

	close(release)
	decodeStructuredSuccess(t, <-first)

	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.HasPrefix(line, "agent_queue_acquire") || strings.HasPrefix(line, forgedStructuredRecord) {
			t.Fatalf("caller forged a record through the queue:\n%s", logs.String())
		}
	}
	if !strings.Contains(logs.String(), "structured_shed") {
		t.Fatalf("expected a structured_shed record:\n%s", logs.String())
	}
}

// Issue 130 AC4: an over-cap schema is a clean 400 invalid_schema that is
// rejected before it is parsed and before any upstream call.
func TestStructuredInferenceRejectsOversizedSchema(t *testing.T) {
	var calls int32
	service := fakeCodexService{
		complete: func(context.Context, codex.Request) (codex.Completion, error) {
			atomic.AddInt32(&calls, 1)
			return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel}, nil
		},
	}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(nil))

	body := structuredBody()
	body.Schema = json.RawMessage(paddedStructuredSchema(t, structured.MaxSchemaBytes+1))
	decodeStructuredError(t, postStructured(t, app, body), http.StatusBadRequest, structured.CodeInvalidSchema)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0 (an over-cap schema must never reach upstream)", got)
	}

	// Exactly at the cap still works, and the 1 MiB input cap is unchanged by
	// the new bound.
	atCap := structuredBody()
	atCap.Schema = json.RawMessage(paddedStructuredSchema(t, structured.MaxSchemaBytes))
	atCap.IdempotencyKey = "idem-at-cap"
	decodeStructuredSuccess(t, postStructured(t, app, atCap))

	oversizedInput := structuredBody()
	oversizedInput.IdempotencyKey = "idem-big-input"
	oversizedInput.Input = strings.Repeat("x", (1<<20)+1)
	decodeStructuredError(t, postStructured(t, app, oversizedInput), http.StatusBadRequest, structured.CodeInvalidRequest)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (only the at-cap schema is served)", got)
	}
}

// paddedStructuredSchema builds a valid strict-subset schema whose encoding is
// exactly size bytes, padded through the (supported) "description" keyword.
func paddedStructuredSchema(t *testing.T, size int) string {
	t.Helper()
	const (
		prefix = `{"type":"object","additionalProperties":false,"required":["title","score"],"properties":{"title":{"type":"string"},"score":{"type":"integer"}},"description":"`
		suffix = `"}`
	)
	pad := size - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("size %d is smaller than the schema skeleton (%d bytes)", size, len(prefix)+len(suffix))
	}
	schema := prefix + strings.Repeat("d", pad) + suffix
	if len(schema) != size {
		t.Fatalf("padded schema = %d bytes, want %d", len(schema), size)
	}
	return schema
}

// A provider message is a fixed in-repo string today, but the log line must not
// depend on that: a multi-line or oversized message can neither forge a second
// record nor flood the log.
func TestSanitizeProviderMessage(t *testing.T) {
	if got := sanitizeProviderMessage(" upstream\nkind=forged\tstatus=200 "); got != "upstream kind=forged status=200" {
		t.Fatalf("sanitizeProviderMessage() = %q", got)
	}
	long := strings.Repeat("x", maxProviderMessageRunes+50)
	got := sanitizeProviderMessage(long)
	if runes := []rune(got); len(runes) != maxProviderMessageRunes+1 || runes[len(runes)-1] != '\u2026' {
		t.Fatalf("sanitizeProviderMessage() truncation = %q (%d runes)", got, len([]rune(got)))
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
	cfg.StructuredQueueTimeout = 20 * time.Millisecond
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

func TestStructuredInferenceIdempotencyCapacityReturnsRetryableUnavailable(t *testing.T) {
	oldCapacity := structuredIdempotencyEntries
	structuredIdempotencyEntries = 1
	t.Cleanup(func() { structuredIdempotencyEntries = oldCapacity })
	var calls int32
	service := fakeCodexService{complete: func(context.Context, codex.Request) (codex.Completion, error) {
		atomic.AddInt32(&calls, 1)
		return codex.Completion{Text: structuredTestOutput, Model: openai.DefaultModel, ID: "up-capacity"}, nil
	}}
	app := New(structuredTestConfig(), WithCodexService(service), WithLogOutput(nil))

	firstBody := structuredBody()
	firstBody.IdempotencyKey = "capacity-1"
	if resp := postStructured(t, app, firstBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d", resp.StatusCode)
	}
	secondBody := structuredBody()
	secondBody.IdempotencyKey = "capacity-2"
	second := postStructured(t, app, secondBody)
	decodeStructuredError(t, second, http.StatusServiceUnavailable, structured.CodeUnavailable)
	if second.Header.Get("Retry-After") == "" {
		t.Fatal("capacity response is missing Retry-After")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	replay := decodeStructuredSuccess(t, postStructured(t, app, firstBody))
	if !replay.IdempotentReplay {
		t.Fatal("existing live binding did not replay")
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

// B is both "the other pod" and "the same pod after a restart": it has its own
// empty in-memory store and its own upstream service, so a replay there can
// only have come from the durable record.

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

// startup, not left for an operator to infer from the absence of an error.
func TestStructuredEnabledMemoryBackendWarnsAtStartup(t *testing.T) {
	var logs bytes.Buffer
	New(structuredTestConfig(), WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(&logs))

	line := logs.String()
	if !strings.Contains(line, "structured_idempotency") {
		t.Fatalf("startup log = %q, want a structured_idempotency line", line)
	}
	for _, want := range []string{"process-local", "maxSurge", "Recreate"} {
		if !strings.Contains(line, want) {
			t.Fatalf("warning = %q, want it to mention %q", line, want)
		}
	}

	// A dark gateway is unchanged: no warning, because nothing can double-call.
	var dark bytes.Buffer
	cfg := structuredTestConfig()
	cfg.StructuredEnabled = false
	New(cfg, WithCodexService(staticStructuredService(structuredTestOutput)), WithLogOutput(&dark))
	if strings.Contains(dark.String(), "structured_idempotency") {
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

		// where single-flight also runs through a filesystem reservation.
		t.Run(fmt.Sprintf("duplicate-file-backend/%d", concurrency), func(t *testing.T) {
			assertStructuredDuplicatesSingleFlight(t, concurrency, func(cfg *config.Config) {
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
// inference parameters is a deterministic 409 with no upstream call — never a
// replay of a response produced by another model, and never a second bill.
func TestStructuredInferenceConflictsOnChangedParameters(t *testing.T) {
	for name, mutate := range map[string]func(*structuredRequestBody){
		"model":            func(b *structuredRequestBody) { b.Model = "gpt-5.6-terra" },
		"reasoning effort": func(b *structuredRequestBody) { b.ReasoningEffort = "high" },
		"verbosity":        func(b *structuredRequestBody) { b.Verbosity = "high" },
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

// unusable must fail startup rather than serve with a process-local store.

// unusable directory says backend=memory and why, so an operator never reads a
// durability guarantee the process does not have.

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
