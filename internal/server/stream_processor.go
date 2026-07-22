package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/openai"
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
	terminal *streamTerminal

	deltas, toolDeltas, upstreamEvents *int
	textBytes, toolArgChars            *int
	assistantText                      *strings.Builder

	toolCallEmitted     *bool
	toolCallIndexByKey  map[string]int
	nextToolCallIndex   *int
	toolCallsByKey      map[string]*streamedToolCall
	toolCallKeysByIndex map[int]string
	toolCallKeyByID     map[string]string
	upstreamStart       time.Time
	firstDeltaLatency   *time.Duration
	observedEvents      int
	deliveredContent    bool
	unexpectedEnd       bool
	responseShape       streamResponseShape
}

type streamedToolCall struct {
	key              string
	index            int
	id               string
	typ              string
	name             string
	arguments        string
	thoughtSignature string
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
	terminal *streamTerminal,
	deltas, toolDeltas, upstreamEvents *int,
	textBytes, toolArgChars *int,
	assistantText *strings.Builder,
	toolCallEmitted *bool,
	upstreamStart time.Time,
	firstDeltaLatency *time.Duration,
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
		terminal:            terminal,
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
		toolCallKeyByID:     map[string]string{},
		upstreamStart:       upstreamStart,
		firstDeltaLatency:   firstDeltaLatency,
	}
}

func (p *streamProcessor) markFirstDelta() {
	if p.firstDeltaLatency == nil || *p.firstDeltaLatency >= 0 {
		return
	}
	*p.firstDeltaLatency = p.opts.now().Sub(p.upstreamStart)
}

// emittedContent reports whether any text delta or tool call has already been
// forwarded to the client, which makes the stream unsafe to rotate mid-flight.
func (p *streamProcessor) emittedContent() bool {
	return p.deliveredContent
}

// phaseEvents is cumulative across degenerate retries. The public stream
// counters are intentionally reset per attempt, but terminal phase telemetry
// must describe the whole downstream request.
func (p *streamProcessor) phaseEvents() int {
	return p.observedEvents
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

func (p *streamProcessor) toolCallForKey(key string) *streamedToolCall {
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

func (p *streamProcessor) toolCallForIndex(upstreamIndex int) *streamedToolCall {
	return p.toolCallForKey(p.toolCallKey(upstreamIndex))
}

func (p *streamProcessor) bindToolCallID(upstreamIndex int, id string) *streamedToolCall {
	idxKey := p.toolCallKey(upstreamIndex)
	if id == "" {
		return p.toolCallForKey(idxKey)
	}
	if key, ok := p.toolCallKeyByID[id]; ok {
		toolCall := p.toolCallForKey(key)
		p.toolCallKeysByIndex[upstreamIndex] = key
		return toolCall
	}
	idxToolCall, hasIdxToolCall := p.toolCallsByKey[idxKey]
	idKey := "id:" + id
	if hasIdxToolCall {
		delete(p.toolCallsByKey, idxKey)
		idxToolCall.key = idKey
		p.toolCallsByKey[idKey] = idxToolCall
		p.toolCallIndexByKey[idKey] = idxToolCall.index
		delete(p.toolCallIndexByKey, idxKey)
		p.toolCallKeysByIndex[upstreamIndex] = idKey
		p.toolCallKeyByID[id] = idKey
		return idxToolCall
	}
	p.toolCallKeysByIndex[upstreamIndex] = idKey
	p.toolCallKeyByID[id] = idKey
	return p.toolCallForKey(idKey)
}

func (p *streamProcessor) toolCallForFullToolCall(upstreamIndex int, id string) *streamedToolCall {
	return p.bindToolCallID(upstreamIndex, id)
}

func (p *streamProcessor) accumulateFullToolCall(upstreamIndex int, toolCall codex.ToolCall) {
	if toolCall.ID == "" && toolCall.Type == "" && toolCall.Function.Name == "" && toolCall.Function.Arguments == "" && toolCall.ThoughtSignature == "" {
		return
	}
	p.responseShape.ToolCalls = true
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
	if toolCall.ThoughtSignature != "" {
		accumulated.thoughtSignature = toolCall.ThoughtSignature
	}
	*p.toolCallEmitted = true
	*p.toolDeltas++
	*p.toolArgChars += len(toolCall.Function.Arguments)
}

func (p *streamProcessor) accumulateToolCallDelta(delta codex.ToolCallDelta) {
	if delta.ID == "" && delta.Type == "" && delta.Function.Name == "" && delta.Function.Arguments == "" && delta.ThoughtSignature == "" {
		return
	}
	p.responseShape.ToolCalls = true
	accumulated := p.toolCallForIndex(delta.Index)
	if delta.ID != "" {
		accumulated = p.bindToolCallID(delta.Index, delta.ID)
		accumulated.id = delta.ID
	}
	if delta.Type != "" {
		accumulated.typ = delta.Type
	}
	if delta.ThoughtSignature != "" {
		accumulated.thoughtSignature = delta.ThoughtSignature
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
	// Custom (freeform) tool calls carry raw text input, not JSON arguments.
	if toolCall.typ == "custom" {
		return nil
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

func (p *streamProcessor) validToolCalls() ([]*streamedToolCall, []error) {
	var valid []*streamedToolCall
	var invalid []error
	for _, toolCall := range p.orderedToolCalls() {
		if err := p.validateToolCall(toolCall); err != nil {
			invalid = append(invalid, fmt.Errorf("index=%d err=%w", toolCall.index, err))
			continue
		}
		valid = append(valid, toolCall)
	}
	return valid, invalid
}

func (p *streamProcessor) writeToolCalls() bool {
	valid, invalid := p.validToolCalls()
	for _, err := range invalid {
		logLine(p.opts, "tool_call_validation_error id=%s %s\n", p.streamID, err)
	}
	if len(valid) == 0 && len(invalid) > 0 {
		_ = writeSSE(p.ctx, p.cancel, p.w, errorChunk(p.id, p.created, *p.model, "invalid upstream tool call"))
		*p.terminal = upstreamErrorStreamTerminal(invalid[0], p.phaseEvents(), p.emittedContent())
		return false
	}
	for i, toolCall := range valid {
		if p.opts.logBodyShape {
			logLine(p.opts, "tool_call_emit id=%s index=%d type=%s name=%s args_len=%d args_head=%q\n",
				p.streamID, i, toolCall.typ, toolCall.name, len(toolCall.arguments), truncateForLog(toolCall.arguments, 160))
		}
		delta := openai.ToolCallDelta{
			Index:        i,
			ID:           toolCall.id,
			Type:         toolCall.typ,
			ExtraContent: openai.GoogleThoughtSignatureExtra(toolCall.thoughtSignature),
		}
		if toolCall.typ == "custom" && p.opts.contextConfig.CustomToolWire == "function" {
			// Cursor's BYOK chat-completions parser drops type:"custom" tool
			// calls; downgrade to function shape with the freeform input as
			// the arguments string.
			delta.Type = "function"
			delta.Function = &openai.ToolCallFunctionDelta{
				Name:      toolCall.name,
				Arguments: toolCall.arguments,
			}
		} else if toolCall.typ == "custom" {
			delta.Custom = &openai.ToolCallCustom{
				Name:  toolCall.name,
				Input: toolCall.arguments,
			}
		} else {
			delta.Function = &openai.ToolCallFunctionDelta{
				Name:      toolCall.name,
				Arguments: toolCall.arguments,
			}
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
			*p.terminal = canceledStreamTerminal(p.phaseEvents(), p.emittedContent())
			return false
		}
		p.markFirstDelta()
		p.deliveredContent = true
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
		p.responseShape.Content = true
	case deltaTextReasoning:
		delta.ReasoningContent = text
		p.responseShape.ReasoningContent = true
	default:
		return true
	}
	if !writeSSE(p.ctx, p.cancel, p.w, openai.ChatCompletionChunk{
		ID:      p.id,
		Object:  "chat.completion.chunk",
		Created: p.created,
		Model:   *p.model,
		Choices: []openai.ChatCompletionChunkChoice{
			{Index: 0, Delta: delta},
		},
	}) {
		*p.terminal = canceledStreamTerminal(p.phaseEvents(), p.emittedContent())
		return false
	}
	p.markFirstDelta()
	*p.deltas++
	p.deliveredContent = true
	return true
}

func (p *streamProcessor) handleEvent(event codex.StreamEvent, write bool, textMode deltaTextMode) (stop bool) {
	*p.upstreamEvents++
	p.observedEvents++
	if event.Err != nil {
		terminal := upstreamErrorStreamTerminal(event.Err, p.phaseEvents(), p.emittedContent())
		if write {
			logLine(p.opts, "stream_error id=%s model=%s err=%s failure_class=%s failure_phase=%s\n", p.streamID, defaultString(event.Model, *p.model), detailedError(event.Err), terminal.failureClass, terminal.phase)
			_ = writeSSE(p.ctx, p.cancel, p.w, errorChunk(p.id, p.created, defaultString(event.Model, *p.model), publicErrorMessage(event.Err)))
		}
		*p.terminal = terminal
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

// handleUnexpectedEnd converts an upstream channel close without a Done event
// into the same bounded terminal error used for explicit upstream failures.
// Count the missing terminal event as the failing event so a stream that
// connected but never produced content is classified as first_event rather
// than connect.
func (p *streamProcessor) handleUnexpectedEnd(write bool) {
	p.unexpectedEnd = true
	err := codex.NewError(
		codex.ErrorKindUpstream,
		502,
		"upstream stream ended without a terminal event",
		io.ErrUnexpectedEOF,
	)
	_ = p.handleEvent(codex.StreamEvent{Err: err}, write, deltaTextDrop)
}

func (p *streamProcessor) writeFinish() bool {
	finish := "stop"
	if *p.toolCallEmitted {
		finish = "tool_calls"
		if !p.writeToolCalls() {
			return p.terminal.outcome == streamOutcomeUpstreamError
		}
		if p.terminal.outcome != streamOutcomeCompleted {
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
		*p.terminal = canceledStreamTerminal(p.phaseEvents(), p.emittedContent())
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
	p.toolCallKeyByID = map[string]string{}
	*p.terminal = pendingStreamTerminal()
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
	for {
		event, ok := recvStreamEvent(p.ctx, p.cancel, events)
		if !ok {
			if p.ctx.Err() != nil {
				*p.terminal = canceledStreamTerminal(p.phaseEvents(), p.emittedContent())
				return false
			}
			p.handleUnexpectedEnd(false)
			return false
		}
		if p.handleEvent(event, true, textMode) {
			return false
		}
		if event.Done {
			return true
		}
	}
}

func recvStreamEvent(ctx context.Context, cancel context.CancelFunc, events <-chan codex.StreamEvent) (codex.StreamEvent, bool) {
	select {
	case <-ctx.Done():
		cancel()
		return codex.StreamEvent{}, false
	case event, ok := <-events:
		if !ok {
			return codex.StreamEvent{}, false
		}
		return event, true
	}
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
	upstreamStart time.Time,
) (terminal streamTerminal, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, toolCallCount int, assistantText string, start time.Time, firstDeltaLatency time.Duration) {
	terminal = pendingStreamTerminal()
	firstDeltaLatency = -1
	var assistant strings.Builder
	proc := newStreamProcessor(
		ctx, cancel, w, opts, id, created, &model, streamID, &terminal,
		&deltas, &toolDeltas, &upstreamEvents, &textBytes, &toolArgChars, &assistant, new(bool),
		upstreamStart, &firstDeltaLatency,
	)
	start = opts.now()
	events, req = applyQuotaFallback(ctx, opts, service, req, events, streamID)
	toolsPresent := rawJSONPresent(req.Tools)
	agentTurn := agentTurnExpectsToolCalls(req.Messages, toolsPresent)
	retryEnabled := opts.contextConfig.DegenerateTurnRetryEnabled && toolsPresent

	streamResult := func() (streamTerminal, int, int, int, int, int, int, string, time.Time, time.Duration) {
		return terminal, deltas, toolDeltas, upstreamEvents, textBytes, toolArgChars, *proc.nextToolCallIndex, assistant.String(), start, firstDeltaLatency
	}

	finishStream := func() (streamTerminal, int, int, int, int, int, int, string, time.Time, time.Duration) {
		if opts.logBodyShape {
			logLine(opts, "stream_response_shape request_id=%s %s\n", streamID, proc.responseShape.logFields())
		}
		if terminal.outcome == streamOutcomeCompleted {
			if !proc.writeFinish() {
				return streamResult()
			}
		} else if proc.unexpectedEnd {
			// Before terminal telemetry was corrected, an ordinary upstream EOF
			// was presented to clients as a normal finish chunk plus [DONE]. Keep
			// that wire contract while retaining the upstream-error observation.
			cause := terminal
			terminal = pendingStreamTerminal()
			if !proc.writeFinish() {
				terminal = cause
				return streamResult()
			}
			terminal = cause
		}
		switch terminal.outcome {
		case streamOutcomeCompleted:
			if writeSSEDone(ctx, cancel, w) {
				terminal = successfulStreamTerminal()
			} else {
				terminal = canceledStreamTerminal(proc.phaseEvents(), proc.emittedContent())
			}
		case streamOutcomeUpstreamError:
			// Preserve the upstream-error result if downstream delivery also fails;
			// the causal terminal error takes precedence over the secondary write.
			_ = writeSSEDone(ctx, cancel, w)
		}
		return streamResult()
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

	for {
		event, ok := recvStreamEvent(ctx, cancel, events)
		if !ok {
			if ctx.Err() != nil {
				terminal = canceledStreamTerminal(proc.phaseEvents(), proc.emittedContent())
				return streamResult()
			}
			proc.handleUnexpectedEnd(false)
			return finishStream()
		}
		if event.Err != nil {
			if proc.handleEvent(event, true, deltaTextDrop) {
				return streamResult()
			}
			break
		}
		if passthrough {
			if proc.handleEvent(event, true, deltaTextReasoning) {
				return streamResult()
			}
		} else if streamEventHasToolCall(event) {
			if bufferPreToolText && !flushPreToolText(deltaTextReasoning) {
				return streamResult()
			}
			passthrough = true
			if proc.handleEvent(event, true, deltaTextReasoning) {
				return streamResult()
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
					return streamResult()
				}
			case bufferPreToolText && event.Delta != "":
				if proc.handleEvent(event, false, deltaTextDrop) {
					return streamResult()
				}
				preToolText.WriteString(event.Delta)
			default:
				if proc.handleEvent(event, true, textMode) {
					return streamResult()
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
		return streamResult()
	}

	firstToolCallCount := *proc.nextToolCallIndex
	if terminal.outcome == streamOutcomeCompleted && firstTextBytes > 0 && retryEnabled && shouldRetryDegenerateTurn(true, toolsPresent, req.Messages, firstToolCallCount, firstAssistant.String(), firstTextBytes) {
		loopPhrase := detectLoopPhrase(firstAssistant.String())
		logDegenerateTurn(opts, streamID, firstTextBytes, firstToolCallCount, loopPhrase, len(req.Messages))
		logDegenerateTurnRetry(opts, streamID, 1, 0)

		proc.resetAttemptStats()
		retryReq := buildDegenerateRetryRequest(req)
		// This retry happens after SSE headers and response chunks have been
		// written, so pool saturation must fail immediately.
		retryReq.DisablePoolWait = true
		retryEvents, err := service.Stream(ctx, retryReq)
		if err != nil {
			logLine(opts, "degenerate_turn_retry_error request_id=%s err=%s\n", streamID, detailedError(err))
			return finishStream()
		}
		retryEvents = withStreamIdleTimeout(ctx, retryEvents, opts.contextConfig.StreamIdleTimeout)
		if proc.streamEvents(retryEvents, deltaTextReasoning) {
			logDegenerateTurnRetry(opts, streamID, 1, *proc.nextToolCallIndex)
		}
	}

	return finishStream()
}

func truncateForLog(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
