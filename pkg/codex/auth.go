package codex

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/teslashibe/open-agent-api/internal/auth"
)

// ErrAuthenticationUnavailable means the requested credential lifetime could
// not be established. No underlying profile path or provider text is exposed.
var ErrAuthenticationUnavailable = errors.New("Codex authentication is unavailable")

// Authentication renews one explicitly selected profile through the gateway's
// existing durable auth source. It never performs inference or returns tokens.
// Callers must own the profile lifecycle and exclude independent CLI writers;
// profile locks coordinate only Sources within this process.
// Lifetime renewal requires a direct private file and parent directory; callers
// validate Windows ACLs. Observed identity changes fail closed, without claiming
// an atomic compare-and-swap with independent writers.
type Authentication struct {
	source *auth.Source
}

// NewAuthentication selects an absolute auth.json path without reading it.
// It does not discover a global profile or require a completion scaffold.
func NewAuthentication(authPath string) (*Authentication, error) {
	if !filepath.IsAbs(authPath) || strings.ContainsAny(authPath, "\x00\r\n") {
		return nil, ErrAuthenticationUnavailable
	}
	return &Authentication{source: auth.NewSource(authPath)}, nil
}

// EnsureValidUntil returns nil only if a known JWT expiry, conservatively
// capped by persisted whole-second expiry, is strictly beyond deadline. If
// renewal is needed, its rotated credentials are durably published (file and
// directory entry) before success. An insufficient renewed token still has its
// rotation persisted but returns an error, never admission.
// Callers supply a future deadline and a bounded context, schedule renewal only
// between jobs, and recheck their selected profile before accepting work.
// Cancellation stops renewal only before the OAuth exchange is sent. After that
// the provider may already have consumed the refresh token, so the exchange
// runs to persistence under a 30-second library bound and cancellation is then
// returned as a context error. Other failures are deliberately private.
// Success proves local lifetime, not entitlement or provider permission.
func (a *Authentication) EnsureValidUntil(ctx context.Context, deadline time.Time) error {
	if a == nil || a.source == nil || ctx == nil {
		return ErrAuthenticationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.source.EnsureValidUntil(ctx, deadline); err != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return ErrAuthenticationUnavailable
	}
	return nil
}
