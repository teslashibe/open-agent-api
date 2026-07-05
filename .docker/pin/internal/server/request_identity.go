package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"
)

var identityBodyFields = map[string]bool{
	"chat_id":             true,
	"conversation_id":     true,
	"metadata":            true,
	"model":               true,
	"parallel_tool_calls": true,
	"request_id":          true,
	"session_id":          true,
	"stream":              true,
	"thread_id":           true,
	"tool_choice":         true,
	"tools":               true,
	"user":                true,
	"workspace_id":        true,
}

func redactedRequestIdentity(c *fiber.Ctx, requestID string) string {
	body := summarizeIdentityBody(c.Body())
	headers := summarizeIdentityHeaders(c)
	return fmt.Sprintf(
		"request_identity request_id=%s method=%s path=%s remote_ip=%s user_agent_hash=%s header_names=%s cursor_headers=%s body_fields=%s body_scalars=%s metadata_fields=%s metadata_scalars=%s message_count=%d message_roles=%s tool_count=%d stream=%s tools_present=%t",
		requestID,
		c.Method(),
		c.Path(),
		c.IP(),
		headers.UserAgentHash,
		joinLogList(headers.Names),
		joinLogList(headers.Candidates),
		joinLogList(body.Fields),
		joinLogList(body.Scalars),
		joinLogList(body.MetadataFields),
		joinLogList(body.MetadataScalars),
		body.MessageCount,
		joinLogList(body.MessageRoles),
		body.ToolCount,
		body.Stream,
		body.ToolsPresent,
	)
}

type identityHeaderSummary struct {
	Names         []string
	Candidates    []string
	UserAgentHash string
}

func summarizeIdentityHeaders(c *fiber.Ctx) identityHeaderSummary {
	values := map[string][]string{}
	c.Request().Header.VisitAll(func(key []byte, value []byte) {
		name := strings.ToLower(string(key))
		values[name] = append(values[name], string(value))
	})

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	candidates := []string{}
	for _, name := range names {
		if !isIdentityHeaderCandidate(name) {
			continue
		}
		if isSecretHeader(name) {
			candidates = append(candidates, name+"=present")
			continue
		}
		candidates = append(candidates, name+"="+safeHash(strings.Join(values[name], ",")))
	}
	sort.Strings(candidates)

	return identityHeaderSummary{
		Names:         names,
		Candidates:    candidates,
		UserAgentHash: safeHash(strings.Join(values["user-agent"], ",")),
	}
}

func isIdentityHeaderCandidate(name string) bool {
	switch {
	case name == "user-agent", name == "forwarded", name == "x-forwarded-for":
		return true
	case strings.HasPrefix(name, "x-cursor-"):
		return true
	case strings.HasPrefix(name, "x-request-"):
		return true
	case strings.HasPrefix(name, "x-client-"):
		return true
	case strings.HasPrefix(name, "x-session-"):
		return true
	case strings.HasPrefix(name, "x-conversation-"):
		return true
	case strings.HasPrefix(name, "openai-"):
		return true
	case strings.HasPrefix(name, "anthropic-"):
		return true
	case strings.HasPrefix(name, "cf-"):
		return true
	default:
		return isSecretHeader(name)
	}
}

func isSecretHeader(name string) bool {
	return name == "authorization" || name == "cookie" || name == "set-cookie"
}

type identityBodySummary struct {
	Fields          []string
	Scalars         []string
	MetadataFields  []string
	MetadataScalars []string
	MessageCount    int
	MessageRoles    []string
	ToolCount       int
	Stream          string
	ToolsPresent    bool
}

func summarizeIdentityBody(raw []byte) identityBodySummary {
	out := identityBodySummary{Stream: "false"}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return out
	}

	out.Fields = sortedMapKeys(body)
	if rawStream, ok := body["stream"]; ok {
		if stream, ok := boolJSON(rawStream); ok {
			out.Stream = fmt.Sprintf("%t", stream)
		}
	}
	if rawMessages, ok := body["messages"]; ok {
		var messages []struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(rawMessages, &messages); err == nil {
			out.MessageCount = len(messages)
			for _, message := range messages {
				out.MessageRoles = append(out.MessageRoles, message.Role)
			}
		}
	}
	if rawTools, ok := body["tools"]; ok {
		out.ToolsPresent = rawJSONPresent(rawTools)
		var tools []json.RawMessage
		if err := json.Unmarshal(rawTools, &tools); err == nil {
			out.ToolCount = len(tools)
		}
	}
	for _, field := range out.Fields {
		if !identityBodyFields[field] || field == "metadata" || field == "tools" || field == "tool_choice" {
			continue
		}
		if value := scalarJSONHash(body[field]); value != "" {
			out.Scalars = append(out.Scalars, field+"="+value)
		}
	}
	if rawMetadata, ok := body["metadata"]; ok {
		out.MetadataFields, out.MetadataScalars = summarizeMetadata(rawMetadata)
	}
	return out
}

func summarizeMetadata(raw json.RawMessage) ([]string, []string) {
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, nil
	}
	fields := sortedMapKeys(metadata)
	scalars := []string{}
	for _, field := range fields {
		if value := scalarJSONHash(metadata[field]); value != "" {
			scalars = append(scalars, field+"="+value)
		}
	}
	return fields, scalars
}

func scalarJSONHash(raw json.RawMessage) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return safeHash(typed)
	case bool:
		return fmt.Sprintf("%t", typed)
	case float64:
		return safeHash(string(raw))
	default:
		return ""
	}
}

func boolJSON(raw json.RawMessage) (bool, bool) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func joinLogList(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}
