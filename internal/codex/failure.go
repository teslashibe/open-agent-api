package codex

import (
	"errors"
	"net/http"
)

// FailureClass is a shared, deterministic taxonomy for upstream failures. Later
// cooldown / rotation logic keys off these classes instead of re-implementing
// ad-hoc string matching. Classification is derived purely from known error
// types, status codes, and body markers already produced by the server; it
// never inspects raw upstream bodies, tokens, or account identifiers.
type FailureClass string

const (
	// FailureRateLimit is a transient capacity 429 (non usage-limit). Rotating
	// to another account may succeed.
	FailureRateLimit FailureClass = "rate_limit"
	// FailureQuota is an account-scoped usage-limit rejection
	// (ErrUsageLimitReached). Rotating to another account may succeed.
	FailureQuota FailureClass = "quota"
	// FailureAuth is an authentication/authorization failure for the current
	// account. Rotating to another account may succeed.
	FailureAuth FailureClass = "auth"
	// FailureTransient is a retryable upstream/transport failure of unknown
	// specificity (5xx, transport errors, unavailable clients, unmapped
	// errors). Rotating may help.
	FailureTransient FailureClass = "transient"
	// FailurePermanent is a client-side failure that rotation cannot fix
	// (bad request, context window exceeded). Rotating is pointless.
	FailurePermanent FailureClass = "permanent"
)

// FailurePhase describes how far a request progressed when it failed. It gates
// whether rotating the underlying Codex account is safe: switching accounts
// mid-stream would corrupt an in-flight (e.g. Cursor Agent tool) turn.
type FailurePhase string

const (
	// PhaseUnknown means the request ended before an upstream operation phase
	// was selected (for example request validation or queue admission).
	PhaseUnknown FailurePhase = "unknown"
	// PhaseConnect means the failure happened before any upstream event was
	// observed (Stream returned the error).
	PhaseConnect FailurePhase = "connect"
	// PhaseFirstEvent means the failure is the first upstream event and no
	// content (text delta or tool call) has reached the client yet.
	PhaseFirstEvent FailurePhase = "first_event"
	// PhaseMidStream means content has already been emitted to the client, so
	// rotating accounts would corrupt the turn.
	PhaseMidStream FailurePhase = "mid_stream"
	// PhaseComplete means a non-streaming completion call ended, or a streaming
	// response reached its downstream terminal marker. The non-streaming API
	// does not expose its internal event progress, so it must not be mislabeled
	// as a connect-time failure.
	PhaseComplete FailurePhase = "complete"
)

// ClassifyFailure maps a known error into a FailureClass. It is pure and
// deterministic. Ordering matters: usage-limit and context-window sentinels are
// checked before the generic Error kinds because they are wrapped inside
// Error values (a 429 upstream Error and a 400 client Error respectively).
//
// Mapping:
//   - ErrUsageLimitReached      -> quota
//   - ErrContextWindowExceeded  -> permanent
//   - ErrorKindAuth             -> auth
//   - ErrorKindClient           -> permanent
//   - ErrorKindUpstream, 429    -> rate_limit (capacity 429, not usage-limit)
//   - ErrorKindUpstream, other  -> transient (incl. ErrClientUnavailable / 5xx)
//   - nil / unknown             -> transient (explicit)
func ClassifyFailure(err error) FailureClass {
	if err == nil {
		return FailureTransient
	}
	if errors.Is(err, ErrUsageLimitReached) {
		return FailureQuota
	}
	if errors.Is(err, ErrContextWindowExceeded) {
		return FailurePermanent
	}
	if codexErr, ok := ErrorAs(err); ok {
		switch codexErr.Kind {
		case ErrorKindAuth:
			return FailureAuth
		case ErrorKindClient:
			return FailurePermanent
		case ErrorKindUpstream:
			if codexErr.Status == http.StatusTooManyRequests {
				return FailureRateLimit
			}
			return FailureTransient
		}
	}
	return FailureTransient
}

// ClassifyPhase reports how far a stream progressed when it failed.
//
// upstreamEvents is the number of upstream events observed so far, including the
// failing event itself; zero means the failure happened before the stream
// produced any event (connect-time). emittedContent reports whether any text
// delta or tool-call event has already been forwarded to the client.
func ClassifyPhase(upstreamEvents int, emittedContent bool) FailurePhase {
	switch {
	case emittedContent:
		return PhaseMidStream
	case upstreamEvents <= 0:
		return PhaseConnect
	default:
		return PhaseFirstEvent
	}
}

// MayRotateAccount reports whether it is safe to switch the underlying Codex
// account given a failure's class and phase. Mid-stream failures are never
// rotatable (switching would corrupt the turn), and permanent failures are
// never worth rotating for (a different account cannot help).
func MayRotateAccount(class FailureClass, phase FailurePhase) bool {
	if phase == PhaseMidStream {
		return false
	}
	switch class {
	case FailureRateLimit, FailureQuota, FailureAuth, FailureTransient:
		return true
	default: // FailurePermanent and any unknown class
		return false
	}
}
