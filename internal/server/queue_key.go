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

func resolveAgentQueueKey(mode string, c *fiber.Ctx, rawBody []byte) agentQueueKey {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = defaultAgentQueueKeyMode
	}

	switch {
	case mode == "cursor":
		return resolveCursorQueueKey(c, rawBody)
	case mode == "global":
		return newAgentQueueKey(mode, defaultAgentQueueKeyValue)
	case mode == "auth_hash":
		auth := strings.TrimSpace(c.Get(fiber.HeaderAuthorization))
		if auth == "" {
			return newAgentQueueKey(mode, defaultAgentQueueKeyValue)
		}
		return newAgentQueueKey(mode, safeHash(auth))
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

var cursorStableIDFields = []string{
	"conversation_id",
	"thread_id",
	"chat_id",
	"session_id",
}

func resolveCursorQueueKey(c *fiber.Ctx, rawBody []byte) agentQueueKey {
	if value := strings.TrimSpace(c.Get("x-cursor-session-id")); value != "" {
		return newAgentQueueKey("cursor:header:x-cursor-session-id", value)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &body); err == nil {
		if field, value := firstScalarField(body, cursorStableIDFields); value != "" {
			return newAgentQueueKey("cursor:body:"+field, value)
		}
		if rawMetadata, ok := body["metadata"]; ok {
			if field, value := cursorMetadataID(rawMetadata); value != "" {
				return newAgentQueueKey("cursor:metadata:"+field, value)
			}
		}
		if fingerprint := cursorConversationFingerprint(body); fingerprint != "" {
			return newAgentQueueKey("cursor:conversation_fingerprint", fingerprint)
		}
	}

	if value := strings.TrimSpace(c.Get("x-forwarded-for")); value != "" {
		return newAgentQueueKey("cursor:fallback:x-forwarded-for", value)
	}
	if fingerprint := requestFingerprint(c); fingerprint != "" {
		return newAgentQueueKey("cursor:fallback:request_fingerprint", fingerprint)
	}
	return newAgentQueueKey("cursor:fallback:global", defaultAgentQueueKeyValue)
}

func firstScalarField(values map[string]json.RawMessage, fields []string) (string, string) {
	for _, field := range fields {
		if value := scalarRawJSON(values[field]); value != "" {
			return field, value
		}
	}
	return "", ""
}

func cursorMetadataID(raw json.RawMessage) (string, string) {
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return "", ""
	}
	if field, value := firstScalarField(metadata, cursorStableIDFields); value != "" {
		return field, value
	}
	for _, field := range sortedMapKeys(metadata) {
		normalized := strings.ToLower(strings.ReplaceAll(field, "-", "_"))
		if !cursorMetadataFieldLooksStable(normalized) {
			continue
		}
		if value := scalarRawJSON(metadata[field]); value != "" {
			return field, value
		}
	}
	return "", ""
}

func cursorMetadataFieldLooksStable(field string) bool {
	if !(strings.Contains(field, "conversation") ||
		strings.Contains(field, "thread") ||
		strings.Contains(field, "chat") ||
		strings.Contains(field, "session")) {
		return false
	}
	return field == "id" || strings.HasSuffix(field, "_id") || strings.HasSuffix(field, "id")
}

type cursorFingerprintMessage struct {
	ID         string                      `json:"id"`
	MessageID  string                      `json:"message_id"`
	Role       string                      `json:"role"`
	ToolCallID string                      `json:"tool_call_id"`
	ToolCalls  []cursorFingerprintToolCall `json:"tool_calls"`
}

type cursorFingerprintToolCall struct {
	ID       string                        `json:"id"`
	Type     string                        `json:"type"`
	Function cursorFingerprintToolCallFunc `json:"function"`
}

type cursorFingerprintToolCallFunc struct {
	Name string `json:"name"`
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

	anchors := make([]string, 0, 8)
	for _, message := range messages {
		if value := strings.TrimSpace(message.ID); value != "" {
			anchors = append(anchors, "message_id:"+message.Role+":"+value)
		}
		if value := strings.TrimSpace(message.MessageID); value != "" {
			anchors = append(anchors, "message_id:"+message.Role+":"+value)
		}
		if value := strings.TrimSpace(message.ToolCallID); value != "" {
			anchors = append(anchors, "tool_result:"+value)
		}
		for _, toolCall := range message.ToolCalls {
			if value := strings.TrimSpace(toolCall.ID); value != "" {
				parts := []string{"tool_call", value, strings.TrimSpace(toolCall.Type), strings.TrimSpace(toolCall.Function.Name)}
				anchors = append(anchors, strings.Join(parts, ":"))
			}
		}
		if len(anchors) >= 8 {
			return "messages=" + strings.Join(anchors[:8], "|")
		}
	}
	if len(anchors) == 0 {
		return ""
	}
	return "messages=" + strings.Join(anchors, "|")
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
	return scalarRawJSON(rawValue)
}

func scalarRawJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
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
		return string(raw)
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
