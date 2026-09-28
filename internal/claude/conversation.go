package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/teslashibe/open-agent-api/internal/claude/mcpbridge"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

// block is one Anthropic content block (text, image, tool_use, tool_result).
type block = map[string]any

type turn struct {
	Role    string
	Content []block
}

// conversation is an OpenAI chat request rebuilt as Anthropic messages.
//
// History is replayed to Claude Code as a resumed transcript and Tail is sent
// on stdin. Tail is either the final user turn, or the final assistant
// tool_use turn plus the user turn holding its tool_result blocks: the CLI
// starts one model turn per stdin user line, and an unanswered tool_use at
// the end of a resumed transcript gets an "interrupted" result injected.
type conversation struct {
	System  string
	History []turn
	Tail    []turn
}

var toolUseIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// normalizeToolUseID maps a client tool-call ID onto the Anthropic tool_use
// ID charset. Both sides of a call/result pair go through it, so pairs match.
func normalizeToolUseID(id string) string {
	if toolUseIDPattern.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return "call_" + hex.EncodeToString(sum[:12])
}

func buildConversation(messages []openai.ChatMessage, tools *toolSet) conversation {
	var system []string
	var turns []turn
	add := func(role string, blocks []block) {
		if len(blocks) == 0 {
			return
		}
		if n := len(turns); n > 0 && turns[n-1].Role == role {
			turns[n-1].Content = append(turns[n-1].Content, blocks...)
			return
		}
		turns = append(turns, turn{Role: role, Content: blocks})
	}
	for _, msg := range messages {
		switch msg.Role {
		case "system", "developer":
			if text := strings.TrimSpace(openai.MessageText(msg.Content)); text != "" {
				system = append(system, text)
			}
		case "assistant":
			blocks := contentBlocks(msg.Content, "assistant", tools)
			for _, call := range msg.ToolCalls {
				blocks = append(blocks, toolUseBlock(call, tools))
			}
			add("assistant", blocks)
		case "tool":
			add("user", []block{toolMessageResult(msg, tools)})
		default:
			add("user", contentBlocks(msg.Content, "user", tools))
		}
	}

	turns = repairToolPairs(turns)
	if len(turns) == 0 {
		turns = []turn{{Role: "user", Content: []block{textBlock("Continue.")}}}
	}
	if turns[0].Role != "user" {
		turns = append([]turn{{Role: "user", Content: []block{textBlock("(conversation continues)")}}}, turns...)
	}
	if turns[len(turns)-1].Role == "assistant" {
		turns = append(turns, turn{Role: "user", Content: []block{textBlock("Continue.")}})
	}

	conv := conversation{System: strings.Join(system, "\n\n")}
	n := len(turns)
	if n >= 2 && hasBlockType(turns[n-1], "tool_result") && hasBlockType(turns[n-2], "tool_use") {
		conv.History = turns[:n-2]
		conv.Tail = turns[n-2:]
	} else {
		conv.History = turns[:n-1]
		conv.Tail = turns[n-1:]
	}
	return conv
}

// repairToolPairs enforces the Messages API tool invariants that OpenAI
// histories do not guarantee (compaction, interrupted turns, legacy IDs):
//   - every tool_use has a tool_result in the next user turn (synthesized
//     as an error result when missing);
//   - tool_result blocks answer only the immediately preceding tool_use
//     blocks and come first in their turn (orphans become text);
//   - tool_use IDs are unique across the conversation (repeats are renamed
//     together with their result).
func repairToolPairs(turns []turn) []turn {
	seen := map[string]int{}
	out := make([]turn, 0, len(turns)+1)
	var pending []string
	renames := map[string]string{}

	closePending := func(existing *turn) {
		if len(pending) == 0 {
			return
		}
		var results, rest []block
		answered := map[string]bool{}
		if existing != nil {
			for _, b := range existing.Content {
				if b["type"] != "tool_result" {
					rest = append(rest, b)
					continue
				}
				id, _ := b["tool_use_id"].(string)
				if renamed, ok := renames[id]; ok {
					id = renamed
					b["tool_use_id"] = id
				}
				if !contains(pending, id) || answered[id] {
					rest = append(rest, orphanResultText(b))
					continue
				}
				answered[id] = true
				results = append(results, b)
			}
		}
		for _, id := range pending {
			if !answered[id] {
				results = append(results, toolResultBlock(id, "(tool result unavailable)", true))
			}
		}
		pending = nil
		renames = map[string]string{}
		merged := append(results, rest...)
		if existing != nil {
			existing.Content = merged
			return
		}
		out = append(out, turn{Role: "user", Content: merged})
	}

	for _, t := range turns {
		if t.Role == "user" {
			if len(pending) > 0 {
				closePending(&t)
			} else {
				for i, b := range t.Content {
					if b["type"] == "tool_result" {
						t.Content[i] = orphanResultText(b)
					}
				}
			}
			if len(t.Content) > 0 {
				out = append(out, t)
			}
			continue
		}
		// assistant
		if len(pending) > 0 {
			// Two assistant turns in a row cannot happen after role merging,
			// but guard anyway: answer the earlier calls first.
			closePending(nil)
		}
		for _, b := range t.Content {
			if b["type"] != "tool_use" {
				continue
			}
			original, _ := b["id"].(string)
			id := original
			if n := seen[original]; n > 0 {
				id = fmt.Sprintf("%s_%d", original, n)
				renames[original] = id
				b["id"] = id
			}
			seen[original]++
			pending = append(pending, id)
		}
		out = append(out, t)
	}
	if len(pending) > 0 {
		closePending(nil)
	}
	return out
}

// toolMessageResult converts an OpenAI tool message, keeping image parts
// (the server normalizes Anthropic image tool results into parts arrays).
func toolMessageResult(msg openai.ChatMessage, tools *toolSet) block {
	if len(msg.Content) > 0 && msg.Content[0] == '[' {
		parts := contentBlocks(msg.Content, "user", tools)
		if hasBlockType(turn{Content: parts}, "image") {
			content := make([]any, 0, len(parts))
			for _, p := range parts {
				content = append(content, p)
			}
			return block{"type": "tool_result", "tool_use_id": normalizeToolUseID(msg.ToolCallID), "content": content}
		}
	}
	return toolResultBlock(msg.ToolCallID, openai.MessageText(msg.Content), false)
}

func orphanResultText(b block) block {
	id, _ := b["tool_use_id"].(string)
	content, _ := b["content"].(string)
	return textBlock(fmt.Sprintf("Tool result (%s):\n%s", id, content))
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func hasBlockType(t turn, kind string) bool {
	for _, b := range t.Content {
		if b["type"] == kind {
			return true
		}
	}
	return false
}

func textBlock(text string) block {
	return block{"type": "text", "text": text}
}

func toolResultBlock(id, content string, isError bool) block {
	if strings.TrimSpace(content) == "" {
		content = "(no output)"
	}
	b := block{"type": "tool_result", "tool_use_id": normalizeToolUseID(id), "content": content}
	if isError {
		b["is_error"] = true
	}
	return b
}

func toolUseBlock(call openai.ToolCall, tools *toolSet) block {
	name := call.Function.Name
	args := call.Function.Arguments
	custom := call.Type == "custom" && call.Custom != nil
	if custom {
		name = call.Custom.Name
		args = call.Custom.Input
	}
	var input any = map[string]any{}
	if custom || tools.isCustom(name) {
		input = map[string]any{"input": args}
	} else if strings.TrimSpace(args) != "" {
		var object map[string]any
		if err := json.Unmarshal([]byte(args), &object); err == nil && object != nil {
			input = object
		} else {
			input = map[string]any{"arguments": args}
		}
	}
	return block{"type": "tool_use", "id": normalizeToolUseID(call.ID), "name": tools.modelName(name), "input": input}
}

// contentBlocks converts an OpenAI message content field (string or parts) to
// Anthropic blocks. Anthropic-shaped tool blocks, which Cursor sometimes
// sends, are passed through with names and IDs normalized.
func contentBlocks(raw json.RawMessage, role string, tools *toolSet) []block {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []block{textBlock(text)}
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		var single map[string]any
		if err := json.Unmarshal(raw, &single); err != nil {
			if t := strings.TrimSpace(openai.MessageText(raw)); t != "" {
				return []block{textBlock(t)}
			}
			return nil
		}
		parts = []map[string]any{single}
	}
	var out []block
	for _, part := range parts {
		kind, _ := part["type"].(string)
		switch kind {
		case "text", "input_text", "output_text":
			if t, _ := part["text"].(string); strings.TrimSpace(t) != "" {
				out = append(out, textBlock(t))
			}
		case "image_url":
			if role == "user" {
				if b := imageBlock(part); b != nil {
					out = append(out, b)
				}
			}
		case "image":
			if role == "user" {
				out = append(out, block(part))
			}
		case "tool_use":
			if role != "assistant" {
				continue
			}
			id, _ := part["id"].(string)
			name, _ := part["name"].(string)
			input := part["input"]
			if input == nil {
				input = map[string]any{}
			}
			if !strings.HasPrefix(name, mcpbridge.ToolPrefix) {
				name = tools.modelName(name)
			}
			out = append(out, block{"type": "tool_use", "id": normalizeToolUseID(id), "name": name, "input": input})
		case "tool_result":
			if role != "user" {
				continue
			}
			id, _ := part["tool_use_id"].(string)
			isError, _ := part["is_error"].(bool)
			out = append(out, anthropicToolResult(id, part["content"], isError))
		default:
			encoded, _ := json.Marshal(part)
			if t := strings.TrimSpace(openai.MessageText(encoded)); t != "" {
				out = append(out, textBlock(t))
			}
		}
	}
	return out
}

// anthropicToolResult keeps array tool_result content (text and image
// blocks, e.g. Cursor reading a PNG) instead of flattening it to text.
func anthropicToolResult(id string, content any, isError bool) block {
	items, ok := content.([]any)
	if !ok {
		text, _ := content.(string)
		if text == "" && content != nil {
			encoded, _ := json.Marshal(content)
			text = openai.MessageText(encoded)
		}
		return toolResultBlock(id, text, isError)
	}
	var blocks []any
	for _, item := range items {
		part, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch part["type"] {
		case "text":
			if text, _ := part["text"].(string); strings.TrimSpace(text) != "" {
				blocks = append(blocks, textBlock(text))
			}
		case "image":
			if source, ok := part["source"].(map[string]any); ok {
				blocks = append(blocks, block{"type": "image", "source": source})
			}
		}
	}
	if len(blocks) == 0 {
		return toolResultBlock(id, "", isError)
	}
	b := block{"type": "tool_result", "tool_use_id": normalizeToolUseID(id), "content": blocks}
	if isError {
		b["is_error"] = true
	}
	return b
}

func imageBlock(part map[string]any) block {
	var url string
	switch v := part["image_url"].(type) {
	case string:
		url = v
	case map[string]any:
		url, _ = v["url"].(string)
	}
	if url == "" {
		return nil
	}
	if strings.HasPrefix(url, "data:") {
		header, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
		mediaType, encoding, _ := strings.Cut(header, ";")
		if !ok || encoding != "base64" || mediaType == "" {
			return nil
		}
		return block{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": data}}
	}
	return block{"type": "image", "source": map[string]any{"type": "url", "url": url}}
}

// renderText flattens turns into one text transcript. It is the fallback
// (CLAUDE_HISTORY_MODE=text) when native transcript replay is unavailable.
func renderText(turns []turn) string {
	var b strings.Builder
	b.WriteString("<conversation>\n")
	for _, t := range turns {
		fmt.Fprintf(&b, "<%s>\n", t.Role)
		for _, blk := range t.Content {
			switch blk["type"] {
			case "text":
				b.WriteString(blk["text"].(string))
				b.WriteString("\n")
			case "tool_use":
				input, _ := json.Marshal(blk["input"])
				fmt.Fprintf(&b, "<tool_call id=%q name=%q>%s</tool_call>\n", blk["id"], blk["name"], input)
			case "tool_result":
				content, _ := blk["content"].(string)
				fmt.Fprintf(&b, "<tool_result id=%q>\n%s\n</tool_result>\n", blk["tool_use_id"], content)
			case "image":
				b.WriteString("[image]\n")
			}
		}
		fmt.Fprintf(&b, "</%s>\n", t.Role)
	}
	b.WriteString("</conversation>\n\nContinue the conversation as the assistant. To use a tool, call it directly; do not write tool calls as text.")
	return b.String()
}
