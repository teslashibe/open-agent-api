package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/teslashibe/open-chat-api/internal/codex"
	"github.com/teslashibe/open-chat-api/internal/openai"
)

const degenerateRetryInstructions = "This turn requires using the provided tools. Call at least one appropriate tool now. Do not answer with prose only."

func shouldRetryDegenerateTurn(enabled bool, toolsPresent bool, messages []openai.ChatMessage, toolCallCount int, assistantText string, textBytes int) bool {
	if !enabled || !toolsPresent || toolCallCount > 0 || len(messages) == 0 {
		return false
	}
	switch messages[len(messages)-1].Role {
	case "user":
		// User agent turns should already use tool_choice=required upstream.
		// Retry only when the model still stalls with planning prose.
		return detectLoopPhrase(assistantText) != ""
	case "tool":
		// Tool continuations may legitimately finish with a long prose summary.
		return detectLoopPhrase(assistantText) != ""
	default:
		return false
	}
}

func degenerateAgentTurn(toolsPresent bool, finishReason string, textBytes int, toolCallCount int) bool {
	return toolsPresent &&
		finishReason == "stop" &&
		toolCallCount == 0 &&
		textBytes > 0
}

func detectLoopPhrase(text string) string {
	lower := strings.ToLower(text)
	for _, phrase := range []string{
		"i'll ",
		"let me ",
		"i will ",
		"going to ",
		"i'm going to ",
	} {
		if strings.Contains(lower, phrase) {
			return strings.TrimSpace(phrase)
		}
	}
	return ""
}

func completeWithDegenerateRetry(
	ctx context.Context,
	opts options,
	service codex.Service,
	req codex.Request,
	toolsPresent bool,
	requestID string,
) (codex.Completion, error) {
	completion, err := service.Complete(ctx, req)
	if err != nil {
		return completion, err
	}

	toolCallCount := len(completion.ToolCalls)
	finishReason := "stop"
	if toolCallCount > 0 {
		finishReason = "tool_calls"
	}
	textBytes := len(completion.Text)
	if !degenerateAgentTurn(toolsPresent, finishReason, textBytes, toolCallCount) {
		return completion, nil
	}

	loopPhrase := detectLoopPhrase(completion.Text)
	if !shouldRetryDegenerateTurn(opts.contextConfig.DegenerateTurnRetryEnabled, toolsPresent, req.Messages, toolCallCount, completion.Text, textBytes) {
		return completion, nil
	}

	logDegenerateTurn(opts, requestID, textBytes, toolCallCount, loopPhrase, len(req.Messages))
	logDegenerateTurnRetry(opts, requestID, 1, 0)
	retryCompletion, retryErr := service.Complete(ctx, buildDegenerateRetryRequest(req))
	if retryErr != nil {
		logLine(opts, "degenerate_turn_retry_error request_id=%s err=%s\n", requestID, detailedError(retryErr))
		return completion, nil
	}
	logDegenerateTurnRetry(opts, requestID, 1, len(retryCompletion.ToolCalls))
	if len(retryCompletion.ToolCalls) > 0 {
		return retryCompletion, nil
	}
	return completion, nil
}

func buildDegenerateRetryRequest(req codex.Request) codex.Request {
	retry := req
	retry.Messages = append([]openai.ChatMessage{}, req.Messages...)
	retry.Messages = append([]openai.ChatMessage{{
		Role:    "system",
		Content: openai.TextContent(degenerateRetryInstructions),
	}}, retry.Messages...)
	retry.ToolChoice = json.RawMessage(`"required"`)
	return retry
}

func applyAgentTurnToolChoice(req codex.Request) codex.Request {
	if !rawJSONPresent(req.Tools) || len(req.Messages) == 0 {
		return req
	}
	if req.Messages[len(req.Messages)-1].Role != "user" || rawJSONPresent(req.ToolChoice) {
		return req
	}
	req.ToolChoice = json.RawMessage(`"required"`)
	return req
}

func logDegenerateTurn(opts options, requestID string, textBytes int, toolCallCount int, loopPhrase string, messageCount int) {
	if loopPhrase == "" {
		loopPhrase = "-"
	}
	logLine(
		opts,
		"degenerate_turn request_id=%s text_bytes=%d tool_call_count=%d loop_phrase=%s message_count=%d\n",
		requestID,
		textBytes,
		toolCallCount,
		loopPhrase,
		messageCount,
	)
}

func logDegenerateTurnRetry(opts options, requestID string, attempt int, toolCallCount int) {
	logLine(
		opts,
		"degenerate_turn_retry request_id=%s attempt=%d tool_call_count=%d\n",
		requestID,
		attempt,
		toolCallCount,
	)
}

func logStreamOutput(opts options, requestID string, textBytes int, toolCallCount int, toolArgChars int, loopPhrase string) {
	if loopPhrase == "" {
		loopPhrase = "-"
	}
	logLine(
		opts,
		"stream_output request_id=%s text_bytes=%d tool_call_count=%d tool_arg_chars=%d loop_phrase=%s\n",
		requestID,
		textBytes,
		toolCallCount,
		toolArgChars,
		loopPhrase,
	)
}
