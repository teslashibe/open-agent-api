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
