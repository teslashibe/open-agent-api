package server

import "strings"

const degenerateTurnMinTextBytes = 120

func degenerateAgentTurn(toolsPresent bool, finishReason string, textBytes int, toolCallCount int) bool {
	return toolsPresent &&
		finishReason == "stop" &&
		toolCallCount == 0 &&
		textBytes >= degenerateTurnMinTextBytes
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
