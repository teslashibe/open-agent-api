package openai

import (
	"strings"
	"testing"
)

func TestReviewedCLICombinations(t *testing.T) {
	full := []string{"low", "medium", "high", "xhigh", "max"}
	cases := []struct {
		model   string
		efforts []string
		fast    bool
	}{
		{"gpt-6.1-sol", full, true}, {"gpt-6-astra", full, true}, {"gpt-6-sol", full, true}, {"gpt-6-luna", full, true},
		{"gpt-5.6-sol", full, true}, {"gpt-5.6-terra", full, true}, {"gpt-5.6-luna", full, true}, {"gpt-5.5", full[:4], true},
		{"api/claude-fable-5-1", full, false}, {"api/claude-fable-5", full, false}, {"api/claude-opus-5-5", full, true},
		{"api/claude-opus-5", full, true}, {"api/claude-opus-4-8", full, true}, {"api/claude-opus-4-7", full, false},
		{"api/claude-sonnet-5-5", full, false}, {"api/claude-sonnet-5", full, false},
		{"api/claude-opus-4-6", []string{"low", "medium", "high", "max"}, false},
		{"api/claude-sonnet-4-6", []string{"low", "medium", "high", "max"}, false},
		{"api/claude-opus-4-5", full[:3], false}, {"api/claude-haiku-4-5", nil, false}, {"api/claude-sonnet-4-5", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if _, err := ResolveModelSelection(tc.model, "", "normal"); err != nil {
				t.Fatal(err)
			}
			for _, effort := range tc.efforts {
				for _, speed := range []string{"normal", "fast"} {
					got, err := ResolveModelSelection(tc.model, effort, speed)
					if speed == "fast" && !tc.fast {
						if err == nil {
							t.Fatal("unsupported fast accepted")
						}
						continue
					}
					if err != nil || got.ReasoningEffort != effort || got.Speed != speed {
						t.Fatalf("%s/%s = %#v %v", effort, speed, got, err)
					}
					alias, err := ResolveModelSelection(tc.model+"-"+speed+"-"+effort, "", "")
					if err != nil || alias.ReasoningEffort != effort || alias.UpstreamModel != got.UpstreamModel {
						t.Fatalf("alias %s/%s = %#v %v", effort, speed, alias, err)
					}
				}
			}
		})
	}
}

func TestInvalidControlsNeverFallback(t *testing.T) {
	for _, tc := range []struct{ model, effort, speed string }{
		{"gpt-5.5-max", "", ""}, {"gpt-6.1-sol-ultra", "", ""}, {"gpt-6-luna", "none", ""},
		{"gpt-6-astra", "ultra", ""}, {"gpt-6-astra", "minimal", ""}, {"gpt-6-sol", "high", "turbo"},
		{"api/claude-opus-4-6-xhigh", "", ""}, {"api/claude-sonnet-5-5-fast", "", ""},
		{"api/claude-haiku-4-5", "high", ""}, {"api/claude-sonnet-4-5-max", "", ""},
		{"api/claude-opus-4-5", "max", ""}, {"api/claude-mythos-5-1", "high", ""},
		{"opus-ultra", "", ""}, {"sonnet-fast", "", ""},
	} {
		if _, err := ResolveModelSelection(tc.model, tc.effort, tc.speed); err == nil {
			t.Fatalf("accepted %#v", tc)
		}
	}
}

func TestExplicitNormalOverridesFastAndDefaultsStayStable(t *testing.T) {
	got, err := ResolveModelSelection("gpt-6.1-sol-fast-high", "max", "normal")
	if err != nil || got.ServiceTier != "" || got.Speed != "normal" || got.ReasoningEffort != "max" {
		t.Fatalf("%#v %v", got, err)
	}
	got, err = ResolveModelSelection("", "", "")
	if err != nil || got.UpstreamModel != "gpt-5.6-sol" || got.ReasoningEffort != "low" {
		t.Fatalf("%#v %v", got, err)
	}
	for _, model := range []string{"api/claude-opus-5-5", "haiku", "sonnet"} {
		got, err = ResolveModelSelection(model, "", "")
		if err != nil || got.ReasoningEffort != "" {
			t.Fatalf("%#v %v", got, err)
		}
	}
	got, err = ResolveModelSelection("api/claude-sonnet-4-6-max", "", "")
	if err != nil || !strings.HasPrefix(got.UpstreamModel, "api/") {
		t.Fatalf("lost Claude routing: %#v %v", got, err)
	}
	got, err = ResolveModelSelection("claude-sonnet-4-6", "", "")
	if err != nil || got.UpstreamModel != "claude-sonnet-4-6" {
		t.Fatalf("lost Antigravity routing: %#v %v", got, err)
	}
}

func TestFutureRawModelIDsPreservePassthrough(t *testing.T) {
	for _, model := range []string{"claude-opus-5-6", "api/claude-opus-5-6", "gpt-6-sol-2027", "gpt-6.1-sol-20270101", "gpt-next"} {
		got, err := ResolveModelSelection(model, "", "")
		if err != nil || got.UpstreamModel != model {
			t.Fatalf("%s = %#v %v", model, got, err)
		}
	}
}
