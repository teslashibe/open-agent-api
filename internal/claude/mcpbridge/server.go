// Package mcpbridge is a minimal stdio MCP server that advertises the client's
// (Cursor's) tools to Claude Code so the model emits native tool_use blocks.
//
// It never executes anything: the gateway reads tool_use from the CLI's
// stream-json output and hands it back to the client, and the CLI runs with
// --permission-mode dontAsk so tools/call is not expected to arrive. If it
// does, the call is answered with an error result.
package mcpbridge

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// ServerName is the MCP server name; Claude Code exposes its tools to the
// model as mcp__<ServerName>__<tool>. One character keeps names short.
const ServerName = "c"

// ToolPrefix is the prefix Claude Code puts on every bridged tool name.
const ToolPrefix = "mcp__" + ServerName + "__"

const defaultProtocolVersion = "2025-06-18"

// Tool is one entry of the tools file and of the tools/list result.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Main runs the bridge as the `claude-mcp` subcommand.
func Main(args []string) error {
	fs := flag.NewFlagSet("claude-mcp", flag.ContinueOnError)
	toolsFile := fs.String("tools-file", "", "JSON file with the tools to advertise")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *toolsFile == "" {
		return errors.New("claude-mcp: --tools-file is required")
	}
	tools, err := LoadTools(*toolsFile)
	if err != nil {
		return err
	}
	return Serve(os.Stdin, os.Stdout, tools)
}

// LoadTools reads a tools file written by the Claude client.
func LoadTools(path string) ([]Tool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read tools file: %w", err)
	}
	var tools []Tool
	if err := json.Unmarshal(data, &tools); err != nil {
		return nil, fmt.Errorf("decode tools file: %w", err)
	}
	return tools, nil
}

// Serve answers newline-delimited JSON-RPC on r/w until r reaches EOF, which
// happens when the parent claude process exits or is killed.
func Serve(r io.Reader, w io.Writer, tools []Tool) error {
	if tools == nil {
		tools = []Tool{}
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	out := bufio.NewWriter(w)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		// Notifications (no id) never get a response.
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		resp := response{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &params)
			version := params.ProtocolVersion
			if version == "" {
				version = defaultProtocolVersion
			}
			resp.Result = map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": ServerName, "version": "1"},
			}
		case "tools/list":
			resp.Result = map[string]any{"tools": tools}
		case "tools/call":
			resp.Result = map[string]any{
				"isError": true,
				"content": []map[string]any{{
					"type": "text",
					"text": "This tool runs in the user's IDE, not on this server.",
				}},
			}
		case "ping":
			resp.Result = map[string]any{}
		default:
			resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
		}
		if err := encoder.Encode(resp); err != nil {
			return err
		}
		if err := out.Flush(); err != nil {
			return err
		}
	}
	return scanner.Err()
}
