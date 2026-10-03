package openai

import (
	"fmt"
	"slices"
	"strings"
)

// Reviewed 2026-10-02 against Codex desktop CLI 0.159.2 model/list and
// Claude Code 2.1.286 plus https://code.claude.com/docs/en/model-config and
// https://code.claude.com/docs/en/fast-mode. These describe supported request
// controls, not account entitlement or measured execution speed.
type modelCapability struct {
	Model   string
	Efforts []string
	Fast    bool
	Claude  bool
}

var fullEfforts = []string{"low", "medium", "high", "xhigh", "max"}
var modelCapabilities = []modelCapability{
	{"gpt-6.1-sol", fullEfforts, true, false},
	{"gpt-6-astra", fullEfforts, true, false},
	{"gpt-6-sol", fullEfforts, true, false},
	{"gpt-6-luna", fullEfforts, true, false},
	{"gpt-5.6-sol", fullEfforts, true, false},
	{"gpt-5.6-terra", fullEfforts, true, false},
	{"gpt-5.6-luna", fullEfforts, true, false},
	{"gpt-5.5", []string{"low", "medium", "high", "xhigh"}, true, false},
	{"gpt-5.4", []string{"low", "medium", "high", "xhigh"}, true, false},
	{"gpt-5.3-codex-spark", []string{"low"}, false, false},
	{"claude-fable-5-1", fullEfforts, false, true},
	{"claude-opus-5-5", fullEfforts, true, true},
	{"claude-sonnet-5-5", fullEfforts, false, true},
	{"claude-haiku-4-5-20251001", nil, false, true},
	{"claude-fable-5", fullEfforts, false, true},
	{"claude-opus-5", fullEfforts, true, true},
	{"claude-opus-4-8", fullEfforts, true, true},
	{"claude-opus-4-7", fullEfforts, false, true},
	{"claude-opus-4-6", []string{"low", "medium", "high", "max"}, false, true},
	{"claude-opus-4-5-20251101", []string{"low", "medium", "high"}, false, true},
	{"claude-sonnet-5", fullEfforts, false, true},
	{"claude-sonnet-4-6", []string{"low", "medium", "high", "max"}, false, true},
	{"claude-sonnet-4-5-20250929", nil, false, true},
}

func capability(model string) (modelCapability, bool) {
	model = strings.TrimPrefix(strings.TrimPrefix(model, "anthropic/"), "api/")
	switch model {
	case "opus":
		model = "claude-opus-5-5"
	case "sonnet":
		model = "claude-sonnet-5-5"
	case "fable":
		model = "claude-fable-5-1"
	case "haiku", "claude-haiku-4-5":
		model = "claude-haiku-4-5-20251001"
	case "claude-opus-4-5":
		model = "claude-opus-4-5-20251101"
	case "claude-sonnet-4-5":
		model = "claude-sonnet-4-5-20250929"
	}
	for _, known := range modelCapabilities {
		if known.Model == model {
			return known, true
		}
	}
	return modelCapability{}, false
}

// ResolveModelSelection validates controls before any provider work. Unknown
// raw IDs retain passthrough compatibility, but reserved control variants
// and unsupported explicit controls never become raw IDs or silent fallbacks.
func ResolveModelSelection(model, effort, speed string) (ModelAlias, error) {
	if model == "" {
		model = DefaultModel
	}
	if effort == "ultra" || strings.HasSuffix(model, "-ultra") {
		return ModelAlias{}, fmt.Errorf("ultra requires Codex multi-agent orchestration; this gateway does not implement that mode")
	}
	if strings.Contains(model, "claude-") {
		model = strings.ReplaceAll(model, ".", "-")
	}
	selected := ResolveModelAlias(model)
	known, found := capability(selected.UpstreamModel)
	antigravity := model == "claude-sonnet-4-6" || model == "claude-opus-4-6-thinking"
	if !found && !antigravity {
		raw := strings.TrimPrefix(strings.TrimPrefix(model, "anthropic/"), "api/")
		for _, candidate := range modelCapabilities {
			stem := candidate.Model
			if date := strings.LastIndex(stem, "-20"); date >= 0 {
				stem = stem[:date]
			}
			if isControlVariant(raw, stem) {
				return ModelAlias{}, fmt.Errorf("unsupported model variant %q", model)
			}
		}
		for _, short := range []string{"opus", "sonnet", "fable", "haiku"} {
			if isControlVariant(raw, short) {
				return ModelAlias{}, fmt.Errorf("unsupported model variant %q", model)
			}
		}
	}
	if !found && !antigravity && strings.Contains(model, "claude-") && effort != "" {
		return ModelAlias{}, fmt.Errorf("effort support has not been reviewed for model %q", model)
	}
	if effort != "" {
		selected.ReasoningEffort = effort
	}
	// The bare Sonnet 4.6 ID retains its existing Antigravity route. Its api/
	// aliases use Claude Code and are checked against the Code matrix.
	if found && !antigravity && selected.ReasoningEffort != "" && !slices.Contains(known.Efforts, selected.ReasoningEffort) {
		return ModelAlias{}, fmt.Errorf("model %q does not support reasoning_effort %q", model, selected.ReasoningEffort)
	}
	if speed != "" && speed != "normal" && speed != "fast" {
		return ModelAlias{}, fmt.Errorf("speed must be normal or fast")
	}
	if speed != "" {
		if speed == "fast" && (!found || !known.Fast || antigravity) {
			return ModelAlias{}, fmt.Errorf("model %q does not support fast mode", model)
		}
		selected.Speed = speed
		if found && !known.Claude {
			selected.ServiceTier = ""
			if speed == "fast" {
				selected.ServiceTier = "priority"
			}
		}
	}
	if selected.Speed == "fast" && (!found || !known.Fast) {
		return ModelAlias{}, fmt.Errorf("model %q does not support fast mode", model)
	}
	return selected, nil
}

// Reserve only control suffixes, preserving unknown raw model/date IDs.
func isControlVariant(model, base string) bool {
	if !strings.HasPrefix(model, base+"-") {
		return false
	}
	suffix := strings.TrimPrefix(model, base+"-")
	first, _, _ := strings.Cut(suffix, "-")
	return slices.Contains([]string{"normal", "fast", "low", "medium", "high", "xhigh", "max", "ultra", "none", "minimal"}, first)
}
