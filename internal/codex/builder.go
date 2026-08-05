package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/teslashibe/open-agent-api/internal/openai"
)

const (
	defaultModel = openai.DefaultModel
)

type requestKind string

const (
	requestKindPrewarm requestKind = "prewarm"
	requestKindTurn    requestKind = "turn"
)

type requestBuilder struct {
	profile        Profile
	scaffold       Scaffold
	codexHome      string
	now            func() time.Time
	cwd            func() string
	newSessionID   idGenerator
	newTurnID      idGenerator
	newPromptCache idGenerator
	installationID func() string
}

func newRequestBuilder(profile Profile, scaffold Scaffold, codexHome string) requestBuilder {
	builder := requestBuilder{
		profile:        profile,
		scaffold:       scaffold,
		codexHome:      codexHome,
		now:            time.Now,
		cwd:            func() string { return "." },
		newSessionID:   newUUIDV7String,
		newTurnID:      newUUIDV7String,
		newPromptCache: uuid.NewString,
	}
	builder.installationID = func() string {
		return loadInstallationID(builder.codexHome, uuid.NewString)
	}
	return builder
}

func (b requestBuilder) buildFaithful(messages []openai.ChatMessage, model, sessionID string, kind requestKind, reasoningEffort, verbosity string) map[string]any {
	systemTexts, conversation := splitMessages(messages)

	turnID := ""
	input := []any{}
	if kind == requestKindTurn {
		turnID = b.newTurnID()
		if b.scaffold.DeveloperItem != nil {
			input = append(input, b.scaffold.DeveloperItem)
		}
		if len(systemTexts) > 0 {
			content := make([]any, 0, len(systemTexts))
			for _, text := range systemTexts {
				content = append(content, map[string]any{
					"type": "input_text",
					"text": text,
				})
			}
			input = append(input, map[string]any{
				"type":    "message",
				"role":    "developer",
				"content": content,
			})
		}
		input = append(input, map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": b.environmentContext()},
			},
		})
		input = append(input, conversation...)
	}

	payload := map[string]any{
		"type":                "response.create",
		"model":               firstNonEmpty(model, b.profile.Model, defaultModel),
		"instructions":        firstNonEmpty(b.profile.Instructions, "You are Codex."),
		"input":               input,
		"tools":               firstNonNil(b.profile.Tools, []any{}),
		"tool_choice":         firstNonNil(b.profile.ToolChoice, "auto"),
		"parallel_tool_calls": firstNonNil(b.profile.ParallelToolCalls, true),
		"reasoning":           map[string]any{"effort": reasoningEffort},
		"store":               false,
		"stream":              true,
		"include":             firstNonNil(b.profile.Include, []any{"reasoning.encrypted_content"}),
		"prompt_cache_key":    sessionID,
		"text":                map[string]any{"verbosity": verbosity},
		"client_metadata": map[string]any{
			"session_id":                         sessionID,
			"thread_id":                          sessionID,
			"turn_id":                            turnID,
			"x-codex-turn-metadata":              turnMetadata(b.installationID(), sessionID, string(kind), turnID),
			"x-codex-ws-stream-request-start-ms": milliseconds(b.now()),
			"x-codex-installation-id":            b.installationID(),
			"x-codex-window-id":                  sessionID + ":0",
		},
	}
	if kind == requestKindPrewarm {
		payload["generate"] = false
	}
	return payload
}

func (b requestBuilder) buildMinimal(req Request) (map[string]any, error) {
	messages := req.Messages
	systemTexts, conversation := splitMessages(messages)
	instructions := strings.Join(systemTexts, "\n\n")
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}
	payload := map[string]any{
		"type":             "response.create",
		"model":            firstNonEmpty(req.Model, defaultModel),
		"instructions":     instructions,
		"input":            conversation,
		"stream":           true,
		"store":            false,
		"reasoning":        map[string]any{"effort": req.ReasoningEffort},
		"text":             map[string]any{"verbosity": req.Verbosity},
		"prompt_cache_key": b.newPromptCache(),
	}
	// Extraction turns are structured inference: strict output format and no
	// tool surface at all. Returning here guarantees no
	// tools/tool_choice/parallel_tool_calls can reach the payload even if a
	// caller populated them. Codex Responses supports no output-token cap, so
	// the extraction payload carries exactly the keys set above plus text.format
	// and nothing else.
	if req.Extraction {
		text := map[string]any{"verbosity": req.Verbosity}
		if rawJSONPresent(req.ResponseFormat) {
			format, err := decodeRawJSON(req.ResponseFormat)
			if err != nil {
				return nil, NewError(ErrorKindClient, 400, "invalid response_format JSON", err)
			}
			text["format"] = format
		}
		payload["text"] = text
		return payload, nil
	}
	if rawJSONPresent(req.Tools) {
		tools, err := decodeRawJSON(req.Tools)
		if err != nil {
			return nil, NewError(ErrorKindClient, 400, "invalid tools JSON", err)
		}
		payload["tools"] = normalizeToolsForCodex(tools)
	}
	if rawJSONPresent(req.ToolChoice) {
		toolChoice, err := decodeRawJSON(req.ToolChoice)
		if err != nil {
			return nil, NewError(ErrorKindClient, 400, "invalid tool_choice JSON", err)
		}
		payload["tool_choice"] = normalizeToolChoiceForCodex(toolChoice)
	}
	if req.ParallelToolCalls != nil {
		payload["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	return payload, nil
}

func (b requestBuilder) environmentContext() string {
	now := b.now()
	replacements := map[*regexp.Regexp]string{
		regexp.MustCompile(`(?s)<cwd>.*?</cwd>`):                   "<cwd>" + b.cwd() + "</cwd>",
		regexp.MustCompile(`(?s)<current_date>.*?</current_date>`): "<current_date>" + now.Format("2006-01-02") + "</current_date>",
		regexp.MustCompile(`(?s)<timezone>.*?</timezone>`):         "<timezone>" + timezoneName(now) + "</timezone>",
	}
	if b.scaffold.EnvironmentContext != "" {
		value := b.scaffold.EnvironmentContext
		for re, replacement := range replacements {
			value = re.ReplaceAllString(value, replacement)
		}
		return value
	}
	return "<environment_context>\n  <cwd>" + b.cwd() + "</cwd>\n</environment_context>"
}

func splitMessages(messages []openai.ChatMessage) ([]string, []any) {
	var systemTexts []string
	items := make([]any, 0, len(messages))
	for _, message := range messages {
		role := message.Role
		if role == "" {
			role = "user"
		}
		text := messageText(message.Content)
		if role == "developer" {
			role = "system"
		}
		if role == "system" {
			systemTexts = append(systemTexts, text)
			continue
		}
		if role == "tool" {
			if message.ToolCallID == "" {
				continue
			}
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": normalizeCallID(message.ToolCallID),
				"output":  text,
			})
			continue
		}
		partType := "input_text"
		if role == "assistant" {
			partType = "output_text"
		}
		if text != "" {
			items = append(items, map[string]any{
				"type": "message",
				"role": role,
				"content": []any{
					map[string]any{"type": partType, "text": text},
				},
			})
		}
		if role == "assistant" {
			for _, toolCall := range message.ToolCalls {
				items = append(items, functionCallItem(toolCall))
			}
		}
	}
	return systemTexts, items
}

func functionCallItem(toolCall openai.ToolCall) map[string]any {
	return map[string]any{
		"type":      "function_call",
		"call_id":   normalizeCallID(toolCall.ID),
		"name":      toolCall.Function.Name,
		"arguments": toolCall.Function.Arguments,
	}
}

// normalizeCallID maps client tool-call IDs into Codex's 64-char limit. Cursor
// can emit longer IDs; assistant function_call and matching function_call_output
// items must use the same normalized value.
func normalizeCallID(id string) string {
	if id == "" || len(id) <= 64 {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func messageText(raw json.RawMessage) string {
	return openai.MessageText(raw)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func rawJSONPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func decodeRawJSON(raw json.RawMessage) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// normalizeToolsForCodex converts OpenAI Chat Completions function tools, which
// nest the schema under a "function" object, into the flat Responses API shape
// Codex expects ({"type":"function","name":...,"parameters":...}). Tools that
// are already flat (or non-function tools) are passed through unchanged.
func normalizeToolsForCodex(tools any) any {
	list, ok := tools.([]any)
	if !ok {
		return tools
	}
	normalized := make([]any, 0, len(list))
	for _, item := range list {
		normalized = append(normalized, normalizeToolForCodex(item))
	}
	return normalized
}

func normalizeToolForCodex(tool any) any {
	toolMap, ok := tool.(map[string]any)
	if !ok {
		return tool
	}
	fn, ok := toolMap["function"].(map[string]any)
	if !ok {
		return tool
	}
	flat := map[string]any{"type": firstNonEmpty(asString(toolMap["type"]), "function")}
	for key, value := range fn {
		flat[key] = value
	}
	if _, hasParams := flat["parameters"]; !hasParams {
		flat["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return flat
}

// normalizeToolChoiceForCodex flattens a forced-function tool_choice object the
// same way as tools. String values ("auto"/"none"/"required") pass through.
func normalizeToolChoiceForCodex(choice any) any {
	choiceMap, ok := choice.(map[string]any)
	if !ok {
		return choice
	}
	fn, ok := choiceMap["function"].(map[string]any)
	if !ok {
		return choice
	}
	flat := map[string]any{"type": firstNonEmpty(asString(choiceMap["type"]), "function")}
	if name := asString(fn["name"]); name != "" {
		flat["name"] = name
	}
	return flat
}

func asString(value any) string {
	s, _ := value.(string)
	return s
}

func milliseconds(t time.Time) string {
	return strconvFormatInt(t.UnixMilli())
}

func timezoneName(t time.Time) string {
	if name, _ := t.Zone(); name != "" {
		return name
	}
	return "UTC"
}

func strconvFormatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
