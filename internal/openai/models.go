package openai

import "strings"

const (
	DefaultReasoningEffort = "medium"
	DefaultVerbosity       = "medium"
	gpt56DefaultVerbosity  = "low"
)

type ModelAlias struct {
	ID              string
	UpstreamModel   string
	ReasoningEffort string
	Verbosity       string
	ServiceTier     string
	Speed           string
	// ContextHardMaxBytes forces aggressive context reduction (including
	// dropping oldest turns) for models with small context windows. 0 = off.
	ContextHardMaxBytes int
	// Unlisted hides the alias from GET /v1/models while still resolving it
	// for chat completions (e.g. overflow-only Spark).
	Unlisted bool
}

func serviceTierAlias(id, upstream, effort, verbosity, serviceTier string) ModelAlias {
	model := alias(id, upstream, effort, verbosity)
	model.ServiceTier = serviceTier
	if serviceTier == "priority" {
		model.Speed = "fast"
	}
	return model
}

func alias(id, upstream, effort, verbosity string) ModelAlias {
	return ModelAlias{
		ID:              id,
		UpstreamModel:   upstream,
		ReasoningEffort: effort,
		Verbosity:       verbosity,
	}
}

// codexEffortLadder returns bare + effort aliases for a Codex model.
// Exposes the model-specific native CLI effort levels, excluding "ultra"
// (ultra is a Codex product multi-agent mode, not a reasoning.effort value).
func codexEffortLadder(upstream string) []ModelAlias {
	defaultEffort := DefaultReasoningEffort
	if upstream == "gpt-6.1-sol" {
		defaultEffort = "low"
	}
	known, _ := capability(upstream)
	out := []ModelAlias{alias(upstream, upstream, defaultEffort, gpt56DefaultVerbosity)}
	for _, effort := range known.Efforts {
		out = append(out, alias(upstream+"-"+effort, upstream, effort, gpt56DefaultVerbosity))
	}
	return out
}

// codexFastEffortLadder returns priority-tier aliases for every exposed effort.
func codexFastEffortLadder(upstream string) []ModelAlias {
	known, _ := capability(upstream)
	out := []ModelAlias{serviceTierAlias(upstream+"-fast", upstream, "low", gpt56DefaultVerbosity, "priority")}
	for _, effort := range known.Efforts {
		out = append(out, serviceTierAlias(upstream+"-fast-"+effort, upstream, effort, gpt56DefaultVerbosity, "priority"))
	}
	return out
}

// astraEffortLadders returns API-native normal and priority-tier aliases.
// Codex Ultra also requires multi-agent orchestration this proxy cannot provide.
func astraEffortLadders() []ModelAlias {
	const upstream = "gpt-6-astra"
	known, _ := capability(upstream)
	efforts := known.Efforts
	out := []ModelAlias{alias(upstream, upstream, "low", DefaultVerbosity)}
	for _, effort := range efforts {
		out = append(out, alias(upstream+"-"+effort, upstream, effort, DefaultVerbosity))
	}
	out = append(out, serviceTierAlias(upstream+"-fast", upstream, "low", DefaultVerbosity, "priority"))
	for _, effort := range efforts {
		out = append(out, serviceTierAlias(upstream+"-fast-"+effort, upstream, effort, DefaultVerbosity, "priority"))
	}
	return out
}

// claudeEffortLadder returns the Claude Code aliases for one pinned model: the
// bare ID (non-Cursor clients), api/ and anthropic/ IDs (Cursor BYOK), and
// model-specific effort variants from the reviewed CLI matrix; the
// unsuffixed IDs carry no effort so the CLI applies the model's default.
func claudeEffortLadder(model string) []ModelAlias {
	known, _ := capability(model)
	out := []ModelAlias{}
	for _, prefix := range []string{"", "api/", "anthropic/"} {
		// Keep bare Sonnet4.6 owned by Antigravity; prefixed IDs are Claude Code.
		if model == "claude-sonnet-4-6" && prefix == "" {
			continue
		}
		upstream := model
		if model == "claude-sonnet-4-6" {
			upstream = "api/" + model
		}
		out = append(out, alias(prefix+model, upstream, "", DefaultVerbosity))
		for _, effort := range known.Efforts {
			out = append(out, alias(prefix+model+"-"+effort, upstream, effort, DefaultVerbosity))
		}
		if known.Fast {
			fast := alias(prefix+model+"-fast", upstream, "", DefaultVerbosity)
			fast.Speed = "fast"
			out = append(out, fast)
			for _, effort := range known.Efforts {
				fast = alias(prefix+model+"-fast-"+effort, upstream, effort, DefaultVerbosity)
				fast.Speed = "fast"
				out = append(out, fast)
			}
		}
	}
	return out
}

func gpt54EffortLadders() []ModelAlias {
	const upstream = "gpt-5.4"
	return []ModelAlias{
		alias(upstream, upstream, "medium", DefaultVerbosity),
		alias(upstream+"-low", upstream, "low", DefaultVerbosity),
		alias(upstream+"-medium", upstream, "medium", DefaultVerbosity),
		alias(upstream+"-high", upstream, "high", DefaultVerbosity),
		alias(upstream+"-xhigh", upstream, "xhigh", DefaultVerbosity),
		serviceTierAlias(upstream+"-fast", upstream, "low", DefaultVerbosity, "priority"),
		serviceTierAlias(upstream+"-fast-low", upstream, "low", DefaultVerbosity, "priority"),
		serviceTierAlias(upstream+"-fast-medium", upstream, "medium", DefaultVerbosity, "priority"),
		serviceTierAlias(upstream+"-fast-high", upstream, "high", DefaultVerbosity, "priority"),
		serviceTierAlias(upstream+"-fast-xhigh", upstream, "xhigh", DefaultVerbosity, "priority"),
	}
}

func buildModelAliases() []ModelAlias {
	out := []ModelAlias{
		// Default / GPT-5.6 family (ChatGPT/Codex path; ~272K context, not 1.05M).
		{
			ID:              DefaultModel,
			UpstreamModel:   DefaultModel,
			ReasoningEffort: "low", // Codex CLI default for Sol
			Verbosity:       gpt56DefaultVerbosity,
		},
		alias("gpt-5.6", DefaultModel, DefaultReasoningEffort, gpt56DefaultVerbosity),
		alias("gpt-5.6-sol-low", DefaultModel, "low", gpt56DefaultVerbosity),
		alias("gpt-5.6-sol-medium", DefaultModel, "medium", gpt56DefaultVerbosity),
		alias("gpt-5.6-sol-high", DefaultModel, "high", gpt56DefaultVerbosity),
		alias("gpt-5.6-sol-xhigh", DefaultModel, "xhigh", gpt56DefaultVerbosity),
		alias("gpt-5.6-sol-max", DefaultModel, "max", gpt56DefaultVerbosity),
	}
	out = append(out, codexFastEffortLadder(DefaultModel)...)
	out = append(out, codexEffortLadder("gpt-6.1-sol")...)
	out = append(out, codexFastEffortLadder("gpt-6.1-sol")...)
	out = append(out, astraEffortLadders()...)
	out = append(out, codexEffortLadder("gpt-6-sol")...)
	out = append(out, codexFastEffortLadder("gpt-6-sol")...)
	out = append(out, codexEffortLadder("gpt-6-luna")...)
	out = append(out, codexFastEffortLadder("gpt-6-luna")...)
	out = append(out, codexEffortLadder("gpt-5.6-terra")...)
	out = append(out, codexFastEffortLadder("gpt-5.6-terra")...)
	out = append(out, codexEffortLadder("gpt-5.6-luna")...)
	out = append(out, codexFastEffortLadder("gpt-5.6-luna")...)
	out = append(out, gpt54EffortLadders()...)
	out = append(out,
		alias("codex-sol", DefaultModel, "low", gpt56DefaultVerbosity),
		alias("codex-terra", "gpt-5.6-terra", DefaultReasoningEffort, gpt56DefaultVerbosity),
		alias("codex-luna", "gpt-5.6-luna", DefaultReasoningEffort, gpt56DefaultVerbosity),
		alias("gemini-2.5-flash", "gemini-2.5-flash", DefaultReasoningEffort, DefaultVerbosity),
		alias("gemini-2.5-flash-lite", "gemini-2.5-flash-lite", DefaultReasoningEffort, DefaultVerbosity),
		alias("gemini-2.5-pro", "gemini-2.5-pro", DefaultReasoningEffort, DefaultVerbosity),
		// Antigravity Gemini 3.x — public IDs match agy where possible; some
		// remap to the wire IDs Cloud Code Assist actually accepts.
		alias("gemini-3.1-pro-low", "gemini-3.1-pro-low", DefaultReasoningEffort, DefaultVerbosity),
		alias("gemini-3.1-pro-high", "gemini-pro-agent", DefaultReasoningEffort, DefaultVerbosity),
		alias("gemini-3.5-flash-low", "gemini-3.5-flash-extra-low", DefaultReasoningEffort, DefaultVerbosity),
		alias("gemini-3.5-flash-medium", "gemini-3.5-flash-low", DefaultReasoningEffort, DefaultVerbosity),
		alias("gemini-3.5-flash-high", "gemini-3-flash-agent", DefaultReasoningEffort, DefaultVerbosity),
		alias("gemini-3.1-flash-lite", "gemini-3.1-flash-lite", DefaultReasoningEffort, DefaultVerbosity),
		alias("gemini-3-flash", "gemini-3-flash", DefaultReasoningEffort, DefaultVerbosity),
		// Antigravity gateway non-Gemini models (routed via Gemini provider).
		alias("claude-sonnet-4-6", "claude-sonnet-4-6", DefaultReasoningEffort, DefaultVerbosity),
		alias("claude-opus-4-6-thinking", "claude-opus-4-6-thinking", DefaultReasoningEffort, DefaultVerbosity),
		alias("gpt-oss-120b-medium", "gpt-oss-120b-medium", DefaultReasoningEffort, DefaultVerbosity),
		// Claude Code CLI short names float to the CLI's latest model. No
		// effort: the CLI applies each model's own default.
		alias("opus", "opus", "", DefaultVerbosity),
		alias("sonnet", "sonnet", "", DefaultVerbosity),
		alias("haiku", "haiku", "", DefaultVerbosity),
		alias("fable", "fable", "", DefaultVerbosity),
	)
	// Claude Code's per-model effort and speed matrix; no guessed cross-product.
	for _, known := range modelCapabilities {
		if known.Claude {
			out = append(out, claudeEffortLadder(known.Model)...)
		}
	}
	out = append(out,
		alias("api/claude-haiku-4-5", "claude-haiku-4-5-20251001", "", DefaultVerbosity),
		alias("anthropic/claude-haiku-4-5", "claude-haiku-4-5-20251001", "", DefaultVerbosity),
		alias("api/claude-opus-4-5", "claude-opus-4-5-20251101", "", DefaultVerbosity),
		alias("api/claude-sonnet-4-5", "claude-sonnet-4-5-20250929", "", DefaultVerbosity),
	)

	// Legacy GPT-5.5 — upstream pinned so DefaultModel cutover does not remap them.
	out = append(out,
		alias(LegacyGPT55, LegacyGPT55, DefaultReasoningEffort, DefaultVerbosity),
		alias("gpt-5.5-low", LegacyGPT55, "low", DefaultVerbosity),
		alias("gpt-5.5-medium", LegacyGPT55, "medium", DefaultVerbosity),
		alias("gpt-5.5-xhigh", LegacyGPT55, "xhigh", DefaultVerbosity),
		serviceTierAlias("gpt-5.5-fast-xhigh", LegacyGPT55, "xhigh", DefaultVerbosity, "priority"),
		alias("gpt-5.5-high", LegacyGPT55, "high", DefaultVerbosity),
		serviceTierAlias("gpt-5.5-fast", LegacyGPT55, "low", "low", "priority"),
		serviceTierAlias("gpt-5.5-fast-low", LegacyGPT55, "low", DefaultVerbosity, "priority"),
		serviceTierAlias("gpt-5.5-fast-medium", LegacyGPT55, "medium", DefaultVerbosity, "priority"),
		serviceTierAlias("gpt-5.5-fast-high", LegacyGPT55, "high", DefaultVerbosity, "priority"),
		alias("gpt-5.5-mini", LegacyGPT55, "low", "low"),
		alias("gpt-5.5-lite", LegacyGPT55, "low", DefaultVerbosity),
		alias("gpt-5.5-deep", LegacyGPT55, "high", DefaultVerbosity),
		alias("gpt-5.5-verbose", LegacyGPT55, DefaultReasoningEffort, "high"),
		serviceTierAlias("gpt-5.5-fast-verbose", LegacyGPT55, "low", "high", "priority"),
	)

	// Spark: ultra-fast overflow / Cursor alias (96 KiB hard context).
	out = append(out,
		ModelAlias{
			ID:                  "gpt-5.3-codex-spark",
			UpstreamModel:       "gpt-5.3-codex-spark",
			ReasoningEffort:     "low",
			Verbosity:           "low",
			ContextHardMaxBytes: 96 * 1024,
		},
		ModelAlias{
			ID:                  "gpt-5.3-codex-spark-preview",
			UpstreamModel:       "gpt-5.3-codex-spark",
			ReasoningEffort:     "low",
			Verbosity:           "low",
			ContextHardMaxBytes: 96 * 1024,
		},
	)
	for _, name := range []string{"claude-haiku-4-5", "claude-opus-4-5", "claude-sonnet-4-5"} {
		known, _ := capability(name)
		for _, current := range claudeEffortLadder(known.Model) {
			replacement := current
			replacement.ID = strings.Replace(current.ID, known.Model, name, 1)
			exists := false
			for _, prior := range out {
				if prior.ID == replacement.ID {
					exists = true
					break
				}
			}
			if !exists {
				out = append(out, replacement)
			}
		}
	}
	for _, short := range []string{"opus", "sonnet", "fable", "haiku"} {
		known, _ := capability(short)
		for _, current := range claudeEffortLadder(known.Model) {
			if current.ID == known.Model || strings.HasPrefix(current.ID, "api/") || strings.HasPrefix(current.ID, "anthropic/") {
				continue
			}
			current.ID = strings.Replace(current.ID, known.Model, short, 1)
			out = append(out, current)
		}
	}
	// Explicit normal aliases preserve the base effort; fast remains independent.
	initial := append([]ModelAlias(nil), out...)
	seen := map[string]bool{}
	for _, current := range out {
		seen[current.ID] = true
	}
	for _, current := range initial {
		known, reviewed := capability(current.UpstreamModel)
		if !reviewed || current.ServiceTier != "" || current.Speed == "fast" || current.ID == "claude-sonnet-4-6" {
			continue
		}
		base := current.ID
		suffix := ""
		for _, effort := range known.Efforts {
			if strings.HasSuffix(base, "-"+effort) {
				base = strings.TrimSuffix(base, "-"+effort)
				suffix = "-" + effort
				break
			}
		}
		normal := current
		normal.ID = base + "-normal" + suffix
		normal.Speed = "normal"
		if current.UpstreamModel == "opus" || current.UpstreamModel == "sonnet" || current.UpstreamModel == "haiku" || current.UpstreamModel == "fable" {
			normal.UpstreamModel = known.Model
		}
		if !seen[normal.ID] {
			seen[normal.ID] = true
			out = append(out, normal)
		}
	}

	return out
}

var modelAliases = buildModelAliases()

func ModelAliases() []ModelAlias {
	aliases := make([]ModelAlias, len(modelAliases))
	copy(aliases, modelAliases)
	return aliases
}

// ListedModelAliases returns aliases exposed by GET /v1/models.
func ListedModelAliases() []ModelAlias {
	aliases := ModelAliases()
	out := make([]ModelAlias, 0, len(aliases))
	for _, alias := range aliases {
		if alias.Unlisted {
			continue
		}
		out = append(out, alias)
	}
	return out
}

func ResolveModelAlias(model string) ModelAlias {
	if model == "" {
		model = DefaultModel
	}
	for _, a := range modelAliases {
		if a.ID == model {
			return a
		}
	}
	effort := DefaultReasoningEffort
	if strings.Contains(model, "claude-") {
		effort = ""
	}
	return ModelAlias{
		ID:              model,
		UpstreamModel:   model,
		ReasoningEffort: effort,
		Verbosity:       DefaultVerbosity,
	}
}
