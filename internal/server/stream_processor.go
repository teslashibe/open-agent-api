package server

import (
	"bufio"
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/openai"
	"github.com/teslashibe/codex-chat-api/internal/sse"
)

type deltaTextMode int

const (
	deltaTextDrop deltaTextMode = iota
	deltaTextContent
	deltaTextReasoning
)

type streamProcessor struct {
	ctx    context.Context
	cancel context.CancelFunc
	w      *bufio.Writer
	opts   options

	id      string
	created int64
	model   *string

	streamID string
	outcome  *string

	deltas, toolDeltas, upstreamEvents *int
	textBytes, toolArgChars           *int
	assistantText                     *strings.Builder

	toolCallEmitted         *bool
	streamedToolCallIDs     map[string]bool
	streamedToolCallIndexes map[int]bool
	toolCallIndexByKey      map[string]int
	nextToolCallIndex       *int
}

func agentTurnExpectsToolCalls(messages []openai.ChatMessage, toolsPresent bool) bool {
	if !toolsPresent || len(messages) == 0 {
		return false
	}
	switch messages[len(messages)-1].Role {
	case "user", "tool":
		return true
	default:
		return false
	}
}

func agentTurnStreamsReasoning(messages []openai.ChatMessage) bool {
	return len(messages) > 0 && messages[len(messages)-1].Role == "user"
}

func streamEventHasToolCall(event codex.StreamEvent) bool {
	return len(event.ToolCalls) > 0 || event.ToolCallDelta != nil
}

func newStreamProcessor(
	ctx context.Context,
	cancel context.CancelFunc,
	w *bufio.Writer,
	opts options,
	id string,
	created int64,
	model *string,
	streamID string,
	outcome *string,
	deltas, toolDeltas, upstreamEvents *int,
	textBytes, toolArgChars *int,
	assistantText *strings.Builder,
	toolCallEmitted *bool,
) *streamProcessor {
	return &streamProcessor{
		ctx:                     ctx,
		cancel:                  cancel,
		w:                       w,
		opts:                    opts,
		id:                      id,
		created:                 created,
		model:                   model,
		streamID:                streamID,
		outcome:                 outcome,
		deltas:                  deltas,
		toolDeltas:              toolDeltas,
		upstreamEvents:          upstreamEvents,
		textBytes:               textBytes,
		toolArgChars:            toolArgChars,
		assistantText:           assistantText,
		toolCallEmitted:         toolCallEmitted,
		streamedToolCallIDs:     map[string]bool{},
		streamedToolCallIndexes: map[int]bool{},
		toolCallIndexByKey:      map[string]int{},
		nextToolCallIndex:       new(int),
	}
}

func (p *streamProcessor) normalizeToolCallIndex(key string) int {
	if mapped, ok := p.toolCallIndexByKey[key]; ok {
		return mapped
	}
	mapped := *p.nextToolCallIndex
	p.toolCallIndexByKey[key] = mapped
	*p.nextToolCallIndex++
	return mapped
}

func (p *streamProcessor) writeTextDelta(text string, mode deltaTextMode) bool {
	if text == "" || mode == deltaTextDrop {
		return true
	}
	delta := openai.ChatDelta{}
	switch mode {
	case deltaTextContent:
		delta.Content = text
	case deltaTextReasoning:
		delta.ReasoningContent = text
	default:
		return true
	}
	*p.deltas++
	if !writeSSE(p.ctx, p.cancel, p.w, openai.ChatCompletionChunk{
		ID:      p.id,
		Object:  "chat.completion.chunk",
		Created: p.created,
		Model:   *p.model,
		Choices: []openai.ChatCompletionChunkChoice{
			{Index: 0, Delta: delta},
		},
	}) {
		*p.outcome = "client_disconnect"
		return false
	}
	return true
}

func (p *streamProcessor) handleEvent(event codex.StreamEvent, write bool, textMode deltaTextMode) (stop bool) {
	*p.upstreamEvents++
	if event.Err != nil {
		if write {
			logLine(p.opts, "stream_error id=%s model=%s err=%s\n", p.streamID, defaultString(event.Model, *p.model), detailedError(event.Err))
			_ = writeSSE(p.ctx, p.cancel, p.w, errorChunk(p.id, p.created, defaultString(event.Model, *p.model), publicErrorMessage(event.Err)))
		}
		*p.outcome = "upstream_error"
		return true
	}
	if event.Model != "" {
		*p.model = event.Model
	}
	if event.ReasoningDelta != "" {
		*p.textBytes += len(event.ReasoningDelta)
		p.assistantText.WriteString(event.ReasoningDelta)
		if write && !p.writeTextDelta(event.ReasoningDelta, deltaTextReasoning) {
			return true
		}
	}
	if event.Delta != "" {
		*p.textBytes += len(event.Delta)
		p.assistantText.WriteString(event.Delta)
		if write {
			if !p.writeTextDelta(event.Delta, textMode) {
				return true
			}
		}
	}
	for i, toolCall := range event.ToolCalls {
		if skipCompletedToolCallDelta(i, toolCall, p.streamedToolCallIDs, p.streamedToolCallIndexes) {
			continue
		}
		*p.toolArgChars += len(toolCall.Function.Arguments)
		*p.toolCallEmitted = true
		*p.toolDeltas++
		fullDelta := openAIToolCallFullDelta(i, toolCall)
		fullKey := toolCall.ID
		if fullKey == "" {
			fullKey = "fslot:" + strconv.Itoa(i)
		}
		fullDelta.Index = p.normalizeToolCallIndex(fullKey)
		if write && !writeSSE(p.ctx, p.cancel, p.w, openai.ChatCompletionChunk{
			ID:      p.id,
			Object:  "chat.completion.chunk",
			Created: p.created,
			Model:   *p.model,
			Choices: []openai.ChatCompletionChunkChoice{
				{Index: 0, Delta: openai.ChatDelta{ToolCalls: []openai.ToolCallDelta{fullDelta}}},
			},
		}) {
			*p.outcome = "client_disconnect"
			return true
		}
	}
	if event.ToolCallDelta != nil {
		*p.toolCallEmitted = true
		*p.toolDeltas++
		*p.toolArgChars += len(event.ToolCallDelta.Function.Arguments)
		p.streamedToolCallIndexes[event.ToolCallDelta.Index] = true
		if event.ToolCallDelta.ID != "" {
			p.streamedToolCallIDs[event.ToolCallDelta.ID] = true
		}
		outDelta := openAIToolCallDelta(*event.ToolCallDelta)
		outDelta.Index = p.normalizeToolCallIndex("idx:" + strconv.Itoa(event.ToolCallDelta.Index))
		if write && !writeSSE(p.ctx, p.cancel, p.w, openai.ChatCompletionChunk{
			ID:      p.id,
			Object:  "chat.completion.chunk",
			Created: p.created,
			Model:   *p.model,
			Choices: []openai.ChatCompletionChunkChoice{
				{Index: 0, Delta: openai.ChatDelta{ToolCalls: []openai.ToolCallDelta{outDelta}}},
			},
		}) {
			*p.outcome = "client_disconnect"
			return true
		}
	}
	return false
}

func (p *streamProcessor) writeFinish() bool {
	finish := "stop"
	if *p.toolCallEmitted {
		finish = "tool_calls"
	}
	if !writeSSE(p.ctx, p.cancel, p.w, openai.ChatCompletionChunk{
		ID:      p.id,
		Object:  "chat.completion.chunk",
		Created: p.created,
		Model:   *p.model,
		Choices: []openai.ChatCompletionChunkChoice{
			{Index: 0, Delta: openai.ChatDelta{}, FinishReason: &finish},
		},
	}) {
		*p.outcome = "client_disconnect"
		return false
	}
	return true
}

func (p *streamProcessor) resetAttemptStats() {
	*p.deltas = 0
	*p.toolDeltas = 0
	*p.upstreamEvents = 0
	*p.textBytes = 0
	*p.toolArgChars = 0
	p.assistantText.Reset()
	*p.toolCallEmitted = false
	*p.nextToolCallIndex = 0
	p.streamedToolCallIDs = map[string]bool{}
	p.streamedToolCallIndexes = map[int]bool{}
	p.toolCallIndexByKey = map[string]int{}
	*p.outcome = "completed"
}

func (p *streamProcessor) replay(events []codex.StreamEvent, textMode deltaTextMode) bool {
	for _, event := range events {
		if p.handleEvent(event, true, textMode) {
			return false
		}
		if event.Done {
			break
		}
	}
	return true
}

func (p *streamProcessor) streamEvents(events <-chan codex.StreamEvent, textMode deltaTextMode) bool {
	for event := range events {
		if p.handleEvent(event, true, textMode) {
			return false
		}
		if event.Done {
			break
		}
	}
	return true
}

func deliverToolStream(
	ctx context.Context,
	opts options,
	w *bufio.Writer,
	cancel context.CancelFunc,
	req codex.Request,
	service codex.Service,
	events <-chan codex.StreamEvent,
	id string,
	created int64,
	model string,
	streamID string,
) (outcome string, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, toolCallCount int, assistantText string, start time.Time) {
	outcome = "completed"
	var assistant strings.Builder
	proc := newStreamProcessor(
		ctx, cancel, w, opts, id, created, &model, streamID, &outcome,
		&deltas, &toolDeltas, &upstreamEvents, &textBytes, &toolArgChars, &assistant, new(bool),
	)
	start = opts.now()
	toolsPresent := rawJSONPresent(req.Tools)
	agentTurn := agentTurnExpectsToolCalls(req.Messages, toolsPresent)
	retryEnabled := opts.contextConfig.DegenerateTurnRetryEnabled && toolsPresent

	finishStream := func() (string, int, int, int, int, int, int, string, time.Time) {
		if outcome == "completed" {
			if !proc.writeFinish() {
				return outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start
			}
		}
		if outcome != "client_disconnect" {
			_, _ = w.Write(sse.Done())
			_ = w.Flush()
		}
		return outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start
	}

	// Tool-result continuations and plain chat stream through immediately.
	if !agentTurn {
		proc.streamEvents(events, deltaTextContent)
		return finishStream()
	}

	textMode := deltaTextContent
	if agentTurnStreamsReasoning(req.Messages) {
		textMode = deltaTextReasoning
	}

	passthrough := false
	firstTextBytes := 0
	var firstAssistant strings.Builder

	for event := range events {
		if event.Err != nil {
			if proc.handleEvent(event, true, deltaTextDrop) {
				return outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start
			}
			break
		}
		if passthrough {
			if proc.handleEvent(event, true, deltaTextReasoning) {
				return outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start
			}
		} else if streamEventHasToolCall(event) {
			passthrough = true
			if proc.handleEvent(event, true, deltaTextReasoning) {
				return outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start
			}
		} else {
			if event.Delta != "" {
				firstTextBytes += len(event.Delta)
				firstAssistant.WriteString(event.Delta)
			}
			if event.ReasoningDelta != "" {
				firstTextBytes += len(event.ReasoningDelta)
				firstAssistant.WriteString(event.ReasoningDelta)
			}
			if proc.handleEvent(event, true, textMode) {
				return outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start
			}
		}
		if event.Done {
			break
		}
	}

	if passthrough {
		return finishStream()
	}

	firstToolCallCount := *proc.nextToolCallIndex
	if outcome == "completed" && firstTextBytes > 0 && retryEnabled && shouldRetryDegenerateTurn(true, toolsPresent, req.Messages, firstToolCallCount, firstAssistant.String(), firstTextBytes) {
		loopPhrase := detectLoopPhrase(firstAssistant.String())
		logDegenerateTurn(opts, streamID, firstTextBytes, firstToolCallCount, loopPhrase, len(req.Messages))
		logDegenerateTurnRetry(opts, streamID, 1, 0)

		proc.resetAttemptStats()
		retryEvents, err := service.Stream(ctx, buildDegenerateRetryRequest(req))
		if err != nil {
			logLine(opts, "degenerate_turn_retry_error request_id=%s err=%s\n", streamID, detailedError(err))
			return finishStream()
		}
		if proc.streamEvents(retryEvents, deltaTextReasoning) {
			logDegenerateTurnRetry(opts, streamID, 1, *proc.nextToolCallIndex)
		}
	}

	return finishStream()
}
