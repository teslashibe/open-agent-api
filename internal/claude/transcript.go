package claude

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// writeTranscript writes turns as a Claude Code session transcript that the
// CLI loads with --resume <path>. The row shape mirrors what the CLI itself
// records; only the fields resume needs are populated. This format is not a
// public contract, so CLAUDE_HISTORY_MODE=text exists as a fallback.
func writeTranscript(path, sessionID, cwd, version, model string, turns []turn) error {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var parent any
	for i, t := range turns {
		id := newUUID()
		row := map[string]any{
			"parentUuid":  parent,
			"isSidechain": false,
			"userType":    "external",
			"cwd":         cwd,
			"sessionId":   sessionID,
			"version":     version,
			"type":        t.Role,
			"uuid":        id,
			"timestamp":   timestamp,
		}
		if t.Role == "assistant" {
			row["message"] = map[string]any{
				"id":            fmt.Sprintf("msg_replay_%d", i),
				"type":          "message",
				"role":          "assistant",
				"model":         model,
				"content":       t.Content,
				"stop_reason":   assistantStopReason(t),
				"stop_sequence": nil,
				"usage":         map[string]int{"input_tokens": 0, "output_tokens": 0},
			}
		} else {
			row["message"] = map[string]any{"role": "user", "content": t.Content}
		}
		if err := encoder.Encode(row); err != nil {
			return fmt.Errorf("encode transcript row: %w", err)
		}
		parent = id
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func assistantStopReason(t turn) string {
	if hasBlockType(t, "tool_use") {
		return "tool_use"
	}
	return "end_turn"
}

// stdinLines encodes turns as --input-format stream-json messages.
func stdinLines(turns []turn) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	for _, t := range turns {
		line := map[string]any{
			"type":               t.Role,
			"message":            map[string]any{"role": t.Role, "content": t.Content},
			"parent_tool_use_id": nil,
			"session_id":         "",
		}
		if err := encoder.Encode(line); err != nil {
			return nil, fmt.Errorf("encode stdin message: %w", err)
		}
	}
	return buf.Bytes(), nil
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
