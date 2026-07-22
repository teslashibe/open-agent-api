package prometheus

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

type alertRuleFile struct {
	Groups []struct {
		Name  string `yaml:"name"`
		Rules []struct {
			Alert       string            `yaml:"alert"`
			Expr        string            `yaml:"expr"`
			For         string            `yaml:"for"`
			Labels      map[string]string `yaml:"labels"`
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"rules"`
	} `yaml:"groups"`
}

var metricNamePattern = regexp.MustCompile(`\b(?:codex_chat_api_[a-z_]+|probe_success)\b`)

func TestAlertRulesUseBoundedMetricsAndActionableAnnotations(t *testing.T) {
	raw := readFile(t, "codex-chat-api.rules.yml")
	var rules alertRuleFile
	if err := yaml.UnmarshalStrict(raw, &rules); err != nil {
		t.Fatalf("parse alert rules: %v", err)
	}

	allowedMetrics := map[string]bool{
		"codex_chat_api_rate_limit_responses_total": true,
		"codex_chat_api_pool_usable_clients":        true,
		"codex_chat_api_pool_client_usable":         true,
		"codex_chat_api_pool_cooldown_skips_total":  true,
		"codex_chat_api_queue_wait_seconds_count":   true,
		"codex_chat_api_queue_wait_seconds_bucket":  true,
		"codex_chat_api_requests_total":             true,
		"probe_success":                             true,
	}
	expectedAlerts := map[string]bool{
		"CodexChatAPICapacitySaturation":         false,
		"CodexChatAPINoUsableClients":            false,
		"CodexChatAPIAllClientsCoolingSuspected": false,
		"CodexChatAPIClientAuthFailure":          false,
		"CodexChatAPIQueueTimeouts":              false,
		"CodexChatAPIQueueWaitHigh":              false,
		"CodexChatAPIStreamFailures":             false,
		"CodexChatAPIReadinessLost":              false,
	}

	for _, group := range rules.Groups {
		for _, rule := range group.Rules {
			if _, ok := expectedAlerts[rule.Alert]; !ok {
				t.Errorf("unexpected or empty alert name %q", rule.Alert)
				continue
			}
			expectedAlerts[rule.Alert] = true
			if rule.For == "" {
				t.Errorf("%s has no sustained for duration", rule.Alert)
			}
			if severity := rule.Labels["severity"]; severity != "warning" && severity != "critical" {
				t.Errorf("%s severity = %q", rule.Alert, severity)
			}
			for _, annotation := range []string{"summary", "description", "runbook_url"} {
				if strings.TrimSpace(rule.Annotations[annotation]) == "" {
					t.Errorf("%s annotation %s is empty", rule.Alert, annotation)
				}
			}
			if got := rule.Annotations["runbook_url"]; !strings.HasPrefix(got, "https://teslashibe.github.io/codex-chat-api/docs/production-readiness#") {
				t.Errorf("%s runbook_url = %q", rule.Alert, got)
			}
			metrics := metricNamePattern.FindAllString(rule.Expr, -1)
			if len(metrics) == 0 {
				t.Errorf("%s expression has no recognized metric", rule.Alert)
			}
			for _, metric := range metrics {
				if !allowedMetrics[metric] {
					t.Errorf("%s uses unbounded or unknown metric %q", rule.Alert, metric)
				}
			}
		}
	}
	for alert, found := range expectedAlerts {
		if !found {
			t.Errorf("required alert %s is missing", alert)
		}
	}
}

func TestPromtoolFixtureCoversEveryAlert(t *testing.T) {
	rules := readFile(t, "codex-chat-api.rules.yml")
	fixture := readFile(t, "codex-chat-api.rules.test.yml")
	var parsedRules alertRuleFile
	if err := yaml.UnmarshalStrict(rules, &parsedRules); err != nil {
		t.Fatalf("parse alert rules: %v", err)
	}
	var parsedFixture any
	if err := yaml.UnmarshalStrict(fixture, &parsedFixture); err != nil {
		t.Fatalf("parse promtool fixture: %v", err)
	}
	fixtureText := string(fixture)
	for _, group := range parsedRules.Groups {
		for _, rule := range group.Rules {
			if !strings.Contains(fixtureText, "alertname: "+rule.Alert) {
				t.Errorf("promtool fixture does not cover %s", rule.Alert)
			}
		}
	}
}

func TestCoolingAlertPreservesScrapeTargetLabels(t *testing.T) {
	raw := readFile(t, "codex-chat-api.rules.yml")
	var rules alertRuleFile
	if err := yaml.UnmarshalStrict(raw, &rules); err != nil {
		t.Fatalf("parse alert rules: %v", err)
	}

	for _, group := range rules.Groups {
		for _, rule := range group.Rules {
			if rule.Alert != "CodexChatAPIAllClientsCoolingSuspected" {
				continue
			}
			for _, want := range []string{"count without (client_label)", "sum without (failure_class)"} {
				if !strings.Contains(rule.Expr, want) {
					t.Fatalf("cooling alert expression missing %q:\n%s", want, rule.Expr)
				}
			}
			for _, forbidden := range []string{"count(", "by (client_label)"} {
				if strings.Contains(rule.Expr, forbidden) {
					t.Fatalf("cooling alert expression contains label-dropping aggregation %q:\n%s", forbidden, rule.Expr)
				}
			}
			return
		}
	}
	t.Fatal("cooling alert is missing")
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return raw
}
