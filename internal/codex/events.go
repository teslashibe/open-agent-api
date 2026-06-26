package codex

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/teslashibe/codex-chat-api/internal/openai"
)

type codexEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta"`
	Status   int             `json:"status"`
	Error    json.RawMessage `json:"error"`
	Response *codexResponse  `json:"response"`
}

type codexResponse struct {
	ID    string     `json:"id"`
	Model string     `json:"model"`
	Usage codexUsage `json:"usage"`
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

func parseStreamEvent(raw []byte) (StreamEvent, bool, error) {
	var event codexEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return StreamEvent{}, false, NewError(ErrorKindUpstream, http.StatusBadGateway, "invalid codex websocket frame", err)
	}

	switch event.Type {
	case "response.output_text.delta":
		return StreamEvent{Delta: event.Delta}, false, nil
	case "response.completed":
		done := StreamEvent{Done: true}
		if event.Response != nil {
			done.ID = event.Response.ID
			done.Model = event.Response.Model
			done.Usage = mapUsage(event.Response.Usage)
		}
		return done, true, nil
	case "response.failed", "error":
		status := event.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		return StreamEvent{}, true, NewError(ErrorKindUpstream, status, "codex backend error", fmt.Errorf("codex event type %s", event.Type))
	default:
		return StreamEvent{}, false, nil
	}
}

func mapUsage(usage codexUsage) openai.Usage {
	return openai.Usage{
		PromptTokens:     usage.InputTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      usage.TotalTokens,
	}
}
