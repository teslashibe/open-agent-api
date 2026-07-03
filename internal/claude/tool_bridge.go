package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/teslashibe/codex-chat-api/internal/codex"
)

type toolBridgeParser struct {
	specsByName map[string]toolSpec
	buffer      strings.Builder
	detected    bool
	emitted     bool
}

func newToolBridgeParser(specs []toolSpec) *toolBridgeParser {
	return &toolBridgeParser{specsByName: toolSpecByName(specs)}
}

func (p *toolBridgeParser) consume(event codex.StreamEvent) []codex.StreamEvent {
	if len(p.specsByName) == 0 || p.emitted || event.Delta == "" {
		return []codex.StreamEvent{event}
	}
	p.buffer.WriteString(event.Delta)
	text := p.buffer.String()
	if !p.detected {
		idx := strings.Index(text, toolCallFence)
		if idx < 0 {
			if p.buffer.Len() > len(toolCallFence) {
				p.buffer.Reset()
			}
			return []codex.StreamEvent{event}
		}
		p.detected = true
	}
	call, ok := extractCursorToolCall(text)
	if !ok {
		return nil
	}
	p.emitted = true
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
	return []codex.StreamEvent{{ToolCallDelta: &codex.ToolCallDelta{
		Index: 0,
		ID:    fmt.Sprintf("call_claude_%x", stableHash(call.Name+args)),
		Type:  defaultString(spec.Type, "function"),
		Function: codex.ToolCallFunctionDelta{
			Name:      call.Name,
			Arguments: args,
		},
		Final: true,
	}}}
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
