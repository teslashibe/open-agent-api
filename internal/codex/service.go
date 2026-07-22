package codex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/teslashibe/codex-chat-api/internal/openai"
)

type Service interface {
	Complete(context.Context, Request) (Completion, error)
	Stream(context.Context, Request) (<-chan StreamEvent, error)
}

// DrainAwareService can interrupt work that has not yet opened an upstream
// stream while allowing already-started streams to finish normally.
type DrainAwareService interface {
	SetDraining(bool)
}

// HealthReporter exposes only bounded operational labels and aggregate client
// counts. Implementations must not include credential paths or identities.
type HealthReporter interface {
	Health() PoolHealth
}

// ReadinessReporter may validate and proactively refresh local credentials.
// It must not open an upstream model connection.
type ReadinessReporter interface {
	Ready(context.Context) PoolHealth
}

type PoolHealth struct {
	TotalClients  int            `json:"total_clients"`
	UsableClients int            `json:"usable_clients"`
	Clients       []ClientHealth `json:"clients"`
}

type ClientHealth struct {
	Label  string `json:"label"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
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
	// AllowCooling is set only by the server's model-level quota fallback.
	// It lets that fallback make one attempt when every pooled account is
	// cooling; ordinary requests always exclude cooling accounts.
	AllowCooling bool
	// DisablePoolWait is set for secondary streaming attempts made after the
	// HTTP response has started. Those attempts may still acquire an immediately
	// available client, but must never queue behind a saturated pool.
	DisablePoolWait bool
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
	ID               string           `json:"id"`
	Type             string           `json:"type"`
	Function         ToolCallFunction `json:"function"`
	ThoughtSignature string           `json:"-"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCallDelta struct {
	Index            int                   `json:"index"`
	ID               string                `json:"id,omitempty"`
	Type             string                `json:"type,omitempty"`
	Function         ToolCallFunctionDelta `json:"function,omitempty"`
	ThoughtSignature string                `json:"-"`
	Final            bool                  `json:"-"`
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

// ErrUsageLimitReached marks upstream quota rejections so the server can
// fall back to an overflow model.
var ErrUsageLimitReached = errors.New("usage limit reached")

type Error struct {
	Kind       ErrorKind
	Status     int
	Message    string
	Err        error
	RetryAfter time.Duration
	ResetAt    time.Time

	credentialRevision    [sha256.Size]byte
	hasCredentialRevision bool
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

// withCredentialRevision associates an upstream failure with the exact
// credential bytes used by that request attempt. The revision is intentionally
// private: it is used only for local recovery decisions and must never be
// rendered in errors, logs, health responses, or metric labels.
func withCredentialRevision(err error, revision [sha256.Size]byte) error {
	if codexErr, ok := ErrorAs(err); ok {
		codexErr.credentialRevision = revision
		codexErr.hasCredentialRevision = true
	}
	return err
}

func credentialRevisionFromError(err error) ([sha256.Size]byte, bool) {
	if codexErr, ok := ErrorAs(err); ok && codexErr.hasCredentialRevision {
		return codexErr.credentialRevision, true
	}
	return [sha256.Size]byte{}, false
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
