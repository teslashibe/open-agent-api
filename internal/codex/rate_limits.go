package codex

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type rateLimitWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes *float64 `json:"window_minutes"`
	ResetAt       *float64 `json:"reset_at"`
}

type rateLimitDetails struct {
	Primary   *rateLimitWindow `json:"primary"`
	Secondary *rateLimitWindow `json:"secondary"`
}

type rateLimitEvent struct {
	Type       string            `json:"type"`
	RateLimits *rateLimitDetails `json:"rate_limits"`
}

func (c *Client) observeRateLimitEvent(raw []byte) {
	if !c.metrics.Enabled() {
		return
	}
	var event rateLimitEvent
	if json.Unmarshal(raw, &event) != nil || event.Type != "codex.rate_limits" {
		return
	}
	if event.RateLimits == nil {
		return
	}
	c.observeRateLimitWindow("event", "primary", event.RateLimits.Primary)
	c.observeRateLimitWindow("event", "secondary", event.RateLimits.Secondary)
}

func (c *Client) observeRateLimitWindow(source, limitType string, value *rateLimitWindow) {
	if value == nil {
		return
	}
	for _, field := range []struct {
		name  string
		value *float64
	}{
		{"used_percent", value.UsedPercent},
		{"window_minutes", value.WindowMinutes},
		{"reset_at", value.ResetAt},
	} {
		if field.value != nil && *field.value >= 0 {
			c.metrics.SetCodexRateLimit(c.clientLabel, source, limitType, field.name, *field.value)
		}
	}
}

func (c *Client) observeRateLimitHeaders(resp *http.Response) {
	if resp == nil || !c.metrics.Enabled() {
		return
	}
	for _, limitType := range []string{"primary", "secondary"} {
		for field, suffix := range map[string]string{
			"used_percent":   "used-percent",
			"window_minutes": "window-minutes",
			"reset_at":       "reset-at",
		} {
			raw := strings.TrimSpace(resp.Header.Get("x-codex-" + limitType + "-" + suffix))
			value, err := strconv.ParseFloat(raw, 64)
			if err == nil && value >= 0 {
				c.metrics.SetCodexRateLimit(c.clientLabel, "header", limitType, field, value)
			}
		}
	}
}
