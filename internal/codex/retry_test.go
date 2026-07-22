package codex

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryHintFromHeader(t *testing.T) {
	now := time.Date(2026, 7, 21, 20, 0, 0, 0, time.UTC)
	retryAfter, resetAt := retryHintFromHeader("90", now)
	if retryAfter != 90*time.Second || !resetAt.IsZero() {
		t.Fatalf("seconds hint = %s %s", retryAfter, resetAt)
	}

	headerTime := now.Add(3 * time.Minute).Format(http.TimeFormat)
	retryAfter, resetAt = retryHintFromHeader(headerTime, now)
	if retryAfter != 0 || !resetAt.Equal(now.Add(3*time.Minute)) {
		t.Fatalf("date hint = %s %s", retryAfter, resetAt)
	}
}

func TestRetryHintFromJSON(t *testing.T) {
	reset := time.Date(2026, 7, 21, 21, 0, 0, 0, time.UTC)
	raw := []byte(`{"error":{"code":"usage_limit_reached","retry_after":12,"details":{"resets_at":"2026-07-21T21:00:00Z"}}}`)
	retryAfter, resetAt := retryHintFromJSON(raw)
	if retryAfter != 12*time.Second || !resetAt.Equal(reset) {
		t.Fatalf("JSON hint = %s %s", retryAfter, resetAt)
	}
}

func TestRetryDeadlinePrefersFutureReset(t *testing.T) {
	now := time.Date(2026, 7, 21, 20, 0, 0, 0, time.UTC)
	want := now.Add(time.Hour)
	err := &Error{RetryAfter: time.Minute, ResetAt: want}
	got, ok := retryDeadline(err, now)
	if !ok || !got.Equal(want) {
		t.Fatalf("retryDeadline() = %s, %t, want %s", got, ok, want)
	}
}

func TestRetryDeadlineUsesLaterRetryAfter(t *testing.T) {
	now := time.Date(2026, 7, 21, 20, 0, 0, 0, time.UTC)
	want := now.Add(2 * time.Hour)
	err := &Error{RetryAfter: 2 * time.Hour, ResetAt: now.Add(time.Minute)}
	got, ok := retryDeadline(err, now)
	if !ok || !got.Equal(want) {
		t.Fatalf("retryDeadline() = %s, %t, want %s", got, ok, want)
	}
}
