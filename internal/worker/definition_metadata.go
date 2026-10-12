package worker

// AgentFieldMetadata describes one configuration value owned by an Agent
// Definition. Secret fields are never returned with a configured value.
type AgentFieldMetadata struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"display_name"`
	Secret      bool               `json:"secret"`
	Required    bool               `json:"required"`
	Options     []AgentFieldOption `json:"options,omitempty"`
}

type AgentFieldOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// AgentDefinitionMetadata is the stable configuration metadata exposed to the
// Configuration Editor. Runtime Adapter internals remain private.
type AgentDefinitionMetadata struct {
	Type                string               `json:"type"`
	DisplayName         string               `json:"display_name"`
	RequiredFields      []AgentFieldMetadata `json:"required_fields"`
	OptionalFields      []AgentFieldMetadata `json:"optional_fields"`
	Capabilities        []string             `json:"capabilities"`
	ResultContract      string               `json:"result_contract"`
	ProviderHealthcheck string               `json:"provider_healthcheck"`
}

// ProviderPreset is a read-only Worker initializer. Its values are copied
// into a normal Draft; it has no runtime identity and contains no Secret.
type ProviderPreset struct {
	ID           string            `json:"id"`
	DisplayName  string            `json:"display_name"`
	AgentType    string            `json:"agent_type"`
	ProviderName string            `json:"provider_name"`
	Protocol     string            `json:"protocol"`
	AgentEnv     map[string]string `json:"agent_env"`
}

func SupportedAgentDefinitionMetadata() []AgentDefinitionMetadata {
	return []AgentDefinitionMetadata{
		{
			Type: "claude_code", DisplayName: "Claude Code",
			RequiredFields: []AgentFieldMetadata{
				{Name: "IS_SANDBOX", DisplayName: "Sandbox", Required: true},
				{Name: "ANTHROPIC_BASE_URL", DisplayName: "Anthropic Base URL", Required: true},
				{Name: "ANTHROPIC_MODEL", DisplayName: "Model", Required: true},
				{Name: "ANTHROPIC_AUTH_TOKEN", DisplayName: "Anthropic Auth Token", Required: true, Secret: true},
			},
			Capabilities:        []string{"process_recovery"},
			ResultContract:      ResultOutputContractSchemaV2,
			ProviderHealthcheck: "http",
		},
		{
			Type: "pi", DisplayName: "Pi",
			RequiredFields: []AgentFieldMetadata{
				{Name: "PI_BASE_URL", DisplayName: "Provider Base URL", Required: true},
				{Name: "PI_MODEL", DisplayName: "Model", Required: true},
				{Name: "PI_PROVIDER_API", DisplayName: "Provider API", Required: true, Options: []AgentFieldOption{
					{Value: "anthropic-messages", Label: "Anthropic Messages"},
					{Value: "openai-responses", Label: "OpenAI Responses"},
					{Value: "openai-completions", Label: "OpenAI Chat Completions"},
				}},
				{Name: "PI_API_KEY", DisplayName: "Provider API Key", Required: true, Secret: true},
			},
			OptionalFields: []AgentFieldMetadata{
				{Name: "PI_MODEL_CONTEXT_WINDOW", DisplayName: "Model Context Window"},
			},
			Capabilities:        []string{"process_recovery"},
			ResultContract:      ResultOutputContractSchemaV2,
			ProviderHealthcheck: "http",
		},
		{
			Type: "codex", DisplayName: "Codex",
			RequiredFields: []AgentFieldMetadata{
				{Name: "CODEX_BASE_URL", DisplayName: "Responses Base URL", Required: true},
				{Name: "CODEX_MODEL", DisplayName: "Model", Required: true},
				{Name: "OPENAI_API_KEY", DisplayName: "OpenAI API Key", Required: true, Secret: true},
			},
			Capabilities:        []string{"process_recovery"},
			ResultContract:      ResultOutputContractSchemaV2,
			ProviderHealthcheck: "http",
		},
	}
}

func ProviderPresets() []ProviderPreset {
	return []ProviderPreset{
		{
			ID: "deepseek_claude_code", DisplayName: "DeepSeek + Claude Code", AgentType: "claude_code",
			ProviderName: "DeepSeek", Protocol: "anthropic-messages",
			AgentEnv: map[string]string{
				"IS_SANDBOX": "1", "ANTHROPIC_BASE_URL": "https://api.deepseek.com/anthropic",
				"ANTHROPIC_MODEL": "deepseek-flash",
			},
		},
		{
			ID: "deepseek_pi", DisplayName: "DeepSeek + Pi", AgentType: "pi",
			ProviderName: "DeepSeek", Protocol: "openai-completions",
			AgentEnv: map[string]string{
				"PI_BASE_URL": "https://api.deepseek.com", "PI_MODEL": "deepseek-flash",
				"PI_PROVIDER_API": "openai-completions",
			},
		},
		{
			ID: "deepseek_codex", DisplayName: "DeepSeek + Codex", AgentType: "codex",
			ProviderName: "DeepSeek", Protocol: "openai-responses",
			AgentEnv: map[string]string{
				"CODEX_BASE_URL": "https://api.deepseek.com", "CODEX_MODEL": "deepseek-flash",
			},
		},
	}
}
