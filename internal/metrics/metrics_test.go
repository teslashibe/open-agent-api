package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsExposeStableBoundedSurface(t *testing.T) {
	m := New(true)
	m.ObserveRequest("codex", "success")
	m.ObserveRateLimit("codex", "quota")
	m.ObservePoolSelection("work-a", "rotated")
	m.ObservePoolCooldown("work-a", "quota")
	m.ObservePoolCooldownSkip("work-a", "quota")
	m.ObserveQueueWait("codex", "acquired", 125*time.Millisecond)
	m.ObserveQueueWait("gemini", "bypassed", 0)
	m.IncActiveStreams("codex")
	m.DecActiveStreams("codex")
	m.ObserveChatDuration("codex", "success", 150*time.Millisecond)
	m.ObserveChatUsage("codex", 10, 5, 15)
	m.ObserveFastTierRequest("codex", "priority", "success")

	body := scrape(t, m)
	for _, want := range []string{
		`codex_chat_api_requests_total{provider="codex",result="success"} 1`,
		`codex_chat_api_rate_limit_responses_total{failure_class="quota",provider="codex"} 1`,
		`codex_chat_api_pool_selections_total{client_label="work-a",result="rotated"} 1`,
		`codex_chat_api_pool_cooldowns_total{client_label="work-a",failure_class="quota"} 1`,
		`codex_chat_api_pool_cooldown_skips_total{client_label="work-a",failure_class="quota"} 1`,
		`codex_chat_api_queue_wait_seconds_count{provider="codex",result="acquired"} 1`,
		`codex_chat_api_queue_wait_seconds_count{provider="gemini",result="bypassed"} 1`,
		`codex_chat_api_active_streams{provider="codex"} 0`,
		`codex_chat_api_request_duration_seconds_count{provider="codex",result="success"} 1`,
		`codex_chat_api_tokens_total{kind="prompt",provider="codex"} 10`,
		`codex_chat_api_tokens_total{kind="completion",provider="codex"} 5`,
		`codex_chat_api_tokens_total{kind="total",provider="codex"} 15`,
		`codex_chat_api_fast_tier_requests_total{provider="codex",result="success",tier="priority"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestStructuredMetricsExposeBoundedSurface(t *testing.T) {
	m := New(true)
	m.AllowStructuredModels([]string{"gpt-5.6-sol"})
	m.ObserveStructuredLatency("gpt-5.6-sol", "success", 250*time.Millisecond)
	m.ObserveStructuredLatency("gpt-5.6-sol", "timeout", 30*time.Second)
	m.ObserveStructuredUsage("gpt-5.6-sol", 11, 22, 33)
	m.ObserveStructuredFailure("output_validation_failed")
	m.ObserveStructuredValidation("valid")
	m.ObserveStructuredValidation("unparsable")
	m.ObserveStructuredIdempotency("local_hit")
	m.ObserveStructuredIdempotency("store_hit")
	m.ObserveStructuredIdempotency("miss")
	m.ObserveStructuredIdempotency("backend_error")
	m.ObserveQueueWait("structured", "full", time.Second)
	m.IncStructuredInflight()
	m.IncStructuredInflight()
	m.DecStructuredInflight()

	body := scrape(t, m)
	for _, want := range []string{
		`codex_chat_api_structured_latency_seconds_count{model="gpt-5.6-sol",result="success"} 1`,
		`codex_chat_api_structured_latency_seconds_count{model="gpt-5.6-sol",result="timeout"} 1`,
		`codex_chat_api_structured_tokens_total{kind="prompt",model="gpt-5.6-sol"} 11`,
		`codex_chat_api_structured_tokens_total{kind="completion",model="gpt-5.6-sol"} 22`,
		`codex_chat_api_structured_tokens_total{kind="total",model="gpt-5.6-sol"} 33`,
		`codex_chat_api_structured_failures_total{code="output_validation_failed"} 1`,
		`codex_chat_api_structured_validation_total{result="valid"} 1`,
		`codex_chat_api_structured_validation_total{result="unparsable"} 1`,
		`codex_chat_api_structured_idempotency_total{result="local_hit"} 1`,
		`codex_chat_api_structured_idempotency_total{result="store_hit"} 1`,
		`codex_chat_api_structured_idempotency_total{result="miss"} 1`,
		`codex_chat_api_structured_idempotency_total{result="backend_error"} 1`,
		`codex_chat_api_queue_wait_seconds_count{provider="structured",result="full"} 1`,
		`codex_chat_api_structured_inflight 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestStructuredMetricsBoundModelAndCodeCardinality(t *testing.T) {
	m := New(true)
	m.AllowStructuredModels([]string{"gpt-5.6-sol"})
	m.ObserveStructuredLatency("attacker-supplied-model", "success", time.Second)
	m.ObserveStructuredUsage("attacker-supplied-model", 1, 1, 2)
	m.ObserveStructuredFailure("made_up_code")
	m.ObserveStructuredValidation("made_up_result")
	m.ObserveStructuredIdempotency("made_up_outcome")
	m.ObserveStructuredLatency("gpt-5.6-sol", "made_up_code", time.Second)

	body := scrape(t, m)
	if strings.Contains(body, "attacker-supplied-model") || strings.Contains(body, "made_up") {
		t.Fatalf("structured metrics leaked unbounded labels:\n%s", body)
	}
	for _, want := range []string{
		`codex_chat_api_structured_latency_seconds_count{model="other",result="success"} 1`,
		`codex_chat_api_structured_latency_seconds_count{model="gpt-5.6-sol",result="unknown"} 1`,
		`codex_chat_api_structured_failures_total{code="unknown"} 1`,
		// Issue 124 AC6: an unrecognized validation label is "unknown", not a
		// counted schema failure.
		`codex_chat_api_structured_validation_total{result="unknown"} 1`,
		`codex_chat_api_structured_idempotency_total{result="unknown"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestDisabledStructuredMetricsAreNoop(t *testing.T) {
	m := New(false)
	m.AllowStructuredModels([]string{"gpt-5.6-sol"})
	m.ObserveStructuredLatency("gpt-5.6-sol", "success", time.Second)
	m.ObserveStructuredUsage("gpt-5.6-sol", 1, 1, 2)
	m.ObserveStructuredFailure("timeout")
	m.ObserveStructuredValidation("valid")
	m.ObserveStructuredIdempotency("store_hit")
	m.IncStructuredInflight()
	m.DecStructuredInflight()
}

// Issue 124 AC6: a typo or a label added later must land on "unknown". Falling
// back to "invalid" made a reporting gap indistinguishable from real model
// non-compliance — the one signal operators judge schema health by.
func TestStructuredValidationUnknownLabelsDoNotForgeSchemaFailures(t *testing.T) {
	m := New(true)
	for _, label := range []string{
		"vaild",       // typo of "valid"
		"invald",      // typo of "invalid"
		"unparsible",  // typo of "unparsable"
		"truncated",   // a label a later change might add
		"",            // an unset result
		"INVALID",     // wrong case
		" unparsable", // untrimmed
	} {
		m.ObserveStructuredValidation(label)
	}

	body := scrape(t, m)
	if !strings.Contains(body, `codex_chat_api_structured_validation_total{result="unknown"} 7`) {
		t.Fatalf("unknown validation labels were not folded onto result=\"unknown\":\n%s", body)
	}
	// Crucially, none of them inflated a real outcome.
	for _, forbidden := range []string{
		`codex_chat_api_structured_validation_total{result="invalid"}`,
		`codex_chat_api_structured_validation_total{result="valid"}`,
		`codex_chat_api_structured_validation_total{result="unparsable"}`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("an unknown label was counted as %s:\n%s", forbidden, body)
		}
	}

	// The three real labels still count themselves, unchanged.
	for _, label := range []string{"valid", "invalid", "unparsable"} {
		m.ObserveStructuredValidation(label)
	}
	body = scrape(t, m)
	for _, want := range []string{
		`codex_chat_api_structured_validation_total{result="valid"} 1`,
		`codex_chat_api_structured_validation_total{result="invalid"} 1`,
		`codex_chat_api_structured_validation_total{result="unparsable"} 1`,
		`codex_chat_api_structured_validation_total{result="unknown"} 7`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

// The conflict outcome and its contract code are part of the closed label sets,
// so the 409 path is observable rather than folded into "unknown".
func TestStructuredConflictLabelsAreCounted(t *testing.T) {
	m := New(true)
	m.AllowStructuredModels([]string{"gpt-5.6-sol"})
	m.ObserveStructuredIdempotency("conflict")
	m.ObserveStructuredFailure("idempotency_conflict")
	m.ObserveStructuredLatency("gpt-5.6-sol", "idempotency_conflict", time.Second)

	body := scrape(t, m)
	for _, want := range []string{
		`codex_chat_api_structured_idempotency_total{result="conflict"} 1`,
		`codex_chat_api_structured_failures_total{code="idempotency_conflict"} 1`,
		`codex_chat_api_structured_latency_seconds_count{model="gpt-5.6-sol",result="idempotency_conflict"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestMetricsNormalizeUntrustedLabels(t *testing.T) {
	m := New(true)
	secrets := []string{
		"raw-tenant@example.test",
		"Bearer secret-access-token",
		"full-model-prompt-hash-0123456789abcdef",
	}
	m.ObserveRequest(secrets[0], secrets[1])
	m.ObserveRateLimit(secrets[0], secrets[2])
	m.ObservePoolSelection(secrets[1], secrets[2])
	m.ObserveQueueWait(secrets[0], secrets[2], time.Second)

	body := scrape(t, m)
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Fatalf("metrics leaked untrusted label %q:\n%s", secret, body)
		}
	}
	for _, want := range []string{`provider="unknown"`, `client_label="invalid"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing normalized label %q:\n%s", want, body)
		}
	}
}

func TestDisabledMetricsAreNoopAndNotFound(t *testing.T) {
	m := New(false)
	m.ObserveRequest("codex", "success")

	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 404 {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 {
		t.Fatalf("scrape status = %d, want 200", recorder.Code)
	}
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatalf("read scrape: %v", err)
	}
	return string(body)
}
