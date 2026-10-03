package mcpbridge

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestServeAnswersHandshakeListAndCall(t *testing.T) {
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"Read","arguments":{"path":"x"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`,
	}, "\n") + "\n"
	tools := []Tool{{Name: "Read", Description: "read", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}}
	var out bytes.Buffer
	if err := Serve(strings.NewReader(in), &out, tools); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("responses = %d, want 4 (notification must not be answered): %s", len(lines), out.String())
	}
	var init struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil || init.Result.ProtocolVersion != "2025-06-18" || init.Result.ServerInfo.Name != ServerName {
		t.Fatalf("initialize = %s (%v)", lines[0], err)
	}
	if !strings.Contains(lines[1], `"name":"Read"`) || !strings.Contains(lines[1], `"inputSchema"`) {
		t.Fatalf("tools/list = %s", lines[1])
	}
	if !strings.Contains(lines[2], `"isError":true`) {
		t.Fatalf("tools/call must return an error result, got %s", lines[2])
	}
	if !strings.Contains(lines[3], `-32601`) {
		t.Fatalf("unknown method = %s", lines[3])
	}
}
