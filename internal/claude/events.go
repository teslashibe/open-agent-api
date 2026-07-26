package claude

import (
	"encoding/json"
	"fmt"

	"github.com/teslashibe/open-chat-api/internal/codex"
	"github.com/teslashibe/open-chat-api/internal/openai"
)

type jsonlEvent struct {
	Type        string          `json:"type"`
	Subtype     string          `json:"subtype"`
	SessionID   string          `json:"session_id"`
	Model       string          `json:"model"`
	Result      string          `json:"result"`
	IsError     bool            `json:"is_error"`
	Error       string          `json:"error"`
	StopReason  string          `json:"stop_reason"`
	Usage       usage           `json:"usage"`
	Message     message         `json:"message"`
	StreamEvent *anthropicEvent `json:"event"`
}

type anthropicEvent struct {
	Type         string       `json:"type"`
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
	Type string `json:"type"`
	Text string `json:"text"`
}

type delta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func parseJSONLEvent(data []byte) (codex.StreamEvent, bool, error) {
	var event jsonlEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return codex.StreamEvent{}, false, fmt.Errorf("decode claude stream event: %w", err)
	}
	if event.Error != "" || event.IsError {
		return codex.StreamEvent{}, false, codex.NewError(codex.ErrorKindUpstream, 502, "claude code error", fmt.Errorf("%s", event.Error))
	}
	if event.Type == "system" {
		return codex.StreamEvent{Model: event.Model, ID: event.SessionID}, event.Model != "" || event.SessionID != "", nil
	}
	if event.StreamEvent != nil {
		return parseStreamEvent(event.StreamEvent)
	}
	if event.Type == "result" {
		return codex.StreamEvent{Done: true, Model: event.Model, ID: event.SessionID, Usage: usageToOpenAI(event.Usage)}, true, nil
	}
	return codex.StreamEvent{}, false, nil
}

func parseStreamEvent(event *anthropicEvent) (codex.StreamEvent, bool, error) {
	switch event.Type {
	case "message_start":
		return codex.StreamEvent{Model: event.Message.Model, ID: event.Message.ID, Usage: usageToOpenAI(event.Message.Usage)}, true, nil
	case "content_block_delta":
		if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
			return codex.StreamEvent{Delta: event.Delta.Text}, true, nil
		}
	case "message_delta":
		return codex.StreamEvent{Usage: usageToOpenAI(event.Usage)}, hasUsage(event.Usage), nil
	case "message_stop":
		return codex.StreamEvent{Done: true}, true, nil
	}
	return codex.StreamEvent{}, false, nil
}

func usageToOpenAI(u usage) openai.Usage {
	prompt := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	return openai.Usage{PromptTokens: prompt, CompletionTokens: u.OutputTokens, TotalTokens: prompt + u.OutputTokens}
}

func hasUsage(u usage) bool {
	return u.InputTokens != 0 || u.OutputTokens != 0 || u.CacheCreationInputTokens != 0 || u.CacheReadInputTokens != 0
}
