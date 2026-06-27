package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestCursorQueueKeyPrefersStableIdentifiers(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		headers  map[string]string
		wantMode string
	}{
		{
			name:     "header",
			body:     `{"metadata":{"thread_id":"metadata-thread"},"messages":[]}`,
			headers:  map[string]string{"X-Cursor-Session-Id": "cursor-session"},
			wantMode: "cursor:header:x-cursor-session-id",
		},
		{
			name:     "body",
			body:     `{"conversation_id":"conversation-123","metadata":{"thread_id":"metadata-thread"},"messages":[]}`,
			wantMode: "cursor:body:conversation_id",
		},
		{
			name:     "metadata",
			body:     `{"metadata":{"thread_id":"metadata-thread"},"messages":[]}`,
			wantMode: "cursor:metadata:thread_id",
		},
		{
			name:     "metadata candidate",
			body:     `{"metadata":{"stableChatId":"chat-123"},"messages":[]}`,
			wantMode: "cursor:metadata:stableChatId",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := resolveQueueKeyForTest(t, "cursor", tc.body, tc.headers)
			if key.Mode != tc.wantMode {
				t.Fatalf("Mode = %q, want %q", key.Mode, tc.wantMode)
			}
			if key.Hash == "" || key.Hash == "none" {
				t.Fatalf("Hash = %q, want non-empty hash", key.Hash)
			}
		})
	}
}

func TestCursorConversationFingerprintDistinguishesConversations(t *testing.T) {
	first := resolveQueueKeyForTest(t, "cursor", `{
		"messages":[
			{"role":"user","content":"secret one"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_alpha","type":"function","function":{"name":"lookup","arguments":"secret args"}}]},
			{"role":"tool","tool_call_id":"call_alpha","content":"secret output"}
		],
		"tools":[{"type":"function"}]
	}`, nil)
	second := resolveQueueKeyForTest(t, "cursor", `{
		"messages":[
			{"role":"user","content":"secret two"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_beta","type":"function","function":{"name":"lookup","arguments":"secret args"}}]},
			{"role":"tool","tool_call_id":"call_beta","content":"secret output"}
		],
		"tools":[{"type":"function"}]
	}`, nil)

	if first.Mode != "cursor:conversation_fingerprint" || second.Mode != "cursor:conversation_fingerprint" {
		t.Fatalf("modes = %q,%q, want conversation_fingerprint", first.Mode, second.Mode)
	}
	if first.Hash == second.Hash {
		t.Fatalf("hashes matched for distinct conversations: %s", first.Hash)
	}
}

func TestCursorConversationFingerprintIsStableAcrossRepeatedTurns(t *testing.T) {
	first := resolveQueueKeyForTest(t, "cursor", `{
		"messages":[
			{"role":"user","content":"secret prompt"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_same","type":"function","function":{"name":"read_file","arguments":"secret args"}}]},
			{"role":"tool","tool_call_id":"call_same","content":"secret output"}
		],
		"tools":[{"type":"function"}]
	}`, nil)
	repeated := resolveQueueKeyForTest(t, "cursor", `{
		"messages":[
			{"role":"user","content":"secret prompt"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_same","type":"function","function":{"name":"read_file","arguments":"secret args"}}]},
			{"role":"tool","tool_call_id":"call_same","content":"secret output"},
			{"role":"assistant","content":"done"},
			{"role":"user","content":"secret followup"}
		],
		"tools":[{"type":"function"}]
	}`, nil)

	if first.Mode != "cursor:conversation_fingerprint" || repeated.Mode != "cursor:conversation_fingerprint" {
		t.Fatalf("modes = %q,%q, want conversation_fingerprint", first.Mode, repeated.Mode)
	}
	if first.Hash != repeated.Hash {
		t.Fatalf("hashes differed for repeated conversation: %s != %s", first.Hash, repeated.Hash)
	}
}

func TestCursorQueueKeyFallsBackSafelyWhenIdentifiersAreMissing(t *testing.T) {
	headers := map[string]string{"X-Forwarded-For": "203.0.113.10"}
	first := resolveQueueKeyForTest(t, "cursor", `{"messages":[{"role":"user","content":"secret one"}],"tools":[{"type":"function"}]}`, headers)
	second := resolveQueueKeyForTest(t, "cursor", `{"messages":[{"role":"user","content":"secret two"}],"tools":[{"type":"function"}]}`, headers)

	if first.Mode != "cursor:fallback:x-forwarded-for" || second.Mode != "cursor:fallback:x-forwarded-for" {
		t.Fatalf("modes = %q,%q, want x-forwarded-for fallback", first.Mode, second.Mode)
	}
	if first.Hash != second.Hash {
		t.Fatalf("fallback hashes differed for same forwarded IP: %s != %s", first.Hash, second.Hash)
	}

	noHeader := resolveQueueKeyForTest(t, "cursor", `{"messages":[{"role":"user","content":"secret"}],"tools":[{"type":"function"}]}`, nil)
	if noHeader.Mode != "cursor:fallback:request_fingerprint" {
		t.Fatalf("Mode = %q, want request fingerprint fallback", noHeader.Mode)
	}
}

func resolveQueueKeyForTest(t *testing.T, mode string, body string, headers map[string]string) agentQueueKey {
	t.Helper()
	app := fiber.New()
	var key agentQueueKey
	app.Post("/", func(c *fiber.Ctx) error {
		key = resolveAgentQueueKey(mode, c, c.Body())
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("User-Agent", "Cursor/Test")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusNoContent)
	}
	return key
}
