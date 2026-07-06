package openai

const (
	DefaultReasoningEffort = "medium"
	DefaultVerbosity       = "medium"
)

type ModelAlias struct {
	ID              string
	UpstreamModel   string
	ReasoningEffort string
	Verbosity       string
}

var modelAliases = []ModelAlias{
	{
		ID:              DefaultModel,
		UpstreamModel:   DefaultModel,
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "gemini-2.5-flash",
		UpstreamModel:   "gemini-2.5-flash",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "gemini-2.5-flash-lite",
		UpstreamModel:   "gemini-2.5-flash-lite",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "gemini-2.5-pro",
		UpstreamModel:   "gemini-2.5-pro",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-opus-4-8",
		UpstreamModel:   "claude-opus-4-8",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-sonnet-5",
		UpstreamModel:   "claude-sonnet-5",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-haiku-4-5-20251001",
		UpstreamModel:   "claude-haiku-4-5-20251001",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-fable-5",
		UpstreamModel:   "claude-fable-5",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "opus",
		UpstreamModel:   "opus",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "sonnet",
		UpstreamModel:   "sonnet",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "haiku",
		UpstreamModel:   "haiku",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "fable",
		UpstreamModel:   "fable",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "api/claude-opus-4-8",
		UpstreamModel:   "claude-opus-4-8",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "api/claude-sonnet-5",
		UpstreamModel:   "claude-sonnet-5",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "api/claude-haiku-4-5-20251001",
		UpstreamModel:   "claude-haiku-4-5-20251001",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "api/claude-fable-5",
		UpstreamModel:   "claude-fable-5",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "api/claude-fable-5-low",
		UpstreamModel:   "claude-fable-5",
		ReasoningEffort: "low",
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "api/claude-fable-5-medium",
		UpstreamModel:   "claude-fable-5",
		ReasoningEffort: "medium",
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "api/claude-fable-5-high",
		UpstreamModel:   "claude-fable-5",
		ReasoningEffort: "high",
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "gpt-5.5-low",
		UpstreamModel:   DefaultModel,
		ReasoningEffort: "low",
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "gpt-5.5-high",
		UpstreamModel:   DefaultModel,
		ReasoningEffort: "high",
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "gpt-5.5-fast",
		UpstreamModel:   DefaultModel,
		ReasoningEffort: "low",
		Verbosity:       "low",
	},
	{
		ID:              "gpt-5.5-mini",
		UpstreamModel:   DefaultModel,
		ReasoningEffort: "low",
		Verbosity:       "low",
	},
	{
		ID:              "gpt-5.5-lite",
		UpstreamModel:   DefaultModel,
		ReasoningEffort: "low",
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "gpt-5.5-deep",
		UpstreamModel:   DefaultModel,
		ReasoningEffort: "high",
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "gpt-5.5-verbose",
		UpstreamModel:   DefaultModel,
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       "high",
	},
	{
		ID:              "gpt-5.5-fast-verbose",
		UpstreamModel:   DefaultModel,
		ReasoningEffort: "low",
		Verbosity:       "high",
	},
	// The upstream supports the real Spark slug (verified via codex CLI);
	// the -preview id is Cursor-side only and maps to the same model.
	{
		ID:              "gpt-5.3-codex-spark",
		UpstreamModel:   "gpt-5.3-codex-spark",
		ReasoningEffort: "low",
		Verbosity:       "low",
	},
	{
		ID:              "gpt-5.3-codex-spark-preview",
		UpstreamModel:   "gpt-5.3-codex-spark",
		ReasoningEffort: "low",
		Verbosity:       "low",
	},
}

func ModelAliases() []ModelAlias {
	aliases := make([]ModelAlias, len(modelAliases))
	copy(aliases, modelAliases)
	return aliases
}

func ResolveModelAlias(model string) ModelAlias {
	if model == "" {
		model = DefaultModel
	}
	for _, alias := range modelAliases {
		if alias.ID == model {
			return alias
		}
	}
	return ModelAlias{
		ID:              model,
		UpstreamModel:   model,
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	}
}
