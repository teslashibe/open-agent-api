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
	var value any
	if err := json.Unmarshal(rawValue, &value); err != nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return string(rawValue)
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
