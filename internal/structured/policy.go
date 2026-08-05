package structured

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

// DefaultModels is the structured-capable allowlist. It is restricted to Codex
// aliases whose upstream honours strict json_schema output on the Responses
// websocket surface. Gemini and Claude Code aliases are deliberately excluded:
// they do not accept text.format json_schema, so admitting them would trade a
// clean unsupported_model for an unpredictable output_validation_failed.
func DefaultModels() []string {
	return []string{
		openai.DefaultModel,
		"gpt-5.6-sol-low",
		"gpt-5.6-sol-medium",
		"gpt-5.6-sol-high",
		"gpt-5.6-terra",
		"gpt-5.6-terra-low",
		"gpt-5.6-terra-medium",
		"gpt-5.6-terra-high",
		"gpt-5.6-luna",
		"gpt-5.6-luna-fast",
	}
}

// ResolvedModel is an admitted structured model.
type ResolvedModel struct {
	// ID is the public alias the caller asked for.
	ID string
	// Upstream is the wire model handed to the provider.
	Upstream string
	// Provider is the routing decision (always codex today).
	Provider string
	// ReasoningEffort and Verbosity are the alias defaults.
	ReasoningEffort string
	Verbosity       string
}

// Policy is the structured model allowlist plus its stable version. The
// version keys idempotency so a deploy that changes routing invalidates
// previously stored responses instead of replaying stale data (AC6).
type Policy struct {
	models  map[string]ResolvedModel
	order   []string
	version string
}

// NewPolicy builds a policy from the configured allowlist. Aliases whose
// provider is disabled on the gateway are dropped, so a narrowed
// GATEWAY_PROVIDERS narrows structured inference the same way it narrows
// /v1/models. A nil providerEnabled admits every provider.
func NewPolicy(models []string, providerEnabled func(string) bool) Policy {
	if providerEnabled == nil {
		providerEnabled = func(string) bool { return true }
	}
	policy := Policy{models: map[string]ResolvedModel{}}
	for _, raw := range models {
		id := strings.TrimSpace(raw)
		if id == "" || policy.models[id].ID != "" {
			continue
		}
		alias := openai.ResolveModelAlias(id)
		provider := codex.ProviderForModel(alias.UpstreamModel)
		if !providerEnabled(provider) {
			continue
		}
		policy.models[id] = ResolvedModel{
			ID:              id,
			Upstream:        alias.UpstreamModel,
			Provider:        provider,
			ReasoningEffort: alias.ReasoningEffort,
			Verbosity:       alias.Verbosity,
		}
		policy.order = append(policy.order, id)
	}
	sort.Strings(policy.order)
	policy.version = policyVersion(policy)
	return policy
}

// Version is a stable hash of the admitted allowlist. Two processes with the
// same allowlist produce the same value; any change produces a new one.
func (p Policy) Version() string {
	if p.version == "" {
		return "none"
	}
	return p.version
}

// Models returns the admitted alias IDs in stable order.
func (p Policy) Models() []string {
	out := make([]string, len(p.order))
	copy(out, p.order)
	return out
}

// Allowed reports whether an alias is on the structured allowlist.
func (p Policy) Allowed(model string) bool {
	_, ok := p.models[strings.TrimSpace(model)]
	return ok
}

// Resolve admits a model. Unknown or provider-disabled aliases fail with
// unsupported_model; the gateway never forwards an unrecognised model upstream.
func (p Policy) Resolve(model string) (ResolvedModel, *Error) {
	resolved, ok := p.models[strings.TrimSpace(model)]
	if !ok {
		return ResolvedModel{}, NewError(
			CodeUnsupportedModel,
			"model is not enabled for structured inference",
			"supported models: "+strings.Join(p.order, ", "),
		)
	}
	return resolved, nil
}

func policyVersion(p Policy) string {
	if len(p.order) == 0 {
		return "none"
	}
	hash := sha256.New()
	for _, id := range p.order {
		model := p.models[id]
		_, _ = hash.Write([]byte(model.ID + "\x00" + model.Upstream + "\x00" + model.Provider + "\x1e"))
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}
