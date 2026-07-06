package server

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/teslashibe/codex-chat-api/internal/config"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

type contextManagementResult struct {
	Messages       []openai.ChatMessage
	Before         contextShape
	After          contextShape
	Changed        bool
	TruncatedTools int
	CompactedTools int
}

type contextShape struct {
	Messages             int
	Bytes                int
	ToolOutputs          int
	OversizedToolOutputs int
}

func manageContext(messages []openai.ChatMessage, cfg config.Config) contextManagementResult {
	before := measureContext(messages, cfg.ContextToolOutputMaxBytes)
	result := contextManagementResult{
		Messages: messages,
		Before:   before,
		After:    before,
	}
	if !cfg.ContextManagementEnabled {
		return result
	}

	managed := cloneMessages(messages)
	for i := range managed {
		if managed[i].Role != "tool" {
			continue
		}
		text := openai.MessageText(managed[i].Content)
		if cfg.ContextToolOutputMaxBytes > 0 && len(text) > cfg.ContextToolOutputMaxBytes {
			managed[i].Content = openai.TextContent(truncatedToolOutput(text, cfg.ContextToolOutputMaxBytes))
			result.TruncatedTools++
		}
	}

	if exceedsContextLimit(measureContext(managed, cfg.ContextToolOutputMaxBytes), cfg) {
		protectStart := len(managed) - cfg.ContextRecentMessages
		if protectStart < 0 {
			protectStart = 0
		}
		for i := 0; i < protectStart; i++ {
			if managed[i].Role != "tool" || managed[i].ToolCallID == "" {
				continue
			}
			text := openai.MessageText(managed[i].Content)
			compacted := compactedToolOutput(text, cfg.ContextCompactedToolOutputMaxBytes)
			if compacted == text {
				continue
			}
			managed[i].Content = openai.TextContent(compacted)
			result.CompactedTools++
		}
	}

	after := measureContext(managed, cfg.ContextToolOutputMaxBytes)
	result.After = after
	result.Changed = result.TruncatedTools > 0 || result.CompactedTools > 0 || after.Bytes != before.Bytes
	if result.Changed {
		result.Messages = managed
	}
	return result
}

// hardContextConfig tightens the normal knobs for models with small context
// windows: shorter protected tail, harsher tool-output caps, hard byte budget.
func hardContextConfig(cfg config.Config, maxBytes int) config.Config {
	cfg.ContextManagementEnabled = true
	cfg.ContextMaxBytes = maxBytes
	if cfg.ContextRecentMessages > hardContextProtectRecent {
		cfg.ContextRecentMessages = hardContextProtectRecent
	}
	if cfg.ContextToolOutputMaxBytes == 0 || cfg.ContextToolOutputMaxBytes > 8*1024 {
		cfg.ContextToolOutputMaxBytes = 8 * 1024
	}
	if cfg.ContextCompactedToolOutputMaxBytes == 0 || cfg.ContextCompactedToolOutputMaxBytes > 256 {
		cfg.ContextCompactedToolOutputMaxBytes = 256
	}
	return cfg
}

const hardContextProtectRecent = 8

// dropOldestToFit removes the oldest turns (keeping a leading system message
// and the protected recent tail) until the conversation fits maxBytes. The
// kept window never starts on an orphaned tool result. Best effort: if even
// the protected tail exceeds the budget, it returns the tail and lets the
// upstream reject with a clear error.
func dropOldestToFit(messages []openai.ChatMessage, maxBytes int, protectRecent int) ([]openai.ChatMessage, int) {
	if maxBytes <= 0 || len(messages) == 0 {
		return messages, 0
	}
	sizeOf := func(msgs []openai.ChatMessage) int {
		data, _ := json.Marshal(msgs)
		return len(data)
	}
	if sizeOf(messages) <= maxBytes {
		return messages, 0
	}
	head := 0
	if messages[0].Role == "system" {
		head = 1
	}
	minKeepStart := len(messages) - protectRecent
	if minKeepStart < head {
		minKeepStart = head
	}
	cut := head
	for cut < minKeepStart {
		cut++
		for cut < minKeepStart && messages[cut].Role == "tool" {
			cut++
		}
		candidate := make([]openai.ChatMessage, 0, head+len(messages)-cut)
		candidate = append(candidate, messages[:head]...)
		candidate = append(candidate, messages[cut:]...)
		if sizeOf(candidate) <= maxBytes {
			return candidate, cut - head
		}
		if cut >= minKeepStart {
			return candidate, cut - head
		}
	}
	return messages, 0
}

func measureContext(messages []openai.ChatMessage, toolOutputMaxBytes int) contextShape {
	data, _ := json.Marshal(messages)
	shape := contextShape{
		Messages: len(messages),
		Bytes:    len(data),
	}
	for _, message := range messages {
		if message.Role != "tool" {
			continue
		}
		shape.ToolOutputs++
		if toolOutputMaxBytes > 0 && len(openai.MessageText(message.Content)) > toolOutputMaxBytes {
			shape.OversizedToolOutputs++
		}
	}
	return shape
}

func exceedsContextLimit(shape contextShape, cfg config.Config) bool {
	return (cfg.ContextMaxBytes > 0 && shape.Bytes > cfg.ContextMaxBytes) ||
		(cfg.ContextMaxMessages > 0 && shape.Messages > cfg.ContextMaxMessages)
}

func cloneMessages(messages []openai.ChatMessage) []openai.ChatMessage {
	out := make([]openai.ChatMessage, len(messages))
	copy(out, messages)
	for i := range out {
		if out[i].Content != nil {
			out[i].Content = append(json.RawMessage(nil), out[i].Content...)
		}
		if out[i].ToolCalls != nil {
			out[i].ToolCalls = append([]openai.ToolCall(nil), out[i].ToolCalls...)
		}
	}
	return out
}

func truncatedToolOutput(text string, maxBytes int) string {
	marker := fmt.Sprintf("[codex-chat-api: tool output truncated from %d bytes; kept %d bytes]", len(text), maxBytes)
	keptBytes := maxBytes - len("\n\n") - len(marker)
	if keptBytes < 0 {
		keptBytes = 0
	}
	kept := prefixBytes(text, keptBytes)
	return kept + "\n\n" + fmt.Sprintf("[codex-chat-api: tool output truncated from %d bytes; kept %d bytes]", len(text), len(kept))
}

func compactedToolOutput(text string, maxBytes int) string {
	kept := prefixBytes(text, maxBytes)
	if kept == text {
		return text
	}
	compacted := kept + "\n\n" + fmt.Sprintf("[codex-chat-api: older tool output compacted from %d bytes; kept %d bytes]", len(text), len(kept))
	if len(compacted) >= len(text) {
		return text
	}
	return compacted
}

func prefixBytes(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	for maxBytes > 0 && !utf8.ValidString(text[:maxBytes]) {
		maxBytes--
	}
	return text[:maxBytes]
}
