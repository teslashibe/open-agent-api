package codex

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func withRetryHint(err error, retryAfter time.Duration, resetAt time.Time) error {
	if codexErr, ok := ErrorAs(err); ok {
		codexErr.RetryAfter = retryAfter
		codexErr.ResetAt = resetAt
	}
	return err
}

func retryDeadline(err error, now time.Time) (time.Time, bool) {
	codexErr, ok := ErrorAs(err)
	if !ok {
		return time.Time{}, false
	}
	deadline := time.Time{}
	if codexErr.RetryAfter > 0 {
		deadline = now.Add(codexErr.RetryAfter)
	}
	if codexErr.ResetAt.After(deadline) && codexErr.ResetAt.After(now) {
		deadline = codexErr.ResetAt
	}
	return deadline, !deadline.IsZero()
}

func retryHintFromHeader(value string, now time.Time) (time.Duration, time.Time) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, time.Time{}
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 && seconds <= int64(math.MaxInt64/time.Second) {
		return time.Duration(seconds) * time.Second, time.Time{}
	}
	if resetAt, err := http.ParseTime(value); err == nil && resetAt.After(now) {
		return 0, resetAt
	}
	return 0, time.Time{}
}

// retryHintFromJSON recognizes the reset fields used by Codex quota events
// without retaining or logging the upstream payload. Unknown fields are
// ignored. When several hints are present, the longest valid cooldown wins.
func retryHintFromJSON(raw []byte) (time.Duration, time.Time) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return 0, time.Time{}
	}
	var retryAfter time.Duration
	var resetAt time.Time
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ReplaceAll(strings.ToLower(key), "-", "_")
				switch normalized {
				case "retry_after", "retry_after_seconds":
					if duration := retryDurationValue(child); duration > retryAfter {
						retryAfter = duration
					}
				case "reset_at", "resets_at", "reset_time", "reset_timestamp":
					if candidate := resetTimeValue(child); candidate.After(resetAt) {
						resetAt = candidate
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(value)
	return retryAfter, resetAt
}

func retryDurationValue(value any) time.Duration {
	switch typed := value.(type) {
	case float64:
		if typed > 0 && typed <= float64(math.MaxInt64)/float64(time.Second) {
			return time.Duration(typed * float64(time.Second))
		}
	case string:
		trimmed := strings.TrimSpace(typed)
		if duration, err := time.ParseDuration(trimmed); err == nil && duration > 0 {
			return duration
		}
		if seconds, err := strconv.ParseFloat(trimmed, 64); err == nil && seconds > 0 {
			return time.Duration(seconds * float64(time.Second))
		}
	}
	return 0
}

func resetTimeValue(value any) time.Time {
	switch typed := value.(type) {
	case float64:
		seconds := int64(typed)
		if typed >= 1e12 {
			return time.UnixMilli(seconds)
		}
		if typed > 0 {
			return time.Unix(seconds, 0)
		}
	case string:
		trimmed := strings.TrimSpace(typed)
		if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return parsed
		}
		if parsed, err := http.ParseTime(trimmed); err == nil {
			return parsed
		}
		if number, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return resetTimeValue(number)
		}
	}
	return time.Time{}
}
