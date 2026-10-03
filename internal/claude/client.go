package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/teslashibe/open-agent-api/internal/claude/mcpbridge"
	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

const (
	DefaultExecutable = "claude"
	DefaultModel      = "sonnet"
	DefaultTimeout    = 10 * time.Minute

	// HistoryModeNative replays prior turns as real tool_use/tool_result
	// messages through a resumed transcript. HistoryModeText flattens them
	// into one user message (fallback if the transcript format changes).
	HistoryModeNative = "native"
	HistoryModeText   = "text"
)

// defaultSystemPrompt replaces Claude Code's coding-agent prompt when the
// client sends none, so plain API chats get a neutral assistant.
const defaultSystemPrompt = "You are Claude, a helpful AI assistant."

// bridgePreamble tells the model that the bridged tools are the client IDE's
// tools. Claude Code still injects its own environment block (cwd, platform),
// which describes the gateway container, not the user's workspace.
const bridgePreamble = "You are operating inside the user's IDE. The tools named " + mcpbridge.ToolPrefix + "* are the IDE's tools: the IDE executes them on the user's machine and returns the results in the next turn. Use them for all workspace, file, and terminal access. Ignore any working directory, platform, shell, or scratchpad path reported by the runtime environment; the only workspace is the user's, reached through these tools."

type Config struct {
	Executable   string
	DefaultModel string
	Timeout      time.Duration
	// BridgeExecutable is the gateway binary that serves `claude-mcp`.
	// Defaults to the running executable.
	BridgeExecutable string
	// RunDir holds one short-lived directory per request (cwd, tools file,
	// MCP config, transcript). Defaults to $TMPDIR/open-agent-api-claude.
	RunDir      string
	HistoryMode string
}

type Client struct {
	executable   string
	defaultModel string
	timeout      time.Duration
	bridge       string
	runDir       string
	historyMode  string

	versionOnce sync.Once
	version     string
}

func NewClient(cfg Config) (*Client, error) {
	if cfg.Executable == "" {
		cfg.Executable = DefaultExecutable
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = DefaultModel
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.BridgeExecutable == "" {
		if self, err := os.Executable(); err == nil {
			cfg.BridgeExecutable = self
		}
	}
	if cfg.RunDir == "" {
		cfg.RunDir = filepath.Join(os.TempDir(), "open-agent-api-claude")
	}
	switch cfg.HistoryMode {
	case "":
		cfg.HistoryMode = HistoryModeNative
	case HistoryModeNative, HistoryModeText:
	default:
		return nil, fmt.Errorf("unsupported claude history mode %q (expected native or text)", cfg.HistoryMode)
	}
	return &Client{
		executable:   cfg.Executable,
		defaultModel: cfg.DefaultModel,
		timeout:      cfg.Timeout,
		bridge:       cfg.BridgeExecutable,
		runDir:       cfg.RunDir,
		historyMode:  cfg.HistoryMode,
	}, nil
}

// Version returns the `claude --version` string, probed once.
func (c *Client) Version() string {
	c.versionOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, c.executable, "--version").Output()
		if err != nil {
			return
		}
		fields := strings.Fields(string(out))
		if len(fields) > 0 {
			c.version = fields[0]
		}
	})
	return c.version
}

// run is everything prepared on disk for one CLI invocation.
type run struct {
	dir   string
	args  []string
	stdin []byte
	tools *toolSet
}

func (c *Client) Stream(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	r, err := c.prepare(req)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd := exec.CommandContext(ctx, c.executable, r.args...)
	cmd.Dir = r.dir
	cmd.Stdin = bytes.NewReader(r.stdin)
	cmd.Env = append(os.Environ(),
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"DISABLE_AUTOUPDATER=1",
		"MCP_TIMEOUT=15000",
		// Cursor's Shell/Task/TodoWrite descriptions exceed the CLI's
		// default 2048-character MCP description cap.
		"CLAUDE_CODE_MAX_MCP_DESCRIPTION_LENGTH=1000000",
		// Never defer bridged tools behind tool search: the model must see
		// every client tool directly.
		"ENABLE_TOOL_SEARCH=false",
	)
	// Stop the CLI with SIGTERM so it shuts down its MCP bridge child; the
	// WaitDelay then escalates to SIGKILL.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = os.RemoveAll(r.dir)
		return nil, fmt.Errorf("create claude stdout pipe: %w", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		_ = os.RemoveAll(r.dir)
		return nil, codex.NewError(codex.ErrorKindUpstream, 502, "start claude code", err)
	}

	events := make(chan codex.StreamEvent)
	go c.readJSONL(ctx, cancel, cmd, stdout, stderr, events, r.tools, func() { _ = os.RemoveAll(r.dir) })
	return events, nil
}

func (c *Client) Complete(ctx context.Context, req codex.Request) (codex.Completion, error) {
	events, err := c.Stream(ctx, req)
	if err != nil {
		return codex.Completion{}, err
	}
	completion := codex.Completion{Model: c.model(req)}
	for event := range events {
		if event.Err != nil {
			return codex.Completion{}, event.Err
		}
		completion.Text += event.Delta
		if event.ToolCallDelta != nil {
			completion.ToolCalls = append(completion.ToolCalls, toolCallFromDelta(*event.ToolCallDelta))
		}
		completion.ToolCalls = append(completion.ToolCalls, event.ToolCalls...)
		if event.Model != "" {
			completion.Model = event.Model
		}
		if event.ID != "" {
			completion.ID = event.ID
		}
		if event.Usage != (openai.Usage{}) {
			completion.Usage = event.Usage
		}
	}
	return completion, nil
}

func (c *Client) prepare(req codex.Request) (run, error) {
	tools := newToolSet(parseToolSpecs(req.Tools))
	choice := parseToolChoice(req.ToolChoice)
	if choice.none {
		tools = newToolSet(nil)
	}
	conv := buildConversation(req.Messages, tools)

	if err := os.MkdirAll(c.runDir, 0o700); err != nil {
		return run{}, fmt.Errorf("create claude run dir: %w", err)
	}
	dir, err := os.MkdirTemp(c.runDir, "r")
	if err != nil {
		return run{}, fmt.Errorf("create claude request dir: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	fail := func(err error) (run, error) {
		_ = os.RemoveAll(dir)
		return run{}, err
	}

	rawModel := req.Model
	if rawModel == "" {
		rawModel = c.defaultModel
	}
	selected, err := openai.ResolveModelSelection(rawModel, req.ReasoningEffort, req.Speed)
	if err != nil {
		return fail(codex.NewError(codex.ErrorKindClient, 400, err.Error(), err))
	}
	model, _ := c.modelAndEffort(codex.Request{Model: selected.UpstreamModel})
	effort := selected.ReasoningEffort
	if selected.Speed == "fast" && !supportsFastPrint(c.Version()) {
		return fail(codex.NewError(codex.ErrorKindClient, 400, "Claude fast mode requires Claude Code 2.1.205 or newer", nil))
	}

	systemPath := filepath.Join(dir, "system.md")
	if err := os.WriteFile(systemPath, []byte(systemPrompt(conv.System, tools, choice, req.ParallelToolCalls)), 0o600); err != nil {
		return fail(fmt.Errorf("write claude system prompt: %w", err))
	}

	args := []string{
		"-p",
		"--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--model", model,
		"--system-prompt-file", systemPath,
		"--tools", "",
		"--setting-sources=",
		"--strict-mcp-config",
		"--permission-mode", "dontAsk",
		"--max-turns", "1",
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	settings, _ := json.Marshal(map[string]bool{"fastMode": selected.Speed == "fast"})
	args = append(args, "--settings", string(settings))

	if !tools.empty() {
		toolsPath := filepath.Join(dir, "tools.json")
		data, err := json.Marshal(tools.bridgeTools())
		if err != nil {
			return fail(fmt.Errorf("encode claude tools: %w", err))
		}
		if err := os.WriteFile(toolsPath, data, 0o600); err != nil {
			return fail(fmt.Errorf("write claude tools: %w", err))
		}
		mcpConfig, _ := json.Marshal(map[string]any{
			"mcpServers": map[string]any{
				mcpbridge.ServerName: map[string]any{
					"type":    "stdio",
					"command": c.bridge,
					"args":    []string{"claude-mcp", "--tools-file", toolsPath},
				},
			},
		})
		mcpPath := filepath.Join(dir, "mcp.json")
		if err := os.WriteFile(mcpPath, mcpConfig, 0o600); err != nil {
			return fail(fmt.Errorf("write claude mcp config: %w", err))
		}
		args = append(args, "--mcp-config", mcpPath)
	}

	tail := conv.Tail
	switch {
	case c.historyMode == HistoryModeText:
		all := append(append([]turn{}, conv.History...), conv.Tail...)
		tail = []turn{{Role: "user", Content: []block{textBlock(renderText(all))}}}
		args = append(args, "--no-session-persistence")
	case len(conv.History) > 0:
		transcript := filepath.Join(dir, "history.jsonl")
		if err := writeTranscript(transcript, newUUID(), dir, c.Version(), model, conv.History); err != nil {
			return fail(fmt.Errorf("write claude transcript: %w", err))
		}
		args = append(args, "--resume", transcript)
	default:
		args = append(args, "--no-session-persistence")
	}
	stdin, err := stdinLines(tail)
	if err != nil {
		return fail(err)
	}
	return run{dir: dir, args: args, stdin: stdin, tools: tools}, nil
}

type toolChoice struct {
	none     bool
	required bool
	name     string
	// serial is Anthropic disable_parallel_tool_use.
	serial bool
}

func parseToolChoice(raw json.RawMessage) toolChoice {
	trimmed := strings.TrimSpace(string(raw))
	switch trimmed {
	case "", "null", `"auto"`:
		return toolChoice{}
	case `"none"`:
		return toolChoice{none: true}
	case `"required"`, `"any"`:
		return toolChoice{required: true}
	}
	// OpenAI {type:function,function:{name}} or Anthropic {type:auto|any|
	// tool|none, name?, disable_parallel_tool_use?}.
	var object struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
		Function               struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return toolChoice{}
	}
	choice := toolChoice{serial: object.DisableParallelToolUse}
	switch object.Type {
	case "none":
		choice.none = true
		return choice
	case "any":
		choice.required = true
		return choice
	case "auto":
		return choice
	}
	choice.name = object.Function.Name
	if choice.name == "" {
		choice.name = object.Name
	}
	choice.required = choice.name != ""
	return choice
}

// systemPrompt builds the replacement system prompt. tool_choice "required"
// is deliberately not turned into an instruction: the server forces it on
// every agent user turn for Codex, which the CLI cannot enforce, and forcing
// a tool call makes plain questions awkward. The server's degenerate-turn
// retry still adds its own instruction when a turn stalls.
func systemPrompt(client string, tools *toolSet, choice toolChoice, parallel *bool) string {
	var parts []string
	if strings.TrimSpace(client) != "" {
		parts = append(parts, client)
	} else {
		parts = append(parts, defaultSystemPrompt)
	}
	if !tools.empty() {
		parts = append(parts, bridgePreamble)
		if choice.name != "" {
			parts = append(parts, "In this turn you must call the tool "+tools.modelName(choice.name)+".")
		}
		if (parallel != nil && !*parallel) || choice.serial {
			parts = append(parts, "Call at most one tool per turn.")
		}
	}
	return strings.Join(parts, "\n\n")
}

func (c *Client) model(req codex.Request) string {
	model, _ := c.modelAndEffort(req)
	return model
}

// modelAndEffort maps a request model onto a Claude Code --model value. It
// accepts Cursor-style names (anthropic/ or api/ prefix, dotted versions) and
// a trailing effort suffix for names that did not resolve through an alias.
func (c *Client) modelAndEffort(req codex.Request) (string, string) {
	if req.Model == "" {
		return c.defaultModel, ""
	}
	name := strings.TrimPrefix(req.Model, "anthropic/")
	name = strings.TrimPrefix(name, "api/")
	if !strings.HasPrefix(name, "claude-") {
		return name, ""
	}
	name = strings.ReplaceAll(name, ".", "-")
	for _, effort := range []string{"xhigh", "medium", "high", "low", "max"} {
		if strings.HasSuffix(name, "-"+effort) {
			return strings.TrimSuffix(name, "-"+effort), effort
		}
	}
	return name, ""
}

func toolCallFromDelta(delta codex.ToolCallDelta) codex.ToolCall {
	return codex.ToolCall{
		ID:   delta.ID,
		Type: defaultString(delta.Type, "function"),
		Function: codex.ToolCallFunction{
			Name:      delta.Function.Name,
			Arguments: delta.Function.Arguments,
		},
	}
}

func (c *Client) readJSONL(ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, stdout io.Reader, stderr *bytes.Buffer, out chan<- codex.StreamEvent, tools *toolSet, cleanup func()) {
	defer cancel()
	defer close(out)
	// Runs before close(out): every path below has reaped the process, and
	// consumers treat the closed channel as the end of the request.
	defer cleanup()

	// Every early return must reap the subprocess or it stays a zombie;
	// cancelling first makes CommandContext kill it so Wait cannot block.
	reap := func() {
		cancel()
		_ = cmd.Wait()
	}

	parser := newStreamParser(tools)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		events, done, err := parser.consume(scanner.Bytes())
		for _, event := range events {
			if !sendEvent(ctx, out, event) {
				reap()
				return
			}
		}
		if err != nil {
			sendEvent(ctx, out, codex.StreamEvent{Err: err})
			reap()
			return
		}
		if done {
			// The model turn is complete. After a tool_use stop the CLI
			// only denies the tool locally, so stop it rather than wait.
			reap()
			return
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	if scanErr != nil && ctx.Err() == nil {
		sendEvent(ctx, out, codex.StreamEvent{Err: codex.NewError(codex.ErrorKindUpstream, 502, "claude stream read failed", scanErr)})
		return
	}
	if waitErr != nil && ctx.Err() == nil {
		sendEvent(ctx, out, codex.StreamEvent{Err: claudeProcessError(waitErr, stderr.String())})
		return
	}
	if ctx.Err() == nil {
		sendEvent(ctx, out, codex.StreamEvent{Err: codex.NewError(codex.ErrorKindUpstream, 502, "claude code exited without a response", fmt.Errorf("%s", strings.TrimSpace(stderr.String())))})
	}
}

// sendEvent delivers an event unless the consumer is gone; a plain channel
// send here would block forever once the server stops reading on cancel.
func sendEvent(ctx context.Context, out chan<- codex.StreamEvent, event codex.StreamEvent) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func claudeProcessError(err error, stderr string) error {
	message := strings.TrimSpace(stderr)
	if message == "" {
		message = err.Error()
	}
	kind := codex.ErrorKindUpstream
	status := 502
	lower := strings.ToLower(message)
	if strings.Contains(lower, "auth") || strings.Contains(lower, "login") {
		kind = codex.ErrorKindAuth
		status = 401
	}
	if strings.Contains(lower, "rate") || strings.Contains(lower, "429") {
		status = 429
	}
	return codex.NewError(kind, status, "claude code failed", fmt.Errorf("%s", message))
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// Print mode honors fastMode settings from Claude Code 2.1.205 onward.
func supportsFastPrint(version string) bool {
	var major, minor, patch int
	if _, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch); err != nil {
		return false
	}
	return major > 2 || major == 2 && (minor > 1 || minor == 1 && patch >= 205)
}
