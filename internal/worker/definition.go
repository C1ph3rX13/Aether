package worker

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type providerHealthMode string

const (
	providerHealthHTTP      providerHealthMode = "http"
	providerHealthInProcess providerHealthMode = "in_process"
)

type providerHealthRequest struct {
	endpoint string
	token    string
	body     any
	headers  http.Header
}

// AdapterDefinition is the Worker-owned policy for one configured Adapter.
// Optional CLI capabilities remain absent when the underlying driver does not
// support them.
type AdapterDefinition struct {
	name                 WorkerType
	driver               driverCapabilities
	submission           submissionProtocol
	requiredEnvironment  []string
	requiredSecrets      []string
	validatedEnvironment []string
	modelEnvironment     string
	smokeCommand         []string
	providerMode         providerHealthMode
	providerRequest      func(map[string]string) providerHealthRequest
	checkProvider        func(CatalogWorker) error
	validateEnvironment  func(string, map[string]string) error
	decorateInvocation   func(map[string]string, Phase)
}

func (definition AdapterDefinition) Name() WorkerType { return definition.name }

func (definition AdapterDefinition) ModelEnvironmentVariable() string {
	return definition.modelEnvironment
}

func (definition AdapterDefinition) RequiredEnvironmentVariables() []string {
	return append([]string(nil), definition.requiredEnvironment...)
}

func (definition AdapterDefinition) RequiredSecretVariables() []string {
	return append([]string(nil), definition.requiredSecrets...)
}

func (definition AdapterDefinition) SmokeCommand() []string {
	return append([]string(nil), definition.smokeCommand...)
}

func (definition AdapterDefinition) AllowsConfigurationVariable(variable string) bool {
	return !reservedEnvironmentVariables[variable]
}

// ValidateDeploymentEnvironment applies Adapter-owned requirements before a
// deployment configuration is converted into an immutable Catalog.
func (definition AdapterDefinition) ValidateDeploymentEnvironment(workerName string, environment map[string]string) error {
	for _, variable := range append(definition.RequiredEnvironmentVariables(), definition.RequiredSecretVariables()...) {
		if strings.TrimSpace(environment[variable]) == "" {
			return fmt.Errorf("worker %s missing env key: %s", workerName, variable)
		}
	}
	return nil
}

// ValidateAdapterEnvironment applies optional Adapter-specific rules without
// requiring deployment-only credentials. Catalog validation remains the final
// authority for the complete immutable Worker.
func (definition AdapterDefinition) ValidateAdapterEnvironment(workerName string, environment map[string]string) error {
	return definition.validateOptionalEnvironment(workerName, environment)
}

func (definition AdapterDefinition) model(environment map[string]string) string {
	return environment[definition.modelEnvironment]
}

func (definition AdapterDefinition) catalogEnvironment(environment map[string]string) map[string]string {
	result := make(map[string]string, len(environment))
	for variable, value := range environment {
		if !definition.isSecretEnvironmentVariable(variable) {
			result[variable] = value
		}
	}
	return result
}

func (definition AdapterDefinition) secretReferences(config WorkerConfig) ([]rundomain.SecretReference, error) {
	references := make([]rundomain.SecretReference, 0, len(definition.requiredSecrets))
	for _, variable := range definition.requiredSecrets {
		if strings.TrimSpace(config.Env[variable]) == "" {
			return nil, fmt.Errorf("worker %q is missing secret environment variable %s", config.Name, variable)
		}
		secretKey := config.SecretVersionIDs[variable]
		if secretKey == "" {
			secretKey = variable
		}
		references = append(references, rundomain.SecretReference{
			EnvironmentVariable: variable, Provider: "configuration", Key: secretKey,
		})
	}
	return references, nil
}

func (definition AdapterDefinition) isSecretEnvironmentVariable(variable string) bool {
	for _, secret := range definition.requiredSecrets {
		if secret == variable {
			return true
		}
	}
	return false
}

func (definition AdapterDefinition) isValidatedEnvironmentVariable(variable string) bool {
	for _, candidate := range definition.validatedEnvironment {
		if candidate == variable {
			return true
		}
	}
	return false
}

func (definition AdapterDefinition) validateConfiguredEnvironment(workerName string, environment map[string]string) error {
	for _, variable := range definition.requiredEnvironment {
		value := strings.TrimSpace(environment[variable])
		if value == "" {
			return fmt.Errorf("Worker %q is missing required environment variable %s", workerName, variable)
		}
		if strings.HasSuffix(variable, "BASE_URL") {
			parsed, err := url.Parse(value)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
				parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
				return fmt.Errorf("Worker %q environment variable %s is not a valid HTTP URL", workerName, variable)
			}
		}
	}
	if definition.validateEnvironment != nil {
		return definition.validateEnvironment(workerName, environment)
	}
	return nil
}

func (definition AdapterDefinition) validateOptionalEnvironment(workerName string, environment map[string]string) error {
	if definition.validateEnvironment == nil {
		return nil
	}
	return definition.validateEnvironment(workerName, environment)
}

var adapterDefinitions = map[string]AdapterDefinition{
	string(WorkerTypeClaudeCode): newAdapterDefinition(adapterDefinitionInput{
		name: WorkerTypeClaudeCode, driver: NewClaudeCodeDriver(),
		validatedEnvironment: []string{
			"IS_SANDBOX", "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN",
		},
		requiredEnvironment: []string{"IS_SANDBOX", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL"},
		requiredSecrets:     []string{"ANTHROPIC_AUTH_TOKEN"},
		modelEnvironment:    "ANTHROPIC_MODEL",
		smokeCommand:        []string{"claude", "--version"},
		providerMode:        providerHealthHTTP,
		providerRequest:     claudeProviderHealthRequest,
	}),
	string(WorkerTypeCodex): newAdapterDefinition(adapterDefinitionInput{
		name:                 WorkerTypeCodex,
		driver:               NewCodexDriver(),
		validatedEnvironment: []string{"CODEX_MODEL", "CODEX_BASE_URL", "OPENAI_API_KEY"},
		requiredEnvironment:  []string{"CODEX_BASE_URL", "CODEX_MODEL"},
		requiredSecrets:      []string{"OPENAI_API_KEY"},
		modelEnvironment:     "CODEX_MODEL",
		smokeCommand:         []string{"codex", "--version"},
		providerMode:         providerHealthHTTP,
		providerRequest:      codexProviderHealthRequest,
	}),
	string(WorkerTypePi): newAdapterDefinition(adapterDefinitionInput{
		name: WorkerTypePi, driver: NewPiDriver(),
		validatedEnvironment: []string{
			"PI_BASE_URL", "PI_MODEL", "PI_PROVIDER_API", "PI_MODEL_CONTEXT_WINDOW", "PI_API_KEY",
		},
		requiredEnvironment: []string{"PI_BASE_URL", "PI_MODEL", "PI_PROVIDER_API"},
		requiredSecrets:     []string{"PI_API_KEY"},
		modelEnvironment:    "PI_MODEL",
		smokeCommand:        []string{"pi", "--version"},
		providerMode:        providerHealthHTTP,
		providerRequest:     piProviderHealthRequest,
		validateEnvironment: validatePiEnvironment,
	}),
	string(WorkerTypeMock): newAdapterDefinition(adapterDefinitionInput{
		name: WorkerTypeMock, driver: NewMockDriver(),
		smokeCommand:        []string{"python3", "--version"},
		providerMode:        providerHealthInProcess,
		checkProvider:       checkMockProvider,
		validateEnvironment: validateMockEnvironment,
		decorateInvocation:  decorateMockInvocation,
	}),
}

type adapterDefinitionInput struct {
	name                 WorkerType
	driver               invocationBuilder
	requiredEnvironment  []string
	requiredSecrets      []string
	validatedEnvironment []string
	modelEnvironment     string
	smokeCommand         []string
	providerMode         providerHealthMode
	providerRequest      func(map[string]string) providerHealthRequest
	checkProvider        func(CatalogWorker) error
	validateEnvironment  func(string, map[string]string) error
	decorateInvocation   func(map[string]string, Phase)
}

func newAdapterDefinition(input adapterDefinitionInput) AdapterDefinition {
	driver := newDriverCapabilities(input.name, input.driver)
	return AdapterDefinition{
		name: input.name, driver: driver, submission: input.driver,
		requiredEnvironment:  append([]string(nil), input.requiredEnvironment...),
		requiredSecrets:      append([]string(nil), input.requiredSecrets...),
		validatedEnvironment: append([]string(nil), input.validatedEnvironment...),
		modelEnvironment:     input.modelEnvironment, smokeCommand: append([]string(nil), input.smokeCommand...),
		providerMode: input.providerMode, providerRequest: input.providerRequest,
		checkProvider: input.checkProvider, validateEnvironment: input.validateEnvironment,
		decorateInvocation: input.decorateInvocation,
	}
}

// LookupAdapterDefinition is the only string dispatch point for Worker
// Adapter identity.
func LookupAdapterDefinition(name string) (AdapterDefinition, error) {
	definition, ok := adapterDefinitions[name]
	if !ok {
		return AdapterDefinition{}, fmt.Errorf("unsupported Adapter %q", name)
	}
	return definition, nil
}

func SupportedAdapterNames() []string {
	names := make([]string, 0, len(adapterDefinitions))
	for name := range adapterDefinitions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newDriverCapabilities(name WorkerType, driver invocationBuilder) driverCapabilities {
	capabilities := driverCapabilities{
		typeName: string(name), buildExecute: driver.BuildExecute,
	}
	if session, ok := driver.(sessionPreparer); ok {
		capabilities.prepareSession = session.PrepareSession
	}
	capabilities.resultEvidence = driver.ResultEvidence()
	if recovery, ok := driver.(recoveryBuilder); ok {
		capabilities.buildRecovery = recovery.BuildRecovery
	}
	if conclude, ok := driver.(concludeBuilder); ok {
		capabilities.buildConclude = conclude.BuildConclude
	}
	return capabilities
}

func claudeProviderHealthRequest(environment map[string]string) providerHealthRequest {
	headers := make(http.Header)
	headers.Set("anthropic-version", "2023-06-01")
	return providerHealthRequest{
		endpoint: adapterProviderEndpoint(environment["ANTHROPIC_BASE_URL"], "/v1/messages"),
		token:    environment["ANTHROPIC_AUTH_TOKEN"], headers: headers,
		body: map[string]any{
			"model": environment["ANTHROPIC_MODEL"], "max_tokens": 10,
			"messages": []map[string]string{{"role": "user", "content": "ping"}},
		},
	}
}

func codexProviderHealthRequest(environment map[string]string) providerHealthRequest {
	return providerHealthRequest{
		endpoint: adapterProviderEndpoint(environment["CODEX_BASE_URL"], "/responses"),
		token:    environment["OPENAI_API_KEY"], headers: make(http.Header),
		body: map[string]any{
			"model": environment["CODEX_MODEL"], "stream": false,
			"input": []map[string]string{{"role": "user", "content": "ping"}},
		},
	}
}

func piProviderHealthRequest(environment map[string]string) providerHealthRequest {
	baseURL := environment["PI_BASE_URL"]
	model := environment["PI_MODEL"]
	providerAPI := environment["PI_PROVIDER_API"]
	messages := []map[string]string{{"role": "user", "content": "ping"}}
	headers := make(http.Header)
	request := providerHealthRequest{token: environment["PI_API_KEY"], headers: headers}
	switch {
	case strings.Contains(providerAPI, "anthropic"):
		request.endpoint = adapterProviderEndpoint(baseURL, "/v1/messages")
		request.body = map[string]any{"model": model, "max_tokens": 10, "messages": messages}
		headers.Set("anthropic-version", "2023-06-01")
	case strings.Contains(providerAPI, "responses"):
		request.endpoint = adapterProviderEndpoint(baseURL, "/responses")
		request.body = map[string]any{"model": model, "stream": false, "input": messages}
	default:
		request.endpoint = adapterProviderEndpoint(baseURL, "/chat/completions")
		request.body = map[string]any{"model": model, "max_tokens": 10, "messages": messages}
	}
	return request
}

func adapterProviderEndpoint(baseURL, path string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	return baseURL + path
}

func validatePiEnvironment(workerName string, environment map[string]string) error {
	if contextWindow := environment["PI_MODEL_CONTEXT_WINDOW"]; contextWindow != "" {
		value, err := strconv.Atoi(contextWindow)
		if err != nil || value <= 0 {
			return fmt.Errorf("Worker %q PI_MODEL_CONTEXT_WINDOW is invalid", workerName)
		}
	}
	return nil
}

func validateMockEnvironment(workerName string, environment map[string]string) error {
	if _, err := parseMockBehavior(environment); err != nil {
		return fmt.Errorf("Worker %q Mock configuration is invalid: %w", workerName, err)
	}
	return nil
}

func decorateMockInvocation(environment map[string]string, phase Phase) {
	environment["MOCK_PHASE"] = string(phase)
	environment["AETHER_RESULT_PROTOCOL"] = OutcomeSubmissionSchemaV1
}
