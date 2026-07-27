package structured

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// Fingerprint is the canonical identity of the inference a request asks for. It
// is stored alongside a durable idempotency record and compared on every reuse
// of the same key, so a caller cannot retry one key with materially different
// parameters and be handed a response produced by another model — or be billed
// for a second, silently divergent call.
//
// KeyParts scopes *storage*: two requests that differ in caller, operation,
// input, schema version, or model policy version never share a slot at all. The
// fingerprint closes the remaining gap: everything that changes the upstream
// call but is not part of the storage key (model, effort, verbosity, output
// limit, schema body). The overlap with KeyParts is deliberate — a fingerprint
// that only covered the difference would be unreadable and would silently stop
// binding if KeyParts ever narrowed.
//
// Deliberately excluded, because they do not change the inference:
//
//	deadline_ms  — how long the caller is willing to wait, not what is asked
//	schema_name  — an upstream label only; the schema body is what constrains
//	request_id   — per-attempt trace identity; a retry always brings a new one
type Fingerprint struct {
	// Caller is the tenant identity (tenant header, else hashed Authorization).
	Caller string
	// Operation is the caller-declared logical operation.
	Operation string
	// Model is the alias the caller asked for.
	Model string
	// ResolvedModel is the alias the policy resolved it to. Both are bound: an
	// allowlist change that re-points an alias must not replay silently.
	ResolvedModel string
	// ReasoningEffort and Verbosity are the effective values sent upstream
	// (request override, else the alias default), not the raw request fields.
	ReasoningEffort string
	Verbosity       string
	// SchemaVersion is the caller's declared schema version.
	SchemaVersion string
	// ModelPolicyVersion is Policy.Version() at admission time.
	ModelPolicyVersion string
	// InputChecksum is a checksum of the request input.
	InputChecksum string
	// SchemaChecksum is a checksum of the canonicalized schema body, so a
	// re-serialized but semantically identical schema is not a conflict.
	SchemaChecksum string
	// MaxOutputTokens is the effective, already-clamped output limit.
	MaxOutputTokens int
}

// String derives the stored fingerprint. Every component is length-prefixed, on
// the same discipline as KeyParts.Key(), so no concatenation of two components
// can be confused for another pair.
func (f Fingerprint) String() string {
	hash := sha256.New()
	for _, part := range []string{
		f.Caller,
		f.Operation,
		f.Model,
		f.ResolvedModel,
		f.ReasoningEffort,
		f.Verbosity,
		f.SchemaVersion,
		f.ModelPolicyVersion,
		f.InputChecksum,
		f.SchemaChecksum,
		// strconv, not the internal itoa: a negative limit would otherwise
		// collapse to the empty string and stop being distinguishable.
		strconv.Itoa(f.MaxOutputTokens),
	} {
		_, _ = hash.Write([]byte(itoa(len(part))))
		_, _ = hash.Write([]byte{':'})
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0x1e})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// SchemaChecksum hashes the canonical form of a schema body. Key order and
// whitespace are not part of a schema's meaning, so two byte-different but
// equivalent documents must produce the same checksum; otherwise a client that
// re-serializes its schema between retries would be told its own retry is a
// conflict.
func SchemaChecksum(raw json.RawMessage) string {
	sum := sha256.Sum256([]byte(canonicalJSON(raw)))
	return hex.EncodeToString(sum[:])
}

// canonicalJSON re-encodes a JSON document with object keys sorted and
// insignificant whitespace removed. encoding/json marshals map[string]any with
// sorted keys, which is the whole canonicalization we need here.
//
// Numbers are decoded with UseNumber, so their literal form is preserved: 1 and
// 1.0 stay distinct. That is the conservative direction — a caller who changes
// a numeric literal gets a conflict rather than a silent replay — and it also
// keeps large integers from losing precision through float64.
//
// A document that does not parse is hashed verbatim. The server compiles the
// schema before it builds a fingerprint, so this is only reachable for a
// hand-built Fingerprint.
func canonicalJSON(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var node any
	if err := decoder.Decode(&node); err != nil {
		return trimmed
	}
	canonical, err := json.Marshal(node)
	if err != nil {
		return trimmed
	}
	return string(canonical)
}
