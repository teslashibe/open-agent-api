package structured

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/teslashibe/open-agent-api/internal/codex"
	"github.com/teslashibe/open-agent-api/internal/openai"
)

func TestDefaultPolicyResolvesCodexAliasesOnly(t *testing.T) {
	policy := NewPolicy(DefaultModels(), nil)

	for _, id := range policy.Models() {
		resolved, err := policy.Resolve(id)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", id, err)
		}
		if resolved.Provider != codex.ProviderCodex {
			t.Fatalf("model %q provider = %q, want codex", id, resolved.Provider)
		}
		if resolved.Upstream == "" || resolved.ReasoningEffort == "" || resolved.Verbosity == "" {
			t.Fatalf("model %q resolved = %#v", id, resolved)
		}
	}
	if !policy.Allowed(openai.DefaultModel) {
		t.Fatalf("default model %q is not on the structured allowlist", openai.DefaultModel)
	}
}

func TestPolicyRejectsUnknownAndDisabledModels(t *testing.T) {
	policy := NewPolicy(DefaultModels(), nil)

	for _, model := range []string{"", "gpt-9", "gemini-2.5-pro", "sonnet", "  "} {
		if _, err := policy.Resolve(model); err == nil {
			t.Fatalf("Resolve(%q) admitted an unsupported model", model)
		} else if err.Code != CodeUnsupportedModel {
			t.Fatalf("Resolve(%q) code = %q, want %q", model, err.Code, CodeUnsupportedModel)
		}
	}
}

func TestPolicyDropsProviderDisabledModels(t *testing.T) {
	codexDisabled := NewPolicy(DefaultModels(), func(provider string) bool {
		return provider != codex.ProviderCodex
	})
	if len(codexDisabled.Models()) != 0 {
		t.Fatalf("models = %v, want none when codex is disabled", codexDisabled.Models())
	}
	if _, err := codexDisabled.Resolve(openai.DefaultModel); err == nil || err.Code != CodeUnsupportedModel {
		t.Fatalf("Resolve() error = %v, want unsupported_model", err)
	}
	if codexDisabled.Version() != "none" {
		t.Fatalf("empty policy version = %q, want none", codexDisabled.Version())
	}
}

func TestPolicyVersionIsStableAndChangesWithTheAllowlist(t *testing.T) {
	base := NewPolicy([]string{"gpt-5.6-terra", openai.DefaultModel}, nil)
	// Same set, different order and with a duplicate: the version must not move.
	reordered := NewPolicy([]string{openai.DefaultModel, "gpt-5.6-terra", "gpt-5.6-terra"}, nil)
	if base.Version() != reordered.Version() {
		t.Fatalf("version = %q and %q, want a stable hash of the set", base.Version(), reordered.Version())
	}
	narrower := NewPolicy([]string{openai.DefaultModel}, nil)
	if narrower.Version() == base.Version() {
		t.Fatalf("narrowing the allowlist did not change the policy version (%q)", base.Version())
	}
}

func TestPolicyModelsIsACopy(t *testing.T) {
	policy := NewPolicy(DefaultModels(), nil)
	models := policy.Models()
	if len(models) == 0 {
		t.Fatal("expected a non-empty allowlist")
	}
	models[0] = "mutated"
	if policy.Models()[0] == "mutated" {
		t.Fatal("Models() leaked the internal slice")
	}
}

func TestRequestValidateRejectsMalformedEnvelopes(t *testing.T) {
	valid := Request{
		RequestID:      "req-1",
		IdempotencyKey: "idem-1",
		Operation:      "summary",
		Model:          openai.DefaultModel,
		Input:          "text",
		Schema:         json.RawMessage(personSchema),
		SchemaVersion:  "v1",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	mutations := map[string]func(*Request){
		"no request id":     func(r *Request) { r.RequestID = "" },
		"no idempotency":    func(r *Request) { r.IdempotencyKey = "" },
		"no operation":      func(r *Request) { r.Operation = "" },
		"no model":          func(r *Request) { r.Model = "" },
		"no input":          func(r *Request) { r.Input = "   " },
		"no schema":         func(r *Request) { r.Schema = nil },
		"no schema version": func(r *Request) { r.SchemaVersion = "" },
		"negative deadline": func(r *Request) { r.DeadlineMS = -1 },
		"bad effort":        func(r *Request) { r.ReasoningEffort = "turbo" },
		"bad verbosity":     func(r *Request) { r.Verbosity = "loud" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			req := valid
			mutate(&req)
			err := req.Validate()
			if err == nil {
				t.Fatal("Validate() accepted a malformed envelope")
			}
			if err.Code != CodeInvalidRequest {
				t.Fatalf("code = %q, want %q", err.Code, CodeInvalidRequest)
			}
		})
	}
}

func TestRequestNormalizeTrimsCallerStrings(t *testing.T) {
	req := Request{RequestID: " req ", IdempotencyKey: "\tidem\n", Operation: " op ", Model: " m ", SchemaVersion: " v1 "}
	req.Normalize()
	if req.RequestID != "req" || req.IdempotencyKey != "idem" || req.Operation != "op" || req.Model != "m" || req.SchemaVersion != "v1" {
		t.Fatalf("normalized = %#v", req)
	}
}

// gpt-6.1-sol is the model S'more's Reddit Signals asks structured inference
// for (profile proposals, request-group names and content briefs). Every alias in
// its chat ladder is admitted with the alias defaults the chat surface uses.
func TestDefaultPolicyAdmitsTheGPT61SolLadder(t *testing.T) {
	policy := NewPolicy(DefaultModels(), nil)

	want := map[string]struct{ effort, tier string }{
		"gpt-6.1-sol":             {"low", ""},
		"gpt-6.1-sol-low":         {"low", ""},
		"gpt-6.1-sol-medium":      {"medium", ""},
		"gpt-6.1-sol-high":        {"high", ""},
		"gpt-6.1-sol-xhigh":       {"xhigh", ""},
		"gpt-6.1-sol-max":         {"max", ""},
		"gpt-6.1-sol-fast":        {"low", "priority"},
		"gpt-6.1-sol-fast-low":    {"low", "priority"},
		"gpt-6.1-sol-fast-medium": {"medium", "priority"},
		"gpt-6.1-sol-fast-high":   {"high", "priority"},
		"gpt-6.1-sol-fast-xhigh":  {"xhigh", "priority"},
		"gpt-6.1-sol-fast-max":    {"max", "priority"},
	}
	for id, expected := range want {
		resolved, err := policy.Resolve(id)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", id, err)
		}
		if resolved.ID != id || resolved.Upstream != "gpt-6.1-sol" || resolved.Provider != codex.ProviderCodex {
			t.Fatalf("Resolve(%q) = %#v, want upstream gpt-6.1-sol on codex", id, resolved)
		}
		if resolved.ReasoningEffort != expected.effort || resolved.ServiceTier != expected.tier {
			t.Fatalf("Resolve(%q) effort/tier = %q/%q, want %q/%q",
				id, resolved.ReasoningEffort, resolved.ServiceTier, expected.effort, expected.tier)
		}
		chat := openai.ResolveModelAlias(id)
		if resolved.ReasoningEffort != chat.ReasoningEffort || resolved.Verbosity != chat.Verbosity || resolved.ServiceTier != chat.ServiceTier {
			t.Fatalf("Resolve(%q) = %#v, drifted from the chat alias %#v", id, resolved, chat)
		}
	}
}

// Every chat alias in the gpt-6.1-sol effort ladders must be on the structured
// allowlist, so a new effort level added to the chat ladder cannot silently
// answer unsupported_model on /v1/structured/inference. The explicit
// "-normal" speed aliases are left out, as they are for every other family on
// the allowlist: they duplicate the base ladder.
func TestDefaultPolicyCoversEveryGPT61SolChatAlias(t *testing.T) {
	policy := NewPolicy(DefaultModels(), nil)

	seen := 0
	for _, alias := range openai.ModelAliases() {
		if alias.UpstreamModel != "gpt-6.1-sol" || alias.Speed == "normal" {
			continue
		}
		seen++
		if !policy.Allowed(alias.ID) {
			t.Errorf("chat alias %q resolves to gpt-6.1-sol but is not on the structured allowlist", alias.ID)
		}
	}
	if seen != 12 {
		t.Fatalf("found %d gpt-6.1-sol chat aliases, want 12 (bare, five efforts, fast, five fast efforts)", seen)
	}
}

// Adding gpt-6.1-sol changes the policy version, so idempotent replays stored
// under the old allowlist are not served after the deploy.
func TestGPT61SolChangesThePolicyVersion(t *testing.T) {
	without := []string{}
	for _, id := range DefaultModels() {
		if !strings.HasPrefix(id, "gpt-6.1-sol") {
			without = append(without, id)
		}
	}
	if len(without) != len(DefaultModels())-12 {
		t.Fatalf("filtered %d models, want %d", len(DefaultModels())-len(without), 12)
	}
	if NewPolicy(without, nil).Version() == NewPolicy(DefaultModels(), nil).Version() {
		t.Fatal("adding the gpt-6.1-sol ladder did not change the policy version")
	}
}
