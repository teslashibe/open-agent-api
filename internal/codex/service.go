package codex

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/teslashibe/codex-chat-api/internal/openai"
)

type Service interface {
	Complete(context.Context, Request) (Completion, error)
	Stream(context.Context, Request) (<-chan StreamEvent, error)
}

type Request struct {
	Model             string
	Messages          []openai.ChatMessage
	Tools             json.RawMessage
	ToolChoice        json.RawMessage
	ParallelToolCalls *bool
	ReasoningEffort   string
	Verbosity         string
	Faithful          bool
	Prewarm           bool
	RequestID         string
	AffinityKey       string
	AffinityKeyHash   string
	AffinityKeyMode   string
}

type Completion struct {
	Text      string
	ToolCalls []ToolCall
	Model     string
	ID        string
	Usage     openai.Usage
}

type StreamEvent struct {
	Delta          string
	ReasoningDelta string
	ToolCalls      []ToolCall
	ToolCallDelta  *ToolCallDelta
	Done           bool
	Model          string
	ID             string
	Usage          openai.Usage
	Err            error
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCallDelta struct {
	Index    int                   `json:"index"`
	ID       string                `json:"id,omitempty"`
	Type     string                `json:"type,omitempty"`
	Function ToolCallFunctionDelta `json:"function,omitempty"`
	Final    bool                  `json:"-"`
}

type ToolCallFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type ErrorKind string

const (
	ErrorKindAuth     ErrorKind = "auth"
	ErrorKindUpstream ErrorKind = "upstream"
	ErrorKindClient   ErrorKind = "client"
)

var ErrClientUnavailable = errors.New("codex client unavailable")

// ErrContextWindowExceeded marks upstream context_length_exceeded rejections
// so the server can surface an actionable message instead of a generic one.
var ErrContextWindowExceeded = errors.New("context window exceeded")

type Error struct {
	Kind    ErrorKind
	Status  int
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Message == "" {
		return string(e.Kind)
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Err
}

func NewError(kind ErrorKind, status int, message string, err error) error {
	return &Error{
		Kind:    kind,
		Status:  status,
		Message: message,
		Err:     err,
	}
}

func ErrorAs(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

type UnavailableService struct{}

func (UnavailableService) Complete(context.Context, Request) (Completion, error) {
	return Completion{}, unavailableError()
}

func (UnavailableService) Stream(context.Context, Request) (<-chan StreamEvent, error) {
	return nil, unavailableError()
}

func unavailableError() error {
	return NewError(
		ErrorKindUpstream,
		502,
		"codex transport is not configured",
		ErrClientUnavailable,
	)
}
