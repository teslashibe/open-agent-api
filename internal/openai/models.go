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
		ID:              "claude-fable-5",
		UpstreamModel:   "claude-fable-5",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-mythos-5",
		UpstreamModel:   "claude-mythos-5",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-opus-4-6",
		UpstreamModel:   "claude-opus-4-6",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-opus-4-6-fast",
		UpstreamModel:   "claude-opus-4-6-fast",
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
		ID:              "claude-opus-4-5",
		UpstreamModel:   "claude-opus-4-5",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-opus-4-1",
		UpstreamModel:   "claude-opus-4-1",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-opus-4",
		UpstreamModel:   "claude-opus-4",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-sonnet-4-6",
		UpstreamModel:   "claude-sonnet-4-6",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-sonnet-4-5",
		UpstreamModel:   "claude-sonnet-4-5",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-sonnet-4",
		UpstreamModel:   "claude-sonnet-4",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-haiku-4-5",
		UpstreamModel:   "claude-haiku-4-5",
		ReasoningEffort: DefaultReasoningEffort,
		Verbosity:       DefaultVerbosity,
	},
	{
		ID:              "claude-haiku-4",
		UpstreamModel:   "claude-haiku-4",
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
		ID:              "mythos",
		UpstreamModel:   "mythos",
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
	{
		ID:              "gpt-5.3-codex-spark",
		UpstreamModel:   DefaultModel,
		ReasoningEffort: "low",
		Verbosity:       "low",
	},
	{
		ID:              "gpt-5.3-codex-spark-preview",
		UpstreamModel:   DefaultModel,
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
