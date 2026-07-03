package gemini

import (
	"encoding/json"
	"fmt"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

type streamFrame struct {
	Response *generateResponse `json:"response"`
	Error    *apiError         `json:"error"`
}

type generateResponse struct {
	Candidates    []candidate   `json:"candidates"`
	UsageMetadata usageMetadata `json:"usageMetadata"`
	ModelVersion  string        `json:"modelVersion"`
	ResponseID    string        `json:"responseId"`
}

type candidate struct {
	Content      content `json:"content"`
	FinishReason string  `json:"finishReason"`
}

type usageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

func parseStreamEvent(data []byte, customTools map[string]bool) ([]codex.StreamEvent, error) {
	var frame streamFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return nil, fmt.Errorf("decode gemini stream event: %w", err)
	}
	if frame.Error != nil {
		return nil, geminiAPIError(frame.Error)
	}
	if frame.Response == nil {
		return nil, nil
	}

	resp := frame.Response
	events := []codex.StreamEvent{}
	for _, cand := range resp.Candidates {
		for i, p := range cand.Content.Parts {
			if p.Text != "" {
				event := codex.StreamEvent{Model: resp.ModelVersion, ID: resp.ResponseID}
				if p.Thought {
					event.ReasoningDelta = p.Text
				} else {
					event.Delta = p.Text
				}
				events = append(events, event)
			}
			if p.FunctionCall != nil {
				events = append(events, codex.StreamEvent{
					Model:         resp.ModelVersion,
					ID:            resp.ResponseID,
					ToolCallDelta: geminiToolCallDelta(resp.ResponseID, i, p.FunctionCall, customTools),
				})
			}
		}
		if cand.FinishReason != "" {
			events = append(events, codex.StreamEvent{
				Done:  true,
				Model: resp.ModelVersion,
				ID:    resp.ResponseID,
				Usage: openai.Usage{
					PromptTokens:     resp.UsageMetadata.PromptTokenCount,
					CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
					TotalTokens:      resp.UsageMetadata.TotalTokenCount,
				},
			})
		}
	}
	return events, nil
}

func geminiToolCallDelta(responseID string, index int, call *functionCall, customTools map[string]bool) *codex.ToolCallDelta {
	args := "{}"
	if len(call.Args) > 0 && json.Valid(call.Args) {
		args = string(call.Args)
	}
	id := call.ID
	if id == "" {
		id = fmt.Sprintf("call_%s_%d", stableResponseID(responseID), index)
	}
	delta := &codex.ToolCallDelta{
		Index: index,
		ID:    id,
		Type:  "function",
		Function: codex.ToolCallFunctionDelta{
			Name:      call.Name,
			Arguments: args,
		},
		Final: true,
	}
	if customTools[call.Name] {
		delta.Type = "custom"
		delta.Function.Arguments = customInput(args)
	}
	return delta
}

func customInput(args string) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(args), &obj); err == nil {
		if input, ok := obj["input"].(string); ok {
			return input
		}
	}
	return args
}

func stableResponseID(id string) string {
	if id == "" {
		return "gemini"
	}
	return id
}

func geminiAPIError(e *apiError) error {
	kind := codex.ErrorKindUpstream
	status := e.Code
	if status == 0 {
		status = 502
	}
	if status == 401 || status == 403 {
		kind = codex.ErrorKindAuth
	}
	message := e.Message
	if message == "" {
		message = e.Status
	}
	if message == "" {
		message = "gemini upstream error"
	}
	return codex.NewError(kind, status, message, nil)
}
