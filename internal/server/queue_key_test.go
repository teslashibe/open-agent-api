package server

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestCursorQueueKeyUsesMetadataIdentifier(t *testing.T) {
	key := resolveQueueKeyForTest(t, "cursor", `{
		"metadata":{"conversation_id":"chat-secret-123"},
		"messages":[{"role":"user","content":"do not log this"}]
	}`, nil)

	if key.Mode != "cursor:metadata" {
		t.Fatalf("mode = %q, want cursor:metadata", key.Mode)
	}
	if key.Hash == "" || key.Hash == "none" {
		t.Fatalf("hash = %q, want populated hash", key.Hash)
	}
	for _, leaked := range []string{"chat-secret-123", "do not log this"} {
		if strings.Contains(key.Mode, leaked) || strings.Contains(key.Hash, leaked) {
			t.Fatalf("key diagnostics leaked %q: mode=%q hash=%q", leaked, key.Mode, key.Hash)
		}
	}
}

func TestCursorQueueKeyConversationFingerprintDistinguishesConversations(t *testing.T) {
	first := resolveQueueKeyForTest(t, "cursor", cursorFingerprintBody("call_alpha", "first secret prompt"), nil)
	second := resolveQueueKeyForTest(t, "cursor", cursorFingerprintBody("call_beta", "second secret prompt"), nil)

	if first.Mode != "cursor:conversation_fingerprint" || second.Mode != "cursor:conversation_fingerprint" {
		t.Fatalf("modes = %q,%q want cursor:conversation_fingerprint", first.Mode, second.Mode)
	}
	if first.Hash == second.Hash {
		t.Fatalf("hashes matched for distinct conversations: %s", first.Hash)
	}
	for _, leaked := range []string{"first secret prompt", "second secret prompt"} {
		if strings.Contains(first.Hash, leaked) || strings.Contains(second.Hash, leaked) {
			t.Fatalf("fingerprint hash leaked content %q", leaked)
		}
	}
}

func TestCursorQueueKeyConversationFingerprintStableAcrossTurns(t *testing.T) {
	first := resolveQueueKeyForTest(t, "cursor", cursorFingerprintBody("call_shared", "first turn secret"), nil)
	repeated := resolveQueueKeyForTest(t, "cursor", `{
		"messages":[
			{"role":"user","content":"first turn secret"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_shared","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"secret one\"}"}}]},
			{"role":"tool","tool_call_id":"call_shared","content":"secret tool result"},
			{"role":"user","content":"second turn secret"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_later","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"secret.txt\"}"}}]},
			{"role":"tool","tool_call_id":"call_later","content":"later secret tool result"}
		]
	}`, nil)

	if first.Mode != "cursor:conversation_fingerprint" || repeated.Mode != "cursor:conversation_fingerprint" {
		t.Fatalf("modes = %q,%q want cursor:conversation_fingerprint", first.Mode, repeated.Mode)
	}
	if first.Hash != repeated.Hash {
		t.Fatalf("hashes differed for repeated conversation: %s != %s", first.Hash, repeated.Hash)
	}
}

func TestCursorQueueKeyFallsBackToForwardedFor(t *testing.T) {
	key := resolveQueueKeyForTest(t, "cursor", `{"messages":[]}`, map[string]string{"X-Forwarded-For": "203.0.113.10"})

	if key.Mode != "cursor:x-forwarded-for" {
		t.Fatalf("mode = %q, want cursor:x-forwarded-for", key.Mode)
	}
	if key.Hash == "" || key.Hash == "none" {
		t.Fatalf("hash = %q, want populated hash", key.Hash)
	}
	if strings.Contains(key.Mode, "203.0.113.10") || strings.Contains(key.Hash, "203.0.113.10") {
		t.Fatalf("key diagnostics leaked forwarded IP: mode=%q hash=%q", key.Mode, key.Hash)
	}
}

func TestCursorQueueKeyFallsBackToRemoteIP(t *testing.T) {
	key := resolveQueueKeyForTest(t, "cursor", `{"messages":[]}`, nil)

	if key.Mode != "cursor:remote_ip" {
		t.Fatalf("mode = %q, want cursor:remote_ip", key.Mode)
	}
	if key.Hash == "" || key.Hash == "none" {
		t.Fatalf("hash = %q, want populated hash", key.Hash)
	}
}

func TestCursorQueueKeyConversationFingerprintIgnoresLaterMessageIDs(t *testing.T) {
	first := resolveQueueKeyForTest(t, "cursor", `{
		"messages":[
			{"role":"user","content":"stable opening prompt"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_one","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_one","content":"result"}
		]
	}`, nil)
	withIDs := resolveQueueKeyForTest(t, "cursor", `{
		"messages":[
			{"id":"msg-user-1","role":"user","content":"stable opening prompt"},
			{"id":"msg-assistant-1","role":"assistant","content":null,"tool_calls":[{"id":"call_one","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"id":"msg-tool-1","role":"tool","tool_call_id":"call_one","content":"result"},
			{"id":"msg-user-2","role":"user","content":"follow up"},
			{"id":"msg-assistant-2","role":"assistant","content":null,"tool_calls":[{"id":"call_two","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"id":"msg-tool-2","role":"tool","tool_call_id":"call_two","content":"later result"}
		]
	}`, nil)

	if first.Mode != "cursor:conversation_fingerprint" || withIDs.Mode != "cursor:conversation_fingerprint" {
		t.Fatalf("modes = %q,%q want cursor:conversation_fingerprint", first.Mode, withIDs.Mode)
	}
	if first.Hash != withIDs.Hash {
		t.Fatalf("hashes differed after message ids appeared: %s != %s", first.Hash, withIDs.Hash)
	}
}

func TestCursorQueueKeyFallsBackToStableHeaderBeforeFingerprint(t *testing.T) {
	key := resolveQueueKeyForTest(t, "cursor", cursorFingerprintBody("call_header", "secret prompt"), map[string]string{
		"X-Cursor-Session-Id": "cursor-session-123",
	})

	if key.Mode != "cursor:header:x-cursor-session-id" {
		t.Fatalf("mode = %q, want cursor:header:x-cursor-session-id", key.Mode)
	}
}

func TestTenantHeaderOverridesQueueKeyModes(t *testing.T) {
	for _, mode := range []string{"cursor", "global", "auth_hash"} {
		t.Run(mode, func(t *testing.T) {
			key := resolveQueueKeyWithTenantForTest(t, mode, "X-Smore-Tenant-ID", cursorFingerprintBody("call_one", "secret prompt"), map[string]string{
				"X-Smore-Tenant-ID": "tenant-alpha",
				"Authorization":     "Bearer shared-secret",
			})

			if key.Mode != "tenant" {
				t.Fatalf("mode = %q, want tenant", key.Mode)
			}
			if key.Value != "tenant:tenant-alpha" {
				t.Fatalf("value = %q, want tenant:tenant-alpha", key.Value)
			}
		})
	}
}

func TestTenantHeaderDistinguishesTenants(t *testing.T) {
	first := resolveQueueKeyWithTenantForTest(t, "cursor", "X-Smore-Tenant-ID", `{"messages":[]}`, map[string]string{"X-Smore-Tenant-ID": "tenant-alpha"})
	second := resolveQueueKeyWithTenantForTest(t, "cursor", "X-Smore-Tenant-ID", `{"messages":[]}`, map[string]string{"X-Smore-Tenant-ID": "tenant-beta"})

	if first.Hash == second.Hash {
		t.Fatalf("hashes matched for distinct tenants: %s", first.Hash)
	}
}

func TestTenantHeaderAbsentKeepsExistingBehavior(t *testing.T) {
	withTenantConfigured := resolveQueueKeyWithTenantForTest(t, "cursor", "X-Smore-Tenant-ID", cursorFingerprintBody("call_one", "secret prompt"), nil)
	withoutTenantConfigured := resolveQueueKeyForTest(t, "cursor", cursorFingerprintBody("call_one", "secret prompt"), nil)

	if withTenantConfigured.Mode != withoutTenantConfigured.Mode {
		t.Fatalf("modes differ without tenant header: %q != %q", withTenantConfigured.Mode, withoutTenantConfigured.Mode)
	}
	if withTenantConfigured.Hash != withoutTenantConfigured.Hash {
		t.Fatalf("hashes differ without tenant header: %s != %s", withTenantConfigured.Hash, withoutTenantConfigured.Hash)
	}
}

func resolveQueueKeyForTest(t *testing.T, mode string, body string, headers map[string]string) agentQueueKey {
	t.Helper()
	return resolveQueueKeyWithTenantForTest(t, mode, "", body, headers)
}

func resolveQueueKeyWithTenantForTest(t *testing.T, mode string, tenantHeader string, body string, headers map[string]string) agentQueueKey {
	t.Helper()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	var key agentQueueKey
	app.Post("/", func(c *fiber.Ctx) error {
		key = resolveAgentQueueKey(mode, tenantHeader, c, c.Body())
		return c.SendStatus(fiber.StatusNoContent)
	})

	req, err := http.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	return key
}

func cursorFingerprintBody(toolCallID string, prompt string) string {
	return `{
		"messages":[
			{"role":"user","content":"` + prompt + `"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"` + toolCallID + `","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"secret\"}"}}]},
			{"role":"tool","tool_call_id":"` + toolCallID + `","content":"secret tool result"}
		]
	}`
}
