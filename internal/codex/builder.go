package codex

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/teslashibe/codex-chat-api/internal/openai"
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

func (b requestBuilder) buildMinimal(messages []openai.ChatMessage, model, reasoningEffort, verbosity string) map[string]any {
	systemTexts, conversation := splitMessages(messages)
	instructions := strings.Join(systemTexts, "\n\n")
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}
	return map[string]any{
		"type":             "response.create",
		"model":            firstNonEmpty(model, defaultModel),
		"instructions":     instructions,
		"input":            conversation,
		"stream":           true,
		"store":            false,
		"reasoning":        map[string]any{"effort": reasoningEffort},
		"text":             map[string]any{"verbosity": verbosity},
		"prompt_cache_key": b.newPromptCache(),
	}
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
		if role == "system" {
			systemTexts = append(systemTexts, text)
			continue
		}
		partType := "input_text"
		if role == "assistant" {
			partType = "output_text"
		}
		items = append(items, map[string]any{
			"type": "message",
			"role": role,
			"content": []any{
				map[string]any{"type": partType, "text": text},
			},
		})
	}
	return systemTexts, items
}

func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, part := range parts {
			if value, ok := part["text"].(string); ok {
				b.WriteString(value)
			}
		}
		return b.String()
	}
	return string(raw)
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
