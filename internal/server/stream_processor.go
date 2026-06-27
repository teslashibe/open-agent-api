package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
	textBytes, toolArgChars            *int
	assistantText                      *strings.Builder

	toolCallEmitted     *bool
	toolCallIndexByKey  map[string]int
	nextToolCallIndex   *int
	toolCallsByKey      map[string]*streamedToolCall
	toolCallKeysByIndex map[int]string
}

type streamedToolCall struct {
	key       string
	index     int
	id        string
	typ       string
	name      string
	arguments string
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
		ctx:                 ctx,
		cancel:              cancel,
		w:                   w,
		opts:                opts,
		id:                  id,
		created:             created,
		model:               model,
		streamID:            streamID,
		outcome:             outcome,
		deltas:              deltas,
		toolDeltas:          toolDeltas,
		upstreamEvents:      upstreamEvents,
		textBytes:           textBytes,
		toolArgChars:        toolArgChars,
		assistantText:       assistantText,
		toolCallEmitted:     toolCallEmitted,
		toolCallIndexByKey:  map[string]int{},
		nextToolCallIndex:   new(int),
		toolCallsByKey:      map[string]*streamedToolCall{},
		toolCallKeysByIndex: map[int]string{},
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

func (p *streamProcessor) toolCallKey(upstreamIndex int) string {
	if key, ok := p.toolCallKeysByIndex[upstreamIndex]; ok {
		return key
	}
	key := "idx:" + strconv.Itoa(upstreamIndex)
	p.toolCallKeysByIndex[upstreamIndex] = key
	return key
}

func (p *streamProcessor) toolCallForIndex(upstreamIndex int) *streamedToolCall {
	key := p.toolCallKey(upstreamIndex)
	if toolCall, ok := p.toolCallsByKey[key]; ok {
		return toolCall
	}
	toolCall := &streamedToolCall{
		key:   key,
		index: p.normalizeToolCallIndex(key),
		typ:   "function",
	}
	p.toolCallsByKey[key] = toolCall
	return toolCall
}

func (p *streamProcessor) toolCallForFullToolCall(upstreamIndex int, id string) *streamedToolCall {
	if id != "" {
		for _, toolCall := range p.toolCallsByKey {
			if toolCall.id == id {
				p.toolCallKeysByIndex[upstreamIndex] = toolCall.key
				return toolCall
			}
		}
	}
	return p.toolCallForIndex(upstreamIndex)
}

func (p *streamProcessor) accumulateFullToolCall(upstreamIndex int, toolCall codex.ToolCall) {
	if toolCall.ID == "" && toolCall.Type == "" && toolCall.Function.Name == "" && toolCall.Function.Arguments == "" {
		return
	}
	accumulated := p.toolCallForFullToolCall(upstreamIndex, toolCall.ID)
	if toolCall.ID != "" {
		accumulated.id = toolCall.ID
	}
	accumulated.typ = defaultString(toolCall.Type, "function")
	if toolCall.Function.Name != "" {
		accumulated.name = toolCall.Function.Name
	}
	if toolCall.Function.Arguments != "" {
		accumulated.arguments = toolCall.Function.Arguments
	}
	*p.toolCallEmitted = true
	*p.toolDeltas++
	*p.toolArgChars += len(toolCall.Function.Arguments)
}

func (p *streamProcessor) accumulateToolCallDelta(delta codex.ToolCallDelta) {
	if delta.ID == "" && delta.Type == "" && delta.Function.Name == "" && delta.Function.Arguments == "" {
		return
	}
	accumulated := p.toolCallForIndex(delta.Index)
	if delta.ID != "" {
		accumulated.id = delta.ID
	}
	if delta.Type != "" {
		accumulated.typ = delta.Type
	}
	if delta.Function.Name != "" {
		accumulated.name = delta.Function.Name
	}
	if delta.Function.Arguments != "" {
		if delta.Final {
			accumulated.arguments = delta.Function.Arguments
		} else {
			accumulated.arguments += delta.Function.Arguments
		}
	}
	*p.toolCallEmitted = true
	*p.toolDeltas++
	*p.toolArgChars += len(delta.Function.Arguments)
}

func (p *streamProcessor) orderedToolCalls() []*streamedToolCall {
	out := make([]*streamedToolCall, 0, len(p.toolCallsByKey))
	for _, toolCall := range p.toolCallsByKey {
		out = append(out, toolCall)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].index > out[j].index; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func (p *streamProcessor) validateToolCall(toolCall *streamedToolCall) error {
	if toolCall.id == "" {
		toolCall.id = fmt.Sprintf("call_%s_%d", safeIdentifier(p.streamID), toolCall.index)
	}
	toolCall.typ = defaultString(toolCall.typ, "function")
	if toolCall.name == "" {
		return fmt.Errorf("missing function name")
	}
	if strings.TrimSpace(toolCall.arguments) == "" {
		toolCall.arguments = "{}"
	}
	if !json.Valid([]byte(toolCall.arguments)) {
		return fmt.Errorf("invalid function arguments JSON")
	}
	return nil
}

func safeIdentifier(value string) string {
	if value == "" {
		return "stream"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "stream"
	}
	return b.String()
}

func (p *streamProcessor) writeToolCalls() bool {
	for _, toolCall := range p.orderedToolCalls() {
		if err := p.validateToolCall(toolCall); err != nil {
			logLine(p.opts, "tool_call_validation_error id=%s index=%d err=%s\n", p.streamID, toolCall.index, err)
			_ = writeSSE(p.ctx, p.cancel, p.w, errorChunk(p.id, p.created, *p.model, "invalid upstream tool call"))
			*p.outcome = "upstream_error"
			return false
		}
		delta := openai.ToolCallDelta{
			Index: toolCall.index,
			ID:    toolCall.id,
			Type:  toolCall.typ,
			Function: &openai.ToolCallFunctionDelta{
				Name:      toolCall.name,
				Arguments: toolCall.arguments,
			},
		}
		if !writeSSE(p.ctx, p.cancel, p.w, openai.ChatCompletionChunk{
			ID:      p.id,
			Object:  "chat.completion.chunk",
			Created: p.created,
			Model:   *p.model,
			Choices: []openai.ChatCompletionChunkChoice{
				{Index: 0, Delta: openai.ChatDelta{ToolCalls: []openai.ToolCallDelta{delta}}},
			},
		}) {
			*p.outcome = "client_disconnect"
			return false
		}
	}
	return true
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
		p.accumulateFullToolCall(i, toolCall)
	}
	if event.ToolCallDelta != nil {
		p.accumulateToolCallDelta(*event.ToolCallDelta)
	}
	return false
}

func (p *streamProcessor) writeFinish() bool {
	finish := "stop"
	if *p.toolCallEmitted {
		finish = "tool_calls"
		if !p.writeToolCalls() {
			return *p.outcome == "upstream_error"
		}
		if *p.outcome != "completed" {
			return true
		}
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
	p.toolCallIndexByKey = map[string]int{}
	p.toolCallsByKey = map[string]*streamedToolCall{}
	p.toolCallKeysByIndex = map[int]string{}
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
	var preToolText strings.Builder
	bufferPreToolText := textMode == deltaTextReasoning

	flushPreToolText := func(mode deltaTextMode) bool {
		if preToolText.Len() == 0 {
			return true
		}
		if !proc.writeTextDelta(preToolText.String(), mode) {
			return false
		}
		preToolText.Reset()
		return true
	}

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
			if bufferPreToolText && !flushPreToolText(deltaTextReasoning) {
				return outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start
			}
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
			switch {
			case bufferPreToolText && event.ReasoningDelta != "":
				if proc.handleEvent(event, true, deltaTextReasoning) {
					return outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start
				}
			case bufferPreToolText && event.Delta != "":
				if proc.handleEvent(event, false, deltaTextDrop) {
					return outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start
				}
				preToolText.WriteString(event.Delta)
			default:
				if proc.handleEvent(event, true, textMode) {
					return outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start
				}
			}
		}
		if event.Done {
			break
		}
	}

	if passthrough {
		return finishStream()
	}

	if bufferPreToolText && !flushPreToolText(deltaTextContent) {
		return outcome, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start
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
