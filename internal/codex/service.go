package codex

import (
	"context"
	"errors"
	"fmt"

	"github.com/teslashibe/codex-chat-api/internal/openai"
)

type Service interface {
	Complete(context.Context, Request) (Completion, error)
	Stream(context.Context, Request) (<-chan StreamEvent, error)
}

type Request struct {
	Model           string
	Messages        []openai.ChatMessage
	ReasoningEffort string
	Verbosity       string
	Faithful        bool
	Prewarm         bool
}

type Completion struct {
	Text      string
	ToolCalls []ToolCall
	Model     string
	ID        string
	Usage     openai.Usage
}

type StreamEvent struct {
	Delta         string
	ToolCalls     []ToolCall
	ToolCallDelta *ToolCallDelta
	Done          bool
	Model         string
	ID            string
	Usage         openai.Usage
	Err           error
}

type ToolCall struct {
	ID       string
	Type     string
	Function ToolCallFunction
}

type ToolCallFunction struct {
	Name      string
	Arguments string
}

type ToolCallDelta struct {
	Index    int
	ID       string
	Type     string
	Function ToolCallFunctionDelta
}

type ToolCallFunctionDelta struct {
	Name      string
	Arguments string
}

type ErrorKind string

const (
	ErrorKindAuth     ErrorKind = "auth"
	ErrorKindUpstream ErrorKind = "upstream"
	ErrorKindClient   ErrorKind = "client"
)

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
		fmt.Errorf("real codex websocket transport is not implemented"),
	)
}
