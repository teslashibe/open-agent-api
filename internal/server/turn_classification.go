package server

import (
	"strings"

	"github.com/teslashibe/codex-chat-api/internal/openai"
)

type turnClass string

const (
	turnClassToolGenerating         turnClass = "tool_generating"
	turnClassToolResultContinuation turnClass = "tool_result_continuation"
	turnClassFinalProseContinuation turnClass = "final_prose_continuation"
	turnClassSimpleNoTool           turnClass = "simple_no_tool"
	turnClassUnknown                turnClass = "unknown"
)

func classifyTurn(req openai.ChatCompletionRequest, toolsPresent bool) turnClass {
	if !toolsPresent {
		return turnClassSimpleNoTool
	}
	if len(req.Messages) == 0 {
		return turnClassUnknown
	}

	last := req.Messages[len(req.Messages)-1]
	if strings.TrimSpace(last.Role) == "tool" && hasMatchedToolResult(req.Messages) {
		return turnClassToolResultContinuation
	}
	if strings.TrimSpace(last.Role) == "assistant" && len(last.ToolCalls) == 0 && strings.TrimSpace(openai.MessageText(last.Content)) != "" {
		return turnClassFinalProseContinuation
	}
	return turnClassToolGenerating
}

func hasMatchedToolResult(messages []openai.ChatMessage) bool {
	toolCalls := map[string]bool{}
	for _, message := range messages {
		if strings.TrimSpace(message.Role) == "assistant" {
			for _, toolCall := range message.ToolCalls {
				if id := strings.TrimSpace(toolCall.ID); id != "" {
					toolCalls[id] = true
				}
			}
		}
		if strings.TrimSpace(message.Role) != "tool" {
			continue
		}
		if id := strings.TrimSpace(message.ToolCallID); id != "" && toolCalls[id] {
			return true
		}
	}
	return false
}

func agentQueuePriority(class turnClass) int {
	switch class {
	case turnClassToolResultContinuation:
		return 10
	case turnClassFinalProseContinuation:
		return 5
	default:
		return 0
	}
}
