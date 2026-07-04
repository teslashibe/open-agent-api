package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScaleComposeExampleProducesValidClients guards the committed
// docker-compose.scale.yml overlay: the CODEX_CLIENTS folded scalar must stay
// valid JSON and pass the same parse/validate path the server uses, and the
// overlay must keep Cursor affinity so chats stay sticky to a shard.
func TestScaleComposeExampleProducesValidClients(t *testing.T) {
	path := filepath.Join("..", "..", "docker-compose.scale.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)

	// AC 2: the overlay keeps queue key mode at cursor.
	if !strings.Contains(content, "CODEX_AGENT_QUEUE_KEY_MODE: cursor") {
		t.Fatalf("overlay must set CODEX_AGENT_QUEUE_KEY_MODE: cursor")
	}

	raw := extractFoldedScalar(t, content, "CODEX_CLIENTS")

	// AC 12: the documented example is valid JSON.
	var probe []map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Fatalf("CODEX_CLIENTS is not valid JSON: %v\nvalue: %q", err, raw)
	}
	if len(probe) < 4 {
		t.Fatalf("overlay must define at least 4 client labels, got %d", len(probe))
	}

	// AC 12: the example parses and validates through the real config path.
	clients, err := parseCodexClients(raw, Defaults())
	if err != nil {
		t.Fatalf("parseCodexClients() error = %v", err)
	}
	if err := validateCodexClients(clients); err != nil {
		t.Fatalf("validateCodexClients() error = %v", err)
	}
	wantLabels := []string{"codex-1", "codex-2", "codex-3", "codex-4"}
	for i, want := range wantLabels {
		if clients[i].Label != want {
			t.Fatalf("client %d label = %q, want %q", i, clients[i].Label, want)
		}
	}
}

// extractFoldedScalar returns the joined value of a `key: >-` folded block
// scalar in a small YAML document. It is intentionally minimal and only handles
// the shapes used by our compose overlays (no external YAML dependency).
func extractFoldedScalar(t *testing.T, content, key string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := -1
	keyIndent := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+":") {
			start = i
			keyIndent = len(line) - len(strings.TrimLeft(line, " "))
			break
		}
	}
	if start < 0 {
		t.Fatalf("key %q not found in compose overlay", key)
	}
	var parts []string
	for _, line := range lines[start+1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent <= keyIndent {
			break
		}
		parts = append(parts, strings.TrimSpace(line))
	}
	if len(parts) == 0 {
		t.Fatalf("no folded scalar value found for key %q", key)
	}
	return strings.Join(parts, " ")
}
