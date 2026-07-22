package smore

import (
	"os"
	"strings"
	"testing"
)

func TestGrowthAuthCircuitPatchContract(t *testing.T) {
	patch, err := os.ReadFile("growth-auth-circuit.patch")
	if err != nil {
		t.Fatal(err)
	}
	text := string(patch)
	for _, want := range []string{
		`UpstreamAuthenticationFailedCode = "upstream_authentication_failed"`,
		`resp.Header.Get("Retry-After")`,
		`if retryAt, open := c.AuthCircuitOpen(); open`,
		`WithLLMAuthCircuit(openRouterClient)`,
		`TestChatCompletionUpstreamAuthCircuitSuppressesHTTPUntilRetry`,
		`TestUpstreamAuthCircuitBackoffGrowsAndCaps`,
		`TestGrowthCadenceStopsBeforeClaimWhileLLMAuthCircuitOpen`,
		`TestOutboxImproveStopsBeforeClaimWhileLLMAuthCircuitOpen`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Growth auth circuit patch missing %q", want)
		}
	}
	gate := strings.Index(text, `if retryAt, open := c.AuthCircuitOpen(); open`)
	payload := strings.Index(text, `payload := map[string]any{`)
	if gate < 0 || payload < 0 || gate > payload {
		t.Fatal("HTTP suppression gate must run before request construction")
	}
}

func TestReleaseGatesDeploymentOnSmoreCircuitSync(t *testing.T) {
	workflow, err := os.ReadFile("../../.github/workflows/docker.yml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(workflow)
	for _, want := range []string{
		"sync-smore-growth-auth-circuit:",
		"repository: teslashibe/smore",
		"git -C smore apply --check",
		"go test ./internal/draftingtool ./internal/growthleads ./internal/outbox ./cmd/server",
		"git push origin main",
		"needs: [build-and-push, sync-smore-growth-auth-circuit]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("release workflow missing %q", want)
		}
	}
}
