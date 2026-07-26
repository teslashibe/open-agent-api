package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/teslashibe/open-chat-api/internal/codex"
)

// toolBridgeParser converts Claude's prompt-injected tool protocol
// (fenced cursor_tool_call blocks) into OpenAI-style tool call deltas.
//
// It processes both the content and reasoning channels so a tool fence can
// never leak to the client as plain text or reasoning, and it supports
// multiple tool calls per turn by assigning each an incrementing index.
type toolBridgeParser struct {
	specsByName   map[string]toolSpec
	content       fenceScanner
	reasoning     fenceScanner
	contentLine   lineToolScanner
	reasoningLine lineToolScanner
	toolIndex     int
}

func newToolBridgeParser(specs []toolSpec) *toolBridgeParser {
	return &toolBridgeParser{specsByName: toolSpecByName(specs)}
}

func (p *toolBridgeParser) consume(event codex.StreamEvent) []codex.StreamEvent {
	if len(p.specsByName) == 0 {
		return []codex.StreamEvent{event}
	}

	hasText := event.Delta != "" || event.ReasoningDelta != ""
	var out []codex.StreamEvent

	if event.Delta != "" {
		safe, bodies := p.content.push(event.Delta)
		text, bare := p.contentLine.push(safe, p.isBareToolLine)
		if text != "" {
			out = append(out, codex.StreamEvent{Delta: text})
		}
		out = append(out, p.toolEvents(bodies)...)
		out = append(out, p.toolEvents(bare)...)
	}
	if event.ReasoningDelta != "" {
		safe, bodies := p.reasoning.push(event.ReasoningDelta)
		text, bare := p.reasoningLine.push(safe, p.isBareToolLine)
		if text != "" {
			out = append(out, codex.StreamEvent{ReasoningDelta: text})
		}
		out = append(out, p.toolEvents(bodies)...)
		out = append(out, p.toolEvents(bare)...)
	}

	// Non-text signals (Done/Usage/Model/ID/Err) pass through untouched. On
	// such an event, flush any partial buffer that never became a fence so no
	// text is silently dropped at end of stream.
	if !hasText {
		out = append(out, p.flush()...)
		out = append(out, event)
	}
	return out
}

func (p *toolBridgeParser) flush() []codex.StreamEvent {
	var out []codex.StreamEvent
	text, bare := p.contentLine.finish(p.content.drain(), p.isBareToolLine)
	if text != "" {
		out = append(out, codex.StreamEvent{Delta: text})
	}
	out = append(out, p.toolEvents(bare)...)
	text, bare = p.reasoningLine.finish(p.reasoning.drain(), p.isBareToolLine)
	if text != "" {
		out = append(out, codex.StreamEvent{ReasoningDelta: text})
	}
	out = append(out, p.toolEvents(bare)...)
	return out
}

// isBareToolLine reports whether a complete text line is an unfenced tool
// call. Models drift from the fenced protocol and emit the JSON directly;
// requiring a registered tool name keeps ordinary JSON prose intact.
func (p *toolBridgeParser) isBareToolLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return false
	}
	call, ok := parseToolCallBody(trimmed)
	if !ok || call.Name == "" {
		return false
	}
	_, known := p.specsByName[call.Name]
	return known
}

func (p *toolBridgeParser) toolEvents(bodies []string) []codex.StreamEvent {
	var out []codex.StreamEvent
	for _, body := range bodies {
		if delta := p.toolCall(body); delta != nil {
			out = append(out, codex.StreamEvent{ToolCallDelta: delta})
		}
	}
	return out
}

func (p *toolBridgeParser) toolCall(body string) *codex.ToolCallDelta {
	call, ok := parseToolCallBody(body)
	if !ok {
		return nil
	}
	spec := p.specsByName[call.Name]
	args := "{}"
	if len(call.Arguments) > 0 && json.Valid(call.Arguments) {
		args = string(call.Arguments)
	}
	if spec.Type == "custom" {
		args = call.Input
		if args == "" && len(call.Arguments) > 0 {
			args = string(call.Arguments)
		}
	}
	index := p.toolIndex
	p.toolIndex++
	return &codex.ToolCallDelta{
		Index: index,
		ID:    fmt.Sprintf("call_claude_%x", stableHash(fmt.Sprintf("%d:%s:%s", index, call.Name, args))),
		Type:  defaultString(spec.Type, "function"),
		Function: codex.ToolCallFunctionDelta{
			Name:      call.Name,
			Arguments: args,
		},
		Final: true,
	}
}

// fenceScanner incrementally scans a text channel for cursor_tool_call fences.
// It emits text that cannot be part of a fence and returns the JSON bodies of
// any completed fences, holding back partial fences until they resolve.
type fenceScanner struct {
	buf strings.Builder
}

func (s *fenceScanner) push(text string) (safe string, bodies []string) {
	s.buf.WriteString(text)
	working := s.buf.String()
	var out strings.Builder
	for {
		idx := strings.Index(working, toolCallFence)
		if idx < 0 {
			keep := partialFenceSuffixLen(working)
			out.WriteString(working[:len(working)-keep])
			working = working[len(working)-keep:]
			break
		}
		out.WriteString(working[:idx])
		rest := working[idx+len(toolCallFence):]
		rest = strings.TrimPrefix(rest, "\n")
		end := strings.Index(rest, "```")
		if end < 0 {
			// Incomplete fence: hold everything from the fence onward.
			working = working[idx:]
			break
		}
		bodies = append(bodies, strings.TrimSpace(rest[:end]))
		working = rest[end+3:]
	}
	s.buf.Reset()
	s.buf.WriteString(working)
	return out.String(), bodies
}

// drain returns any buffered text that never resolved into a fence.
func (s *fenceScanner) drain() string {
	text := s.buf.String()
	s.buf.Reset()
	if strings.Contains(text, toolCallFence) {
		// An unterminated fence: drop it rather than leak the protocol.
		return ""
	}
	return text
}

// partialFenceSuffixLen returns the length of the longest suffix of s that is a
// proper prefix of the tool call fence, so we never emit a partial fence.
func partialFenceSuffixLen(s string) int {
	max := len(toolCallFence) - 1
	if max > len(s) {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasPrefix(toolCallFence, s[len(s)-n:]) {
			return n
		}
	}
	return 0
}

// lineToolScanner buffers streamed text into complete lines so unfenced tool
// call JSON can be intercepted. Only lines that might be a bare tool call
// (start with "{") are held back; everything else flows through immediately,
// preserving token-level streaming for prose.
type lineToolScanner struct {
	buf strings.Builder
}

func (s *lineToolScanner) push(text string, isToolLine func(string) bool) (string, []string) {
	if text == "" {
		return "", nil
	}
	s.buf.WriteString(text)
	working := s.buf.String()
	var out strings.Builder
	var bodies []string
	for {
		nl := strings.IndexByte(working, '\n')
		if nl < 0 {
			break
		}
		line := working[:nl]
		if isToolLine(line) {
			bodies = append(bodies, strings.TrimSpace(line))
		} else {
			out.WriteString(line)
			out.WriteByte('\n')
		}
		working = working[nl+1:]
	}
	if !couldBeToolLine(working) {
		out.WriteString(working)
		working = ""
	}
	s.buf.Reset()
	s.buf.WriteString(working)
	return out.String(), bodies
}

// finish processes trailing text plus whatever the scanner still holds.
func (s *lineToolScanner) finish(trailing string, isToolLine func(string) bool) (string, []string) {
	text, bodies := s.push(trailing, isToolLine)
	rest := s.buf.String()
	s.buf.Reset()
	if rest == "" {
		return text, bodies
	}
	if isToolLine(rest) {
		bodies = append(bodies, strings.TrimSpace(rest))
		return text, bodies
	}
	return text + rest, bodies
}

// couldBeToolLine reports whether a partial line might still become a bare
// tool call once complete. Oversized buffers are released to bound memory.
func couldBeToolLine(partial string) bool {
	trimmed := strings.TrimLeft(partial, " \t")
	return strings.HasPrefix(trimmed, "{") && len(partial) < 64*1024
}

func stableHash(value string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(value); i++ {
		h ^= uint32(value[i])
		h *= 16777619
	}
	return h
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
