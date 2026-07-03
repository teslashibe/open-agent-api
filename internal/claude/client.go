package claude

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/codex"
	"github.com/teslashibe/codex-chat-api/internal/openai"
)

const (
	DefaultExecutable = "claude"
	DefaultModel      = "sonnet"
	DefaultTimeout    = 120 * time.Second
)

type Config struct {
	Executable   string
	DefaultModel string
	Timeout      time.Duration
}

type Client struct {
	executable   string
	defaultModel string
	timeout      time.Duration
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
	return &Client{executable: cfg.Executable, defaultModel: cfg.DefaultModel, timeout: cfg.Timeout}, nil
}

func (c *Client) Stream(ctx context.Context, req codex.Request) (<-chan codex.StreamEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	cmd := c.command(ctx, req)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create claude stdout pipe: %w", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, codex.NewError(codex.ErrorKindUpstream, 502, "start claude code", err)
	}

	events := make(chan codex.StreamEvent)
	go c.readJSONL(ctx, cancel, cmd, stdout, stderr, events)
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

func (c *Client) command(ctx context.Context, req codex.Request) *exec.Cmd {
	args := []string{
		"-p",
		"--verbose",
		"--output-format=stream-json",
		"--include-partial-messages",
		"--no-session-persistence",
		"--tools", "",
		"--model", c.model(req),
	}
	if effort := claudeEffort(req.ReasoningEffort); effort != "" {
		args = append(args, "--effort", effort)
	}
	args = append(args, promptFromMessages(req.Messages))
	cmd := exec.CommandContext(ctx, c.executable, args...)
	cmd.Stdin = strings.NewReader("")
	return cmd
}

func (c *Client) model(req codex.Request) string {
	if req.Model != "" {
		return strings.TrimPrefix(req.Model, "api/")
	}
	return c.defaultModel
}

func claudeEffort(effort string) string {
	switch effort {
	case "low", "medium", "high":
		return effort
	default:
		return ""
	}
}

func (c *Client) readJSONL(ctx context.Context, cancel context.CancelFunc, cmd *exec.Cmd, stdout io.Reader, stderr *bytes.Buffer, out chan<- codex.StreamEvent) {
	defer cancel()
	defer close(out)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		event, ok, err := parseJSONLEvent(scanner.Bytes())
		if err != nil {
			out <- codex.StreamEvent{Err: err}
			return
		}
		if !ok {
			continue
		}
		select {
		case out <- event:
		case <-ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		out <- codex.StreamEvent{Err: codex.NewError(codex.ErrorKindUpstream, 502, "claude stream read failed", err)}
	}
	if err := cmd.Wait(); err != nil && ctx.Err() == nil {
		out <- codex.StreamEvent{Err: claudeProcessError(err, stderr.String())}
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

func promptFromMessages(messages []openai.ChatMessage) string {
	var b strings.Builder
	for _, msg := range messages {
		text := openai.MessageText(msg.Content)
		if text == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		role := msg.Role
		if role == "" {
			role = "user"
		}
		b.WriteString(strings.ToUpper(role[:1]))
		if len(role) > 1 {
			b.WriteString(role[1:])
		}
		b.WriteString(": ")
		b.WriteString(text)
		for _, toolCall := range msg.ToolCalls {
			if toolCall.Function.Name != "" {
				b.WriteString("\nTool call ")
				b.WriteString(toolCall.Function.Name)
				b.WriteString(": ")
				b.WriteString(toolCall.Function.Arguments)
			}
		}
		b.WriteString("\n\n")
	}
	prompt := strings.TrimSpace(b.String())
	if prompt == "" {
		return "Continue."
	}
	return prompt
}
