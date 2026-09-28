package claude

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

type jsonlEvent struct {
	Type           string `json:"type"`
	Subtype        string `json:"subtype"`
	SessionID      string `json:"session_id"`
	Model          string `json:"model"`
	Result         string `json:"result"`
	IsError        bool   `json:"is_error"`
	Error          string `json:"error"`
	APIErrorStatus int    `json:"api_error_status"`
	Usage          usage  `json:"usage"`
	// Message is an object on assistant/user events but a plain string on
	// some system events (permission_denied), so it is decoded lazily.
	Message       json.RawMessage `json:"message"`
	StreamEvent   *anthropicEvent `json:"event"`
	RateLimitInfo *rateLimitInfo  `json:"rate_limit_info"`
}

type rateLimitInfo struct {
	Status         string `json:"status"`
	ResetsAt       int64  `json:"resetsAt"`
	IsUsingOverage bool   `json:"isUsingOverage"`
}

type anthropicEvent struct {
	Type         string       `json:"type"`
	Index        int          `json:"index"`
	Message      message      `json:"message"`
	Delta        delta        `json:"delta"`
	ContentBlock contentBlock `json:"content_block"`
	Usage        usage        `json:"usage"`
}

type message struct {
	ID         string         `json:"id"`
	Model      string         `json:"model"`
	Usage      usage          `json:"usage"`
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type delta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	StopReason  string `json:"stop_reason"`
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type toolUseBlockState struct {
	id    string
	name  string
	input strings.Builder
	start json.RawMessage
}

// streamParser turns Claude Code stream-json lines into gateway stream events.
// Native tool_use blocks become one complete ToolCallDelta each (the server's
// Cursor accumulator emits exactly one frame per call). It stops at the first
// message_stop: the CLI runs with --max-turns 1, and anything after a
// tool_use stop is the CLI denying the tool locally, which must not reach the
// client.
type streamParser struct {
	tools      *toolSet
	blocks     map[int]*toolUseBlockState
	emitted    map[string]bool
	toolIndex  int
	stopReason string
	errorCode  string
	usage      usage
}

func newStreamParser(tools *toolSet) *streamParser {
	return &streamParser{tools: tools, blocks: map[int]*toolUseBlockState{}, emitted: map[string]bool{}}
}

// consume parses one line. done reports that the model turn is over and the
// process can be stopped.
func (p *streamParser) consume(data []byte) (events []codex.StreamEvent, done bool, err error) {
	var event jsonlEvent
	if err := json.Unmarshal(data, &event); err != nil {
		// The CLI adds event types between releases; a line this parser
		// cannot decode must not fail an otherwise healthy turn.
		return nil, false, nil
	}
	switch event.Type {
	case "system":
		if event.Subtype == "init" && (event.Model != "" || event.SessionID != "") {
			return []codex.StreamEvent{{Model: event.Model, ID: event.SessionID}}, false, nil
		}
	case "stream_event":
		if event.StreamEvent != nil {
			return p.streamEvent(event.StreamEvent)
		}
	case "assistant":
		if event.Error != "" {
			p.errorCode = event.Error
			return nil, false, nil
		}
		// Complete messages duplicate the partial stream; they only matter
		// for tool_use blocks whose partial events were missed.
		var msg message
		_ = json.Unmarshal(event.Message, &msg)
		for _, b := range msg.Content {
			if b.Type == "tool_use" && b.ID != "" && !p.emitted[b.ID] {
				events = append(events, p.toolEvent(b.ID, b.Name, string(b.Input)))
			}
		}
		return events, false, nil
	case "rate_limit_event":
		if event.RateLimitInfo != nil && event.RateLimitInfo.Status == "rejected" && !event.RateLimitInfo.IsUsingOverage {
			return nil, true, rateLimitError(event.RateLimitInfo)
		}
	case "result":
		if event.IsError && event.Subtype != "error_max_turns" {
			return nil, true, resultError(event, p.errorCode)
		}
		return []codex.StreamEvent{{Done: true, Model: event.Model, ID: event.SessionID, Usage: usageToOpenAI(event.Usage)}}, true, nil
	}
	return nil, false, nil
}

func (p *streamParser) streamEvent(event *anthropicEvent) ([]codex.StreamEvent, bool, error) {
	switch event.Type {
	case "message_start":
		p.mergeUsage(event.Message.Usage)
		return []codex.StreamEvent{{Model: event.Message.Model, ID: event.Message.ID, Usage: usageToOpenAI(p.usage)}}, false, nil
	case "content_block_start":
		if event.ContentBlock.Type == "tool_use" {
			p.blocks[event.Index] = &toolUseBlockState{id: event.ContentBlock.ID, name: event.ContentBlock.Name, start: event.ContentBlock.Input}
		}
	case "content_block_delta":
		switch event.Delta.Type {
		case "text_delta":
			if event.Delta.Text != "" {
				return []codex.StreamEvent{{Delta: event.Delta.Text}}, false, nil
			}
		case "input_json_delta":
			if state := p.blocks[event.Index]; state != nil {
				state.input.WriteString(event.Delta.PartialJSON)
			}
		}
	case "content_block_stop":
		if state := p.blocks[event.Index]; state != nil {
			delete(p.blocks, event.Index)
			input := state.input.String()
			if strings.TrimSpace(input) == "" {
				input = string(state.start)
			}
			if !p.emitted[state.id] {
				return []codex.StreamEvent{p.toolEvent(state.id, state.name, input)}, false, nil
			}
		}
	case "message_delta":
		if event.Delta.StopReason != "" {
			p.stopReason = event.Delta.StopReason
		}
		if hasUsage(event.Usage) {
			p.mergeUsage(event.Usage)
			return []codex.StreamEvent{{Usage: usageToOpenAI(p.usage)}}, false, nil
		}
	case "message_stop":
		return []codex.StreamEvent{{Done: true}}, true, nil
	}
	return nil, false, nil
}

// mergeUsage folds message_delta usage (cumulative output, often no input
// counts) into the message_start usage so the reported total stays complete.
func (p *streamParser) mergeUsage(u usage) {
	if u.InputTokens > 0 {
		p.usage.InputTokens = u.InputTokens
	}
	if u.CacheCreationInputTokens > 0 {
		p.usage.CacheCreationInputTokens = u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens > 0 {
		p.usage.CacheReadInputTokens = u.CacheReadInputTokens
	}
	if u.OutputTokens > 0 {
		p.usage.OutputTokens = u.OutputTokens
	}
}

func (p *streamParser) toolEvent(id, modelName, input string) codex.StreamEvent {
	p.emitted[id] = true
	spec, _ := p.tools.clientTool(modelName)
	index := p.toolIndex
	p.toolIndex++
	kind := "function"
	args := toolArguments(spec, input)
	if spec.Type == "custom" {
		kind = "custom"
	}
	return codex.StreamEvent{ToolCallDelta: &codex.ToolCallDelta{
		Index: index,
		ID:    id,
		Type:  kind,
		Function: codex.ToolCallFunctionDelta{
			Name:      spec.Name,
			Arguments: args,
		},
		Final: true,
	}}
}

// toolArguments undoes the schema wrapping applied by inputSchema: custom
// tools return their raw input string, non-object schemas their inner value.
func toolArguments(spec toolSpec, input string) string {
	input = strings.TrimSpace(input)
	if input == "" || !json.Valid([]byte(input)) {
		input = "{}"
	}
	wrapped := spec.Type == "custom"
	if !wrapped && len(spec.Parameters) > 0 {
		var schema map[string]any
		if json.Unmarshal(spec.Parameters, &schema) == nil {
			if kind, ok := schema["type"].(string); ok && kind != "object" {
				wrapped = true
			}
		}
	}
	if !wrapped {
		return input
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(input), &object) != nil {
		return input
	}
	inner, ok := object["input"]
	if !ok {
		return input
	}
	if spec.Type == "custom" {
		var text string
		if json.Unmarshal(inner, &text) == nil {
			return text
		}
	}
	return string(inner)
}

func resultError(event jsonlEvent, code string) error {
	message := strings.TrimSpace(event.Result)
	if message == "" {
		message = code
	}
	if message == "" {
		message = "claude code returned an error"
	}
	status := event.APIErrorStatus
	kind := codex.ErrorKindUpstream
	switch {
	case status == 401 || status == 403:
		kind = codex.ErrorKindAuth
	case status == 400 || status == 404 || status == 413:
		kind = codex.ErrorKindClient
	case status == 0:
		status = 502
	}
	if code != "" && code != message {
		message = code + ": " + message
	}
	return codex.NewError(kind, status, "claude code error", fmt.Errorf("%s", message))
}

func rateLimitError(info *rateLimitInfo) error {
	err := &codex.Error{
		Kind:    codex.ErrorKindUpstream,
		Status:  429,
		Message: "claude code usage limit reached",
		Err:     fmt.Errorf("claude subscription rate limit rejected"),
	}
	if info.ResetsAt > 0 {
		err.ResetAt = time.Unix(info.ResetsAt, 0)
	}
	return err
}

func usageToOpenAI(u usage) openai.Usage {
	prompt := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	return openai.Usage{PromptTokens: prompt, CompletionTokens: u.OutputTokens, TotalTokens: prompt + u.OutputTokens}
}

func hasUsage(u usage) bool {
	return u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheCreationInputTokens != 0 || u.CacheReadInputTokens != 0
}
