package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/open-agent-api/internal/auth"
	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/config"
	"github.com/teslashibe/open-agent-api/internal/structured"
)

// structuredLiveRequiredEnv turns a missing credential from a skip into a
// failure. A QA gate sets it so "the live test never ran" can never be mistaken
// for "the live test passed".
const structuredLiveRequiredEnv = "STRUCTURED_LIVE_REQUIRED"

// structuredLiveBody is the copy-paste request from website/docs/api.md,
// verbatim. Keeping them identical is the point: if the documented body stops
// working against the real upstream, this test fails (AC3 and AC5 together).
const structuredLiveBody = `{
    "request_id": "req-1",
    "idempotency_key": "idem-1",
    "operation": "report_summary",
    "model": "gpt-5.6-sol",
    "input": "Revenue rose 12% in Q3, driven by enterprise renewals.",
    "schema_version": "v1",
    "deadline_ms": 60000,
    "schema": {
      "type": "object",
      "additionalProperties": false,
      "required": ["headline", "growth_pct"],
      "properties": {
        "headline": {"type": "string"},
        "growth_pct": {"type": "number"}
      }
    }
  }`

// repoRoot resolves the checkout root from this file's own path. The Codex
// client eagerly reads codex_profile.json and codex_scaffold.json, and the
// config defaults for both are repo-relative while this test runs in
// internal/server.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot resolve the repo root")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

// liveCodexClient builds a client pointed at the real Codex upstream, or
// skips (or fails, under STRUCTURED_LIVE_REQUIRED=1) when there is no usable
// credential to run with.
func liveCodexClient(t *testing.T) *codex.Client {
	t.Helper()
	required := os.Getenv(structuredLiveRequiredEnv) == "1"
	unavailable := func(format string, args ...any) {
		t.Helper()
		if required {
			t.Fatalf("%s=1 but the live upstream is unusable: "+format, append([]any{structuredLiveRequiredEnv}, args...)...)
		}
		t.Skipf("live structured upstream unavailable: "+format+" (set %s=1 to make this a failure)", append(args, structuredLiveRequiredEnv)...)
	}

	defaults := config.Defaults()
	authPath := defaults.AuthPath
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		authPath = filepath.Join(home, "auth.json")
	}
	if path := strings.TrimSpace(os.Getenv("CODEX_AUTH_PATH")); path != "" {
		authPath = path
	}
	credentials, err := auth.Load(authPath)
	if err != nil {
		unavailable("no Codex credentials at %s (%v); run `codex login`", authPath, err)
	}
	if credentials.AccessToken == "" {
		unavailable("credentials at %s carry no access token; run `codex login`", authPath)
	}

	root := repoRoot(t)
	client, err := codex.NewClient(codex.ClientConfig{
		AuthPath:     authPath,
		CodexHome:    filepath.Dir(authPath),
		ProfilePath:  filepath.Join(root, defaults.CodexProfilePath),
		ScaffoldPath: filepath.Join(root, defaults.CodexScaffoldPath),
		WebsocketURL: config.DefaultCodexWebsocketURL,
		Timeout:      2 * time.Minute,
	})
	if err != nil {
		unavailable("building the Codex client failed: %v", err)
	}
	return client
}

// Issue 126 AC3: the gateway must be proven against the real Codex upstream,
// not only against fakes. With credentials present this test never skips, so a
// QA gate that runs it cannot pass without observing one successful structured
// response: HTTP 200, data that satisfies the request schema, a real upstream
// response id, and real token usage.
//
// Gate command:
//
//	STRUCTURED_LIVE_REQUIRED=1 go test -count=1 ./internal/server -run TestStructuredInferenceLiveUpstream -v
func TestStructuredInferenceLiveUpstream(t *testing.T) {
	client := liveCodexClient(t)

	var logs bytes.Buffer
	cfg := config.Defaults()
	cfg.StructuredEnabled = true
	cfg.MetricsEnabled = true
	cfg.StructuredMaxDeadline = 2 * time.Minute
	app := New(cfg, WithCodexService(client), WithLogOutput(&logs))

	req, err := http.NewRequest(http.MethodPost, StructuredPath, strings.NewReader(structuredLiveBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer local-open-agent-api")
	resp, err := app.Test(req, 150_000)
	if err != nil {
		t.Fatalf("live structured request failed before a response: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var failure structured.ErrorResponse
		raw, _ := json.Marshal(&failure)
		if decodeErr := json.NewDecoder(resp.Body).Decode(&failure); decodeErr == nil {
			raw, _ = json.Marshal(&failure)
		}
		hint := "the gateway or the upstream call failed"
		if failure.Error.Code == structured.CodeOutputValidation {
			hint = "the gateway worked and the model did not comply with the strict json_schema — a model/prompt problem, not a gateway bug"
		}
		t.Fatalf("live structured status = %d, want 200: %s (%s)", resp.StatusCode, raw, hint)
	}

	var body structured.Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode live success: %v", err)
	}

	// The response is only a pass if it satisfies the schema that was sent.
	var request struct {
		Schema json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal([]byte(structuredLiveBody), &request); err != nil {
		t.Fatal(err)
	}
	schema, schemaErr := structured.CompileSchema(request.Schema)
	if schemaErr != nil {
		t.Fatalf("the documented schema does not compile: %v", schemaErr)
	}
	if validationErr := structured.Validate(schema, body.Data); validationErr != nil {
		t.Fatalf("live data %s violates the request schema: %v", body.Data, validationErr.Details)
	}

	if body.IdempotentReplay {
		t.Fatal("live response reported a replay; this run proves nothing about the upstream")
	}
	if body.UpstreamResponseID == "" {
		t.Fatalf("upstream_response_id is empty: %#v", body)
	}
	if body.Usage.TotalTokens <= 0 {
		t.Fatalf("usage = %#v, want real upstream token counts", body.Usage)
	}
	if body.ContractVersion != structured.ContractVersion {
		t.Fatalf("contract_version = %q, want %q", body.ContractVersion, structured.ContractVersion)
	}

	logged := logs.String()
	if !strings.Contains(logged, "structured_success") {
		t.Fatalf("logs missing structured_success:\n%s", logged)
	}
	// AC4 holds on the success path too: a live run must not put the caller's
	// input, the schema, the model's output, or credentials into the log.
	forbidden := []string{
		"Revenue rose 12%",
		"growth_pct",
		"Bearer ",
		"local-open-agent-api",
		string(body.Data),
	}
	for _, secret := range forbidden {
		if strings.Contains(logged, secret) {
			t.Fatalf("logs leaked %q:\n%s", secret, logged)
		}
	}
}
