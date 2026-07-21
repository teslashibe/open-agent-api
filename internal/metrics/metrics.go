// Package metrics owns the gateway's bounded Prometheus metric surface.
package metrics

import (
	"net/http"
	"regexp"
	"strings"
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

	m.registry.MustRegister(
		m.requests,
		m.rateLimits,
		m.poolSelections,
		m.poolCooldowns,
		m.poolCooldownSkips,
		m.queueWait,
		m.activeStreams,
	)
	return m
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

func normalizeProvider(value string) string {
	return allow(value, "unknown", "codex", "gemini", "claude")
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
