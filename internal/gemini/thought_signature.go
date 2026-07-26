package gemini

import (
	"sync"
	"time"

	"github.com/teslashibe/open-chat-api/internal/openai"
)

// skipThoughtSignatureValidator is Gemini's documented escape hatch when a
// functionCall was not produced by the API (or a proxy client dropped the
// signature). Prefer a real signature whenever one is available.
const skipThoughtSignatureValidator = "skip_thought_signature_validator"

// thoughtSignatures remembers Gemini thoughtSignature values keyed by the
// OpenAI-facing tool_call id we emitted. Clients like Verum rebuild assistant
// tool_calls without extra_content, so we restore the signature on the next
// request by id. Single-replica gateway; entries expire to bound memory.
type thoughtSignatureStore struct {
	mu    sync.Mutex
	items map[string]thoughtSignatureEntry
	ttl   time.Duration
	now   func() time.Time
}

type thoughtSignatureEntry struct {
	sig       string
	expiresAt time.Time
}

var thoughtSignatures = newThoughtSignatureStore(30 * time.Minute)

func newThoughtSignatureStore(ttl time.Duration) *thoughtSignatureStore {
	return &thoughtSignatureStore{
		items: make(map[string]thoughtSignatureEntry),
		ttl:   ttl,
		now:   time.Now,
	}
}

func (s *thoughtSignatureStore) Remember(id, sig string) {
	id = trimID(id)
	sig = trimID(sig)
	if s == nil || id == "" || sig == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	s.items[id] = thoughtSignatureEntry{sig: sig, expiresAt: s.now().Add(s.ttl)}
}

func (s *thoughtSignatureStore) Lookup(id string) string {
	id = trimID(id)
	if s == nil || id == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	entry, ok := s.items[id]
	if !ok {
		return ""
	}
	return entry.sig
}

func (s *thoughtSignatureStore) expireLocked() {
	now := s.now()
	for id, entry := range s.items {
		if now.After(entry.expiresAt) {
			delete(s.items, id)
		}
	}
}

func trimID(v string) string {
	for len(v) > 0 && (v[0] == ' ' || v[0] == '\t' || v[0] == '\n' || v[0] == '\r') {
		v = v[1:]
	}
	for len(v) > 0 {
		last := v[len(v)-1]
		if last != ' ' && last != '\t' && last != '\n' && last != '\r' {
			break
		}
		v = v[:len(v)-1]
	}
	return v
}

func rememberThoughtSignature(id, sig string) {
	thoughtSignatures.Remember(id, sig)
}

func lookupThoughtSignature(id string) string {
	return thoughtSignatures.Lookup(id)
}

// resolveThoughtSignature picks a signature for an inbound assistant tool_call:
// 1) extra_content.google.thought_signature from OpenAI-compat clients
// 2) server-side cache keyed by tool_call id (Verum / other rebuilders)
// 3) skip_thought_signature_validator as last resort so Gemini 3 does not 400
func resolveThoughtSignature(toolCall openai.ToolCall) string {
	if sig := openai.ThoughtSignatureFromExtra(toolCall.ExtraContent); sig != "" {
		return sig
	}
	if sig := lookupThoughtSignature(toolCall.ID); sig != "" {
		return sig
	}
	return skipThoughtSignatureValidator
}
