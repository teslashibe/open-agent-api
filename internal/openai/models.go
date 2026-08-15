package openai

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

// gpt56EffortLadder returns bare + effort aliases for a GPT-5.6 tier.
// Upstream accepts none|minimal|low|medium|high|xhigh|max — not "ultra"
// (ultra is a Codex product multi-agent mode, not a reasoning.effort value).
func gpt56EffortLadder(upstream string) []ModelAlias {
	return []ModelAlias{
		alias(upstream, upstream, DefaultReasoningEffort, gpt56DefaultVerbosity),
		alias(upstream+"-low", upstream, "low", gpt56DefaultVerbosity),
		alias(upstream+"-medium", upstream, "medium", gpt56DefaultVerbosity),
		alias(upstream+"-high", upstream, "high", gpt56DefaultVerbosity),
		alias(upstream+"-xhigh", upstream, "xhigh", gpt56DefaultVerbosity),
		alias(upstream+"-max", upstream, "max", gpt56DefaultVerbosity),
	}
}

// gpt56FastEffortLadder returns priority-tier aliases for every exposed effort.
func gpt56FastEffortLadder(upstream string) []ModelAlias {
	return []ModelAlias{
		serviceTierAlias(upstream+"-fast", upstream, "low", gpt56DefaultVerbosity, "priority"),
		serviceTierAlias(upstream+"-fast-low", upstream, "low", gpt56DefaultVerbosity, "priority"),
		serviceTierAlias(upstream+"-fast-medium", upstream, "medium", gpt56DefaultVerbosity, "priority"),
		serviceTierAlias(upstream+"-fast-high", upstream, "high", gpt56DefaultVerbosity, "priority"),
		serviceTierAlias(upstream+"-fast-xhigh", upstream, "xhigh", gpt56DefaultVerbosity, "priority"),
		serviceTierAlias(upstream+"-fast-max", upstream, "max", gpt56DefaultVerbosity, "priority"),
	}
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
	out = append(out, gpt56FastEffortLadder(DefaultModel)...)
	out = append(out, gpt56EffortLadder("gpt-5.6-terra")...)
	out = append(out, gpt56FastEffortLadder("gpt-5.6-terra")...)
	out = append(out, gpt56EffortLadder("gpt-5.6-luna")...)
	out = append(out, gpt56FastEffortLadder("gpt-5.6-luna")...)
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
		alias("claude-opus-4-8", "claude-opus-4-8", DefaultReasoningEffort, DefaultVerbosity),
		alias("claude-sonnet-5", "claude-sonnet-5", DefaultReasoningEffort, DefaultVerbosity),
		alias("claude-haiku-4-5-20251001", "claude-haiku-4-5-20251001", DefaultReasoningEffort, DefaultVerbosity),
		alias("claude-fable-5", "claude-fable-5", DefaultReasoningEffort, DefaultVerbosity),
		alias("opus", "opus", DefaultReasoningEffort, DefaultVerbosity),
		alias("sonnet", "sonnet", DefaultReasoningEffort, DefaultVerbosity),
		alias("haiku", "haiku", DefaultReasoningEffort, DefaultVerbosity),
		alias("fable", "fable", DefaultReasoningEffort, DefaultVerbosity),
		alias("api/claude-opus-4-8", "claude-opus-4-8", DefaultReasoningEffort, DefaultVerbosity),
		alias("api/claude-sonnet-5", "claude-sonnet-5", DefaultReasoningEffort, DefaultVerbosity),
		alias("api/claude-haiku-4-5-20251001", "claude-haiku-4-5-20251001", DefaultReasoningEffort, DefaultVerbosity),
		alias("api/claude-fable-5", "claude-fable-5", DefaultReasoningEffort, DefaultVerbosity),
		alias("api/claude-fable-5-low", "claude-fable-5", "low", DefaultVerbosity),
		alias("api/claude-fable-5-medium", "claude-fable-5", "medium", DefaultVerbosity),
		alias("api/claude-fable-5-high", "claude-fable-5", "high", DefaultVerbosity),
	)

	// Legacy GPT-5.5 — upstream pinned so DefaultModel cutover does not remap them.
	out = append(out,
		alias(LegacyGPT55, LegacyGPT55, DefaultReasoningEffort, DefaultVerbosity),
		alias("gpt-5.5-low", LegacyGPT55, "low", DefaultVerbosity),
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
	return ModelAlias{
		ID:              model,
		UpstreamModel:   model,
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	}
}
