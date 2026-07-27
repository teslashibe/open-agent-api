package structured

import "testing"

// AC3: every field the fingerprint claims to cover has to actually change it,
// otherwise the binding is decorative and a key could be reused with different
// parameters.
func TestFingerprintCoversEveryMaterialParameter(t *testing.T) {
	base := baseFingerprint().String()
	if base == "" {
		t.Fatal("Fingerprint.String() returned an empty string")
	}
	if base != baseFingerprint().String() {
		t.Fatal("Fingerprint.String() is not deterministic")
	}

	for name, mutate := range map[string]func(*Fingerprint){
		"caller":               func(f *Fingerprint) { f.Caller = "tenant-b" },
		"operation":            func(f *Fingerprint) { f.Operation = "extract" },
		"model":                func(f *Fingerprint) { f.Model = "gpt-5.6-terra" },
		"resolved model":       func(f *Fingerprint) { f.ResolvedModel = "gpt-5.6-terra" },
		"reasoning effort":     func(f *Fingerprint) { f.ReasoningEffort = "high" },
		"verbosity":            func(f *Fingerprint) { f.Verbosity = "high" },
		"schema version":       func(f *Fingerprint) { f.SchemaVersion = "v2" },
		"model policy version": func(f *Fingerprint) { f.ModelPolicyVersion = "policy-2" },
		"input checksum":       func(f *Fingerprint) { f.InputChecksum = InputChecksum("goodbye") },
		"schema checksum":      func(f *Fingerprint) { f.SchemaChecksum = SchemaChecksum([]byte(`{"type":"array"}`)) },
		"max output tokens":    func(f *Fingerprint) { f.MaxOutputTokens = 1024 },
	} {
		t.Run(name, func(t *testing.T) {
			fingerprint := baseFingerprint()
			mutate(&fingerprint)
			if fingerprint.String() == base {
				t.Fatalf("changing %s did not change the fingerprint", name)
			}
		})
	}
}

// Length-prefixing means a value that "borrows" a character from the next
// component cannot collide with the original.
func TestFingerprintIsNotVulnerableToComponentSmuggling(t *testing.T) {
	left := Fingerprint{Caller: "ab", Operation: "c"}.String()
	right := Fingerprint{Caller: "a", Operation: "bc"}.String()
	if left == right {
		t.Fatal("fingerprint component boundaries are ambiguous")
	}
}

// The canonicalizer is load-bearing: without it a client that re-serializes an
// identical schema between retries would be told its own retry is a conflict.
func TestSchemaChecksumIgnoresFormattingAndKeyOrder(t *testing.T) {
	compact := []byte(`{"type":"object","additionalProperties":false,"required":["title"],"properties":{"title":{"type":"string"}}}`)
	reordered := []byte(`{"properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false,"type":"object"}`)
	indented := []byte("{\n  \"type\": \"object\",\n  \"additionalProperties\": false,\n  \"required\": [ \"title\" ],\n  \"properties\": {\n    \"title\": { \"type\": \"string\" }\n  }\n}\n")

	want := SchemaChecksum(compact)
	for name, raw := range map[string][]byte{"reordered keys": reordered, "indented": indented} {
		t.Run(name, func(t *testing.T) {
			if got := SchemaChecksum(raw); got != want {
				t.Fatalf("SchemaChecksum(%s) = %s, want the canonical %s", name, got, want)
			}
		})
	}

	// A real change to the schema still has to be visible.
	changed := []byte(`{"type":"object","additionalProperties":false,"required":["title"],"properties":{"title":{"type":"integer"}}}`)
	if SchemaChecksum(changed) == want {
		t.Fatal("SchemaChecksum ignored a change to a property type")
	}
}

// Array order is meaning in JSON Schema ("required" is a set, but "enum" is
// not), so canonicalization must not sort array elements.
func TestSchemaChecksumPreservesArrayOrder(t *testing.T) {
	left := SchemaChecksum([]byte(`{"type":"string","enum":["a","b"]}`))
	right := SchemaChecksum([]byte(`{"type":"string","enum":["b","a"]}`))
	if left == right {
		t.Fatal("SchemaChecksum reordered an array")
	}
}

// A large integer must not be rounded through float64 on its way through the
// canonicalizer.
func TestSchemaChecksumKeepsIntegerPrecision(t *testing.T) {
	left := SchemaChecksum([]byte(`{"type":"array","items":{"type":"string"},"maxItems":9007199254740993}`))
	right := SchemaChecksum([]byte(`{"type":"array","items":{"type":"string"},"maxItems":9007199254740992}`))
	if left == right {
		t.Fatal("SchemaChecksum lost integer precision")
	}
}

// A schema that does not parse is hashed verbatim rather than collapsing every
// unparseable body onto one checksum.
func TestSchemaChecksumFallsBackToTheRawBytes(t *testing.T) {
	if SchemaChecksum([]byte("not json")) == SchemaChecksum([]byte("also not json")) {
		t.Fatal("unparseable schemas collapsed onto one checksum")
	}
	if SchemaChecksum(nil) != SchemaChecksum([]byte("   ")) {
		t.Fatal("an empty schema body is not canonical")
	}
}
