package codex

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want FailureClass
	}{
		{
			name: "nil is transient",
			err:  nil,
			want: FailureTransient,
		},
		{
			// AC1: usage-limit / mapped 429 -> quota.
			name: "usage limit reached is quota",
			err:  NewError(ErrorKindUpstream, http.StatusTooManyRequests, "usage limit reached", fmt.Errorf("%w: codex response.failed: usage_limit_reached", ErrUsageLimitReached)),
			want: FailureQuota,
		},
		{
			// AC1: capacity 429 (not usage-limit) -> rate_limit.
			name: "capacity 429 is rate_limit",
			err:  NewError(ErrorKindUpstream, http.StatusTooManyRequests, "codex backend error", errors.New("codex response.failed: too many requests")),
			want: FailureRateLimit,
		},
		{
			// AC3: auth error must not be transient.
			name: "auth kind is auth",
			err:  NewError(ErrorKindAuth, http.StatusUnauthorized, "load codex credentials", errors.New("bad token")),
			want: FailureAuth,
		},
		{
			name: "context window exceeded is permanent",
			err:  NewError(ErrorKindClient, http.StatusBadRequest, "conversation exceeds the model's context window", fmt.Errorf("%w: codex response.failed: context_length_exceeded", ErrContextWindowExceeded)),
			want: FailurePermanent,
		},
		{
			name: "client kind is permanent",
			err:  NewError(ErrorKindClient, http.StatusBadRequest, "invalid tools JSON", errors.New("bad json")),
			want: FailurePermanent,
		},
		{
			name: "client unavailable is transient",
			err:  NewError(ErrorKindUpstream, http.StatusBadGateway, "codex transport is not configured", ErrClientUnavailable),
			want: FailureTransient,
		},
		{
			name: "upstream 5xx is transient",
			err:  NewError(ErrorKindUpstream, http.StatusBadGateway, "codex backend error", errors.New("codex response.failed: boom")),
			want: FailureTransient,
		},
		{
			name: "unknown error is transient",
			err:  errors.New("some unmapped error"),
			want: FailureTransient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyFailure(tt.err); got != tt.want {
				t.Fatalf("ClassifyFailure() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClassifyFailureAuthNotTransient documents AC3 explicitly: an auth /
// permanent error observed at connect time is never labeled transient.
func TestClassifyFailureAuthNotTransient(t *testing.T) {
	authErr := NewError(ErrorKindAuth, http.StatusUnauthorized, "load codex credentials", errors.New("bad token"))
	if got := ClassifyFailure(authErr); got == FailureTransient {
		t.Fatalf("auth error classified as transient, want non-transient")
	}

	permErr := NewError(ErrorKindClient, http.StatusBadRequest, "invalid tools JSON", errors.New("bad json"))
	if got := ClassifyFailure(permErr); got == FailureTransient {
		t.Fatalf("permanent error classified as transient, want non-transient")
	}
}

func TestClassifyPhase(t *testing.T) {
	tests := []struct {
		name           string
		upstreamEvents int
		emittedContent bool
		want           FailurePhase
	}{
		{
			name:           "no events is connect",
			upstreamEvents: 0,
			emittedContent: false,
			want:           PhaseConnect,
		},
		{
			name:           "first event is first_event",
			upstreamEvents: 1,
			emittedContent: false,
			want:           PhaseFirstEvent,
		},
		{
			// AC2: past the first delta/tool event -> mid_stream.
			name:           "emitted content is mid_stream",
			upstreamEvents: 3,
			emittedContent: true,
			want:           PhaseMidStream,
		},
		{
			name:           "emitted content dominates even at first event count",
			upstreamEvents: 1,
			emittedContent: true,
			want:           PhaseMidStream,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyPhase(tt.upstreamEvents, tt.emittedContent); got != tt.want {
				t.Fatalf("ClassifyPhase(%d, %t) = %q, want %q", tt.upstreamEvents, tt.emittedContent, got, tt.want)
			}
		})
	}
}

func TestMayRotateAccount(t *testing.T) {
	tests := []struct {
		name  string
		class FailureClass
		phase FailurePhase
		want  bool
	}{
		{
			name:  "connect quota may rotate",
			class: FailureQuota,
			phase: PhaseConnect,
			want:  true,
		},
		{
			name:  "connect rate_limit may rotate",
			class: FailureRateLimit,
			phase: PhaseConnect,
			want:  true,
		},
		{
			name:  "connect auth may rotate",
			class: FailureAuth,
			phase: PhaseConnect,
			want:  true,
		},
		{
			name:  "first_event transient may rotate",
			class: FailureTransient,
			phase: PhaseFirstEvent,
			want:  true,
		},
		{
			// AC2: mid_stream never rotates, even for a rotatable class.
			name:  "mid_stream quota may not rotate",
			class: FailureQuota,
			phase: PhaseMidStream,
			want:  false,
		},
		{
			name:  "mid_stream transient may not rotate",
			class: FailureTransient,
			phase: PhaseMidStream,
			want:  false,
		},
		{
			name:  "permanent never rotates",
			class: FailurePermanent,
			phase: PhaseConnect,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MayRotateAccount(tt.class, tt.phase); got != tt.want {
				t.Fatalf("MayRotateAccount(%q, %q) = %t, want %t", tt.class, tt.phase, got, tt.want)
			}
		})
	}
}
