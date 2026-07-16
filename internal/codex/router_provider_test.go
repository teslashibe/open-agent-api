package codex

import "testing"

func TestProviderForModel(t *testing.T) {
	cases := map[string]string{
		"gpt-5.5":                ProviderCodex,
		"gpt-5.6-sol":            ProviderCodex,
		"gpt-5.6-terra":          ProviderCodex,
		"gpt-5.6-luna":           ProviderCodex,
		"gpt-5.3-codex-spark":    ProviderCodex,
		"":                       ProviderCodex,
		"gemini-2.5-pro":         ProviderGemini,
		"gemini-2.5-flash-lite":  ProviderGemini,
		"claude-sonnet-5":        ProviderClaude,
		"api/claude-opus-4-8":    ProviderClaude,
		"sonnet":                 ProviderClaude,
		"opus":                   ProviderClaude,
		"haiku":                  ProviderClaude,
		"fable":                  ProviderClaude,
		"sonnet-unknown-variant": ProviderCodex,
		"anthropic/claude-opus-4.8": ProviderClaude,
		"anthropic/claude-sonnet-5": ProviderClaude,
	}
	for model, want := range cases {
		if got := ProviderForModel(model); got != want {
			t.Errorf("ProviderForModel(%q) = %q, want %q", model, got, want)
		}
	}
}
