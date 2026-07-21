package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/gofiber/fiber/v2"
)

const (
	defaultAgentQueueKeyMode  = "global"
	defaultAgentQueueKeyValue = "global"
)

type agentQueueKey struct {
	Mode  string
	Value string
	Hash  string
}

func resolveAgentQueueKey(mode string, tenantHeader string, c *fiber.Ctx, rawBody []byte) agentQueueKey {
	// An explicit tenant header (smore free-tier traffic) always wins over the
	// configured key mode so fairness is per tenant, not per Cursor session.
	if name := strings.TrimSpace(tenantHeader); name != "" {
		if tenant := strings.TrimSpace(c.Get(name)); tenant != "" {
			return newAgentQueueKey("tenant", tenant)
		}
	}

	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = defaultAgentQueueKeyMode
	}

	switch {
	case mode == "global":
		return newAgentQueueKey(mode, defaultAgentQueueKeyValue)
	case mode == "auth_hash":
		auth := strings.TrimSpace(c.Get(fiber.HeaderAuthorization))
		if auth == "" {
			return newAgentQueueKey(mode, defaultAgentQueueKeyValue)
		}
		return newAgentQueueKey(mode, safeHash(auth))
	case mode == "cursor":
		return resolveCursorQueueKey(c, rawBody)
	case mode == "request_fingerprint":
		return newAgentQueueKey(mode, requestFingerprint(c))
	case strings.HasPrefix(mode, "header:"):
		name := strings.TrimSpace(strings.TrimPrefix(mode, "header:"))
		value := strings.TrimSpace(c.Get(name))
		if value == "" {
			return newAgentQueueKey(mode, defaultAgentQueueKeyValue)
		}
		return newAgentQueueKey(mode, value)
	case strings.HasPrefix(mode, "body:"):
		field := strings.TrimSpace(strings.TrimPrefix(mode, "body:"))
		value := scalarBodyField(rawBody, field)
		if value == "" {
			return newAgentQueueKey(mode, defaultAgentQueueKeyValue)
		}
		return newAgentQueueKey(mode, value)
	default:
		return newAgentQueueKey(defaultAgentQueueKeyMode, defaultAgentQueueKeyValue)
	}
}

func resolveCursorQueueKey(c *fiber.Ctx, rawBody []byte) agentQueueKey {
	var body map[string]json.RawMessage
	_ = json.Unmarshal(rawBody, &body)

	if value := cursorMetadataIdentifier(body); value != "" {
		return newAgentQueueKey("cursor:metadata", value)
	}
	for _, name := range []string{
		"x-cursor-session-id",
		"x-conversation-id",
		"x-conversation-thread-id",
		"x-session-id",
	} {
		if value := strings.TrimSpace(c.Get(name)); value != "" {
			return newAgentQueueKey("cursor:header:"+name, value)
		}
	}
	if value := cursorConversationFingerprint(body); value != "" {
		return newAgentQueueKey("cursor:conversation_fingerprint", value)
	}
	if value := forwardedForQueueKey(c); value != "" {
		return newAgentQueueKey("cursor:x-forwarded-for", value)
	}
	if ip := strings.TrimSpace(c.IP()); ip != "" {
		return newAgentQueueKey("cursor:remote_ip", ip)
	}
	return newAgentQueueKey("cursor:global", defaultAgentQueueKeyValue)
}

func forwardedForQueueKey(c *fiber.Ctx) string {
	raw := strings.TrimSpace(c.Get("X-Forwarded-For"))
	if raw == "" {
		return ""
	}
	if idx := strings.Index(raw, ","); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(raw)
}

func cursorMetadataIdentifier(body map[string]json.RawMessage) string {
	for _, field := range cursorStableIDFields() {
		if value := scalarRawString(body[field]); value != "" {
			return field + "=" + value
		}
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(body["metadata"], &metadata); err != nil {
		return ""
	}
	for _, field := range cursorStableIDFields() {
		if value := scalarRawString(metadata[field]); value != "" {
			return "metadata." + field + "=" + value
		}
	}
	return ""
}

func cursorStableIDFields() []string {
	return []string{
		"conversation_id",
		"chat_id",
		"thread_id",
		"session_id",
		"cursor_conversation_id",
		"cursor_chat_id",
		"cursor_thread_id",
	}
}

func cursorConversationFingerprint(body map[string]json.RawMessage) string {
	rawMessages, ok := body["messages"]
	if !ok {
		return ""
	}
	var messages []cursorFingerprintMessage
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return ""
	}

	for _, message := range messages {
		for _, toolCall := range message.ToolCalls {
			if id := strings.TrimSpace(toolCall.ID); id != "" {
				return "earliest_tool=" + id
			}
		}
		if id := strings.TrimSpace(message.ToolCallID); id != "" {
			return "earliest_tool_result=" + id
		}
	}
	for _, message := range messages {
		if strings.TrimSpace(message.Role) != "user" {
			continue
		}
		if content := cursorMessageContentFingerprint(message.Content); content != "" {
			return "first_user=" + content
		}
	}
	return ""
}

func cursorMessageContentFingerprint(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		text = strings.TrimSpace(text)
		if text != "" {
			return safeHash(text)
		}
	}
	return safeHash(string(content))
}

type cursorFingerprintMessage struct {
	Role       string                      `json:"role"`
	Content    json.RawMessage             `json:"content"`
	ToolCallID string                      `json:"tool_call_id"`
	ToolCalls  []cursorFingerprintToolCall `json:"tool_calls"`
}

type cursorFingerprintToolCall struct {
	ID       string                        `json:"id"`
	Type     string                        `json:"type"`
	Function cursorFingerprintToolFunction `json:"function"`
}

type cursorFingerprintToolFunction struct {
	Name string `json:"name"`
}

func newAgentQueueKey(mode string, value string) agentQueueKey {
	if value == "" {
		value = defaultAgentQueueKeyValue
	}
	return agentQueueKey{
		Mode:  mode,
		Value: mode + ":" + value,
		Hash:  safeHash(mode + ":" + value),
	}
}

func (key agentQueueKey) withDefaults() agentQueueKey {
	if key.Mode == "" {
		key.Mode = defaultAgentQueueKeyMode
	}
	if key.Value == "" {
		key.Value = key.Mode + ":" + defaultAgentQueueKeyValue
	}
	if key.Hash == "" {
		key.Hash = safeHash(key.Value)
	}
	return key
}

func requestFingerprint(c *fiber.Ctx) string {
	parts := []string{
		"auth=" + safeHash(c.Get(fiber.HeaderAuthorization)),
		"ua=" + safeHash(c.Get(fiber.HeaderUserAgent)),
		"ip=" + c.IP(),
	}
	return strings.Join(parts, "|")
}

func scalarBodyField(raw []byte, field string) string {
	if field == "" {
		return ""
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return ""
	}
	rawValue, ok := body[field]
	if !ok {
		return ""
	}
	return scalarRawString(rawValue)
}

func scalarRawString(raw json.RawMessage) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimSpace(string(raw))
	default:
		return ""
	}
}

func safeHash(value string) string {
	if value == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}
