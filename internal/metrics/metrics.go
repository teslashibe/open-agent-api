// Package metrics owns the gateway's bounded Prometheus metric surface.
package metrics

import (
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var clientLabelPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// Metrics contains process-local collectors. A disabled Metrics is a no-op.
// Each instance uses a private registry so tests and multiple Fiber apps in one
// process never collide in the global Prometheus registry.
type Metrics struct {
	enabled  bool
	registry *prometheus.Registry

	requests          *prometheus.CounterVec
	rateLimits        *prometheus.CounterVec
	poolSelections    *prometheus.CounterVec
	poolCooldowns     *prometheus.CounterVec
	poolCooldownSkips *prometheus.CounterVec
	queueWait         *prometheus.HistogramVec
	activeStreams     *prometheus.GaugeVec

	structuredLatency     *prometheus.HistogramVec
	structuredTokens      *prometheus.CounterVec
	structuredFailures    *prometheus.CounterVec
	structuredValidation  *prometheus.CounterVec
	structuredIdempotency *prometheus.CounterVec
	structuredInflight    prometheus.Gauge

	// structuredModels bounds the model label to the configured structured
	// allowlist. Anything else collapses to "other" so a caller cannot grow
	// cardinality by sending arbitrary model strings.
	structuredModelsMu sync.RWMutex
	structuredModels   map[string]bool
}

// New builds an isolated registry when enabled and a no-op recorder otherwise.
func New(enabled bool) *Metrics {
	m := &Metrics{enabled: enabled}
	if !enabled {
		return m
	}

	m.registry = prometheus.NewRegistry()
	m.requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "codex_chat_api_requests_total",
		Help: "Total chat completion requests by provider and final HTTP result.",
	}, []string{"provider", "result"})
	m.rateLimits = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "codex_chat_api_rate_limit_responses_total",
		Help: "Total chat completion responses with HTTP 429 by provider and failure class.",
	}, []string{"provider", "failure_class"})
	m.poolSelections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "codex_chat_api_pool_selections_total",
		Help: "Total Codex client pool selections by safe client label and result.",
	}, []string{"client_label", "result"})
	m.poolCooldowns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "codex_chat_api_pool_cooldowns_total",
		Help: "Total Codex client cooldown tickets by safe client label and failure class.",
	}, []string{"client_label", "failure_class"})
	m.poolCooldownSkips = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "codex_chat_api_pool_cooldown_skips_total",
		Help: "Total pool selection skips caused by an active cooldown.",
	}, []string{"client_label", "failure_class"})
	m.queueWait = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "codex_chat_api_queue_wait_seconds",
		Help:    "Agent queue wait time by provider and terminal admission result.",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	}, []string{"provider", "result"})
	m.activeStreams = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "codex_chat_api_active_streams",
		Help: "Current downstream chat completion streams by provider.",
	}, []string{"provider"})
	m.structuredLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "codex_chat_api_structured_latency_seconds",
		Help:    "Structured inference end-to-end latency by model and contract result.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 30, 60, 120, 300},
	}, []string{"model", "result"})
	m.structuredTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "codex_chat_api_structured_tokens_total",
		Help: "Structured inference token usage by model and token kind.",
	}, []string{"model", "kind"})
	m.structuredFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "codex_chat_api_structured_failures_total",
		Help: "Structured inference failures by machine-readable contract error code.",
	}, []string{"code"})
	m.structuredValidation = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "codex_chat_api_structured_validation_total",
		Help: "Structured inference schema validation outcomes: valid, invalid, unparsable, unknown.",
	}, []string{"result"})
	m.structuredIdempotency = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "codex_chat_api_structured_idempotency_total",
		Help: "Structured inference idempotency outcomes: local_hit, store_hit, miss, backend_error, conflict.",
	}, []string{"result"})
	m.structuredInflight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "codex_chat_api_structured_inflight",
		Help: "Structured inference requests currently admitted, for saturation alerting.",
	})

	m.registry.MustRegister(
		m.requests,
		m.rateLimits,
		m.poolSelections,
		m.poolCooldowns,
		m.poolCooldownSkips,
		m.queueWait,
		m.activeStreams,
		m.structuredLatency,
		m.structuredTokens,
		m.structuredFailures,
		m.structuredValidation,
		m.structuredIdempotency,
		m.structuredInflight,
	)
	return m
}

// AllowStructuredModels bounds the structured model label to the configured
// allowlist. Models outside it are recorded as "other".
func (m *Metrics) AllowStructuredModels(models []string) {
	if m == nil {
		return
	}
	allowed := make(map[string]bool, len(models))
	for _, model := range models {
		if model = strings.TrimSpace(model); model != "" {
			allowed[model] = true
		}
	}
	m.structuredModelsMu.Lock()
	m.structuredModels = allowed
	m.structuredModelsMu.Unlock()
}

// Enabled reports whether recording and the scrape endpoint are enabled.
func (m *Metrics) Enabled() bool {
	return m != nil && m.enabled
}

// Handler returns a non-panicking Prometheus text handler for the private registry.
func (m *Metrics) Handler() http.Handler {
	if !m.Enabled() || m.registry == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	})
}

func (m *Metrics) ObserveRequest(provider, result string) {
	if m.Enabled() {
		m.requests.WithLabelValues(normalizeProvider(provider), normalizeRequestResult(result)).Inc()
	}
}

func (m *Metrics) ObserveRateLimit(provider, failureClass string) {
	if m.Enabled() {
		m.rateLimits.WithLabelValues(normalizeProvider(provider), normalizeFailureClass(failureClass)).Inc()
	}
}

func (m *Metrics) ObservePoolSelection(clientLabel, result string) {
	if m.Enabled() {
		m.poolSelections.WithLabelValues(normalizeClientLabel(clientLabel), normalizePoolResult(result)).Inc()
	}
}

func (m *Metrics) ObservePoolCooldown(clientLabel, failureClass string) {
	if m.Enabled() {
		m.poolCooldowns.WithLabelValues(normalizeClientLabel(clientLabel), normalizeFailureClass(failureClass)).Inc()
	}
}

func (m *Metrics) ObservePoolCooldownSkip(clientLabel, failureClass string) {
	if m.Enabled() {
		m.poolCooldownSkips.WithLabelValues(normalizeClientLabel(clientLabel), normalizeFailureClass(failureClass)).Inc()
	}
}

func (m *Metrics) ObserveQueueWait(provider, result string, wait time.Duration) {
	if !m.Enabled() {
		return
	}
	if wait < 0 {
		wait = 0
	}
	m.queueWait.WithLabelValues(normalizeProvider(provider), normalizeQueueResult(result)).Observe(wait.Seconds())
}

func (m *Metrics) IncActiveStreams(provider string) {
	if m.Enabled() {
		m.activeStreams.WithLabelValues(normalizeProvider(provider)).Inc()
	}
}

func (m *Metrics) DecActiveStreams(provider string) {
	if m.Enabled() {
		m.activeStreams.WithLabelValues(normalizeProvider(provider)).Dec()
	}
}

// ObserveStructuredLatency records one completed structured inference. Result
// is "success" or the contract error code.
func (m *Metrics) ObserveStructuredLatency(model, result string, latency time.Duration) {
	if !m.Enabled() {
		return
	}
	if latency < 0 {
		latency = 0
	}
	m.structuredLatency.WithLabelValues(m.normalizeStructuredModel(model), normalizeStructuredResult(result)).Observe(latency.Seconds())
}

// ObserveStructuredUsage records upstream token usage for one structured call.
func (m *Metrics) ObserveStructuredUsage(model string, promptTokens, completionTokens, totalTokens int) {
	if !m.Enabled() {
		return
	}
	normalized := m.normalizeStructuredModel(model)
	for _, usage := range []struct {
		kind  string
		value int
	}{
		{"prompt", promptTokens},
		{"completion", completionTokens},
		{"total", totalTokens},
	} {
		if usage.value > 0 {
			m.structuredTokens.WithLabelValues(normalized, usage.kind).Add(float64(usage.value))
		}
	}
}

// ObserveStructuredFailure counts one structured failure by contract code.
func (m *Metrics) ObserveStructuredFailure(code string) {
	if m.Enabled() {
		m.structuredFailures.WithLabelValues(normalizeStructuredCode(code)).Inc()
	}
}

// ObserveStructuredValidation counts one schema validation outcome.
func (m *Metrics) ObserveStructuredValidation(result string) {
	if m.Enabled() {
		m.structuredValidation.WithLabelValues(normalizeStructuredValidation(result)).Inc()
	}
}

// ObserveStructuredIdempotency counts one idempotency outcome. The label set is
// closed — local_hit, store_hit, miss, backend_error, conflict — so a durable
// backend cannot grow cardinality with keys or paths.
func (m *Metrics) ObserveStructuredIdempotency(result string) {
	if m.Enabled() {
		m.structuredIdempotency.WithLabelValues(normalizeStructuredIdempotency(result)).Inc()
	}
}

// IncStructuredInflight and DecStructuredInflight track admitted structured
// requests so saturation is observable independently of chat traffic.
func (m *Metrics) IncStructuredInflight() {
	if m.Enabled() {
		m.structuredInflight.Inc()
	}
}

func (m *Metrics) DecStructuredInflight() {
	if m.Enabled() {
		m.structuredInflight.Dec()
	}
}

func (m *Metrics) normalizeStructuredModel(value string) string {
	value = strings.TrimSpace(value)
	m.structuredModelsMu.RLock()
	allowed := m.structuredModels
	m.structuredModelsMu.RUnlock()
	if !allowed[value] {
		return "other"
	}
	if !clientLabelPattern.MatchString(value) {
		return "other"
	}
	return value
}

func normalizeStructuredResult(value string) string {
	if value == "success" {
		return value
	}
	return normalizeStructuredCode(value)
}

func normalizeStructuredCode(value string) string {
	return allow(value, "unknown",
		"auth_error",
		"unsupported_model",
		"invalid_schema",
		"rate_limited",
		"timeout",
		"upstream_error",
		"output_validation_failed",
		"invalid_request",
		"unavailable",
		"idempotency_conflict",
	)
}

// normalizeStructuredValidation keeps the label set closed. The fallback is
// "unknown", not "invalid": a typo or a newly added label is a reporting gap,
// and counting it as a real schema failure would manufacture a false alarm on
// the one metric operators use to judge model compliance.
func normalizeStructuredValidation(value string) string {
	return allow(value, "unknown", "valid", "invalid", "unparsable")
}

func normalizeStructuredIdempotency(value string) string {
	return allow(value, "unknown", "local_hit", "store_hit", "miss", "backend_error", "conflict")
}

func normalizeProvider(value string) string {
	return allow(value, "unknown", "codex", "gemini", "claude", "structured")
}

func normalizeRequestResult(value string) string {
	return allow(value, "server_error", "success", "client_error", "rate_limited", "server_error")
}

func normalizePoolResult(value string) string {
	return allow(value, "normal", "normal", "rotated", "fallback", "pinned")
}

func normalizeQueueResult(value string) string {
	return allow(value, "error", "acquired", "bypassed", "full", "timeout", "canceled", "error")
}

func normalizeFailureClass(value string) string {
	return allow(value, "unknown", "quota", "rate_limit", "auth", "permanent", "transient", "unknown")
}

func normalizeClientLabel(value string) string {
	value = strings.TrimSpace(value)
	if !clientLabelPattern.MatchString(value) {
		return "invalid"
	}
	return value
}

func allow(value, fallback string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}
