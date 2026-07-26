package gemini

import (
	"encoding/json"
	"strings"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

// Wire types for the Cloud Code Assist / Antigravity generateContent envelope:
// {"project","model","userAgent","requestId","request":{contents,sessionId,...}}.

type caGenerateContentRequest struct {
	Model     string          `json:"model"`
	Project   string          `json:"project,omitempty"`
	UserAgent string          `json:"userAgent,omitempty"`
	RequestID string          `json:"requestId,omitempty"`
	Request   generateRequest `json:"request"`
}

type generateRequest struct {
	Contents          []content         `json:"contents"`
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	Tools             []toolDeclaration `json:"tools,omitempty"`
	ToolConfig        *toolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
	SessionID         string            `json:"sessionId,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	// ThoughtSignature is Gemini 3's opaque reasoning key. Required on
	// functionCall parts when replaying tool-result continuations.
	ThoughtSignature string            `json:"thoughtSignature,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
}

type functionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type functionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type toolDeclaration struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations"`
}

type functionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type toolConfig struct {
	FunctionCallingConfig functionCallingConfig `json:"functionCallingConfig"`
}

type functionCallingConfig struct {
	Mode string `json:"mode"`
}

type generationConfig struct {
	ThinkingConfig *thinkingConfig `json:"thinkingConfig,omitempty"`
}

type thinkingConfig struct {
	IncludeThoughts bool `json:"includeThoughts"`
}

func buildGenerateContentRequest(req codex.Request, project string, sessionID string) caGenerateContentRequest {
	contents, systemInstruction := convertMessages(req.Messages)
	requestID := req.RequestID
	if requestID == "" {
		requestID = sessionID
	}
	out := caGenerateContentRequest{
		Model:     req.Model,
		Project:   project,
		UserAgent: "antigravity",
		RequestID: requestID,
		Request: generateRequest{
			Contents:          contents,
			SystemInstruction: systemInstruction,
			Tools:             convertTools(req.Tools),
			ToolConfig:        convertToolChoice(req.ToolChoice),
			SessionID:         sessionID,
		},
	}
	if req.ReasoningEffort != "" && req.ReasoningEffort != "none" {
		out.Request.GenerationConfig = &generationConfig{
			ThinkingConfig: &thinkingConfig{IncludeThoughts: true},
		}
	}
	return out
}

func convertMessages(messages []openai.ChatMessage) ([]content, *content) {
	var systemTexts []string
	// Map tool_call_id -> function name so tool results can be converted into
	// functionResponse parts, which Gemini matches by name.
	toolCallNames := map[string]string{}
	for _, msg := range messages {
		for _, toolCall := range msg.ToolCalls {
			if toolCall.ID != "" {
				toolCallNames[toolCall.ID] = toolCallName(toolCall)
			}
		}
	}

	contents := make([]content, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "system", "developer":
			if text := openai.MessageText(msg.Content); text != "" {
				systemTexts = append(systemTexts, text)
			}
		case "assistant":
			parts := []part{}
			if text := openai.MessageText(msg.Content); text != "" {
				parts = append(parts, part{Text: text})
			}
			for _, toolCall := range msg.ToolCalls {
				name := toolCall.Function.Name
				args := json.RawMessage(toolCall.Function.Arguments)
				if toolCall.Type == "custom" && toolCall.Custom != nil {
					name = toolCall.Custom.Name
					args = json.RawMessage(`{"input":` + quoteString(toolCall.Custom.Input) + `}`)
				}
				if !json.Valid(args) || len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				if name == "" {
					continue
				}
				parts = append(parts, part{
					ThoughtSignature: resolveThoughtSignature(toolCall),
					FunctionCall: &functionCall{
						ID:   toolCall.ID,
						Name: name,
						Args: args,
					},
				})
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, content{Role: "model", Parts: parts})
		case "tool":
			name := toolCallNames[msg.ToolCallID]
			if name == "" {
				name = "tool"
			}
			contents = append(contents, content{Role: "user", Parts: []part{{
				FunctionResponse: &functionResponse{
					ID:   msg.ToolCallID,
					Name: name,
					Response: map[string]any{
						"output": openai.MessageText(msg.Content),
					},
				},
			}}})
		default: // user and anything unrecognized
			text := openai.MessageText(msg.Content)
			if text == "" {
				continue
			}
			contents = append(contents, content{Role: "user", Parts: []part{{Text: text}}})
		}
	}

	// Gemini requires at least one content entry.
	if len(contents) == 0 {
		contents = append(contents, content{Role: "user", Parts: []part{{Text: ""}}})
	}

	var systemInstruction *content
	if len(systemTexts) > 0 {
		systemInstruction = &content{Parts: []part{{Text: strings.Join(systemTexts, "\n\n")}}}
	}
	return contents, systemInstruction
}

// convertTools maps OpenAI Chat Completions tools
// ({"type":"function","function":{name,description,parameters}}) into a
// Gemini functionDeclarations block.
func convertTools(raw json.RawMessage) []toolDeclaration {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var tools []struct {
		Type     string `json:"type"`
		Function *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
		Custom *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"custom"`
		// Flat (Responses API style) fallback fields.
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	declarations := make([]functionDeclaration, 0, len(tools))
	for _, tool := range tools {
		decl := functionDeclaration{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  sanitizeSchema(tool.Parameters),
		}
		if tool.Function != nil {
			decl = functionDeclaration{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  sanitizeSchema(tool.Function.Parameters),
			}
		}
		if tool.Custom != nil {
			decl = functionDeclaration{
				Name:        tool.Custom.Name,
				Description: tool.Custom.Description,
				Parameters:  customToolInputSchema(tool.Custom.Parameters),
			}
		}
		if decl.Name == "" {
			continue
		}
		declarations = append(declarations, decl)
	}
	if len(declarations) == 0 {
		return nil
	}
	return []toolDeclaration{{FunctionDeclarations: declarations}}
}

// sanitizeSchema strips JSON Schema keywords the Gemini API rejects.
func sanitizeSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	cleaned := sanitizeSchemaValue(value)
	out, err := json.Marshal(cleaned)
	if err != nil {
		return nil
	}
	return out
}

func sanitizeSchemaValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{"$schema", "additionalProperties", "$defs", "definitions", "strict", "exclusiveMinimum", "exclusiveMaximum"} {
			delete(v, key)
		}
		for key, nested := range v {
			v[key] = sanitizeSchemaValue(nested)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = sanitizeSchemaValue(item)
		}
		return v
	default:
		return value
	}
}

func customToolNames(raw json.RawMessage) map[string]bool {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var tools []struct {
		Type   string `json:"type"`
		Custom *struct {
			Name string `json:"name"`
		} `json:"custom"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, tool := range tools {
		if tool.Type != "custom" {
			continue
		}
		name := tool.Name
		if tool.Custom != nil && tool.Custom.Name != "" {
			name = tool.Custom.Name
		}
		if name != "" {
			out[name] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toolCallName(toolCall openai.ToolCall) string {
	if toolCall.Type == "custom" && toolCall.Custom != nil {
		return toolCall.Custom.Name
	}
	return toolCall.Function.Name
}

func customToolInputSchema(raw json.RawMessage) json.RawMessage {
	if cleaned := sanitizeSchema(raw); len(cleaned) > 0 {
		return cleaned
	}
	return json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)
}

func quoteString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func convertToolChoice(raw json.RawMessage) *toolConfig {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var choice string
	if err := json.Unmarshal(raw, &choice); err == nil {
		switch choice {
		case "none":
			return &toolConfig{FunctionCallingConfig: functionCallingConfig{Mode: "NONE"}}
		case "required":
			return &toolConfig{FunctionCallingConfig: functionCallingConfig{Mode: "ANY"}}
		case "auto":
			return &toolConfig{FunctionCallingConfig: functionCallingConfig{Mode: "AUTO"}}
		}
		return nil
	}
	// Object form ({"type":"function","function":{"name":...}}) forces a call.
	return &toolConfig{FunctionCallingConfig: functionCallingConfig{Mode: "ANY"}}
}
