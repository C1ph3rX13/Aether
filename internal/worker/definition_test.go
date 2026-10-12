package worker

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestAdapterDefinitionsShareOneContract(t *testing.T) {
	tests := []struct {
		name                WorkerType
		modelEnvironment    string
		requiredEnvironment []string
		requiredSecrets     []string
		smoke               []string
		preparesSession     bool
		hasResultEvidence   bool
		concludes           bool
		providerMode        providerHealthMode
		environment         map[string]string
		baseURLVariable     string
		providerPath        string
	}{
		{
			name: WorkerTypeClaudeCode, modelEnvironment: "ANTHROPIC_MODEL",
			requiredEnvironment: []string{"IS_SANDBOX", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL"},
			requiredSecrets:     []string{"ANTHROPIC_AUTH_TOKEN"}, smoke: []string{"claude", "--version"},
			preparesSession: true, hasResultEvidence: true, concludes: true, providerMode: providerHealthHTTP,
			environment: map[string]string{
				"IS_SANDBOX": "1", "ANTHROPIC_MODEL": "claude-test", "ANTHROPIC_AUTH_TOKEN": "provider-token",
			},
			baseURLVariable: "ANTHROPIC_BASE_URL", providerPath: "/v1/messages",
		},
		{
			name: WorkerTypeCodex, modelEnvironment: "CODEX_MODEL",
			requiredEnvironment: []string{"CODEX_BASE_URL", "CODEX_MODEL"},
			requiredSecrets:     []string{"OPENAI_API_KEY"}, smoke: []string{"codex", "--version"},
			hasResultEvidence: true, concludes: true, providerMode: providerHealthHTTP,
			environment: map[string]string{
				"CODEX_MODEL": "codex-test", "OPENAI_API_KEY": "provider-token",
			},
			baseURLVariable: "CODEX_BASE_URL", providerPath: "/responses",
		},
		{
			name: WorkerTypePi, modelEnvironment: "PI_MODEL",
			requiredEnvironment: []string{"PI_BASE_URL", "PI_MODEL", "PI_PROVIDER_API"},
			requiredSecrets:     []string{"PI_API_KEY"}, smoke: []string{"pi", "--version"},
			hasResultEvidence: true, concludes: true, providerMode: providerHealthHTTP,
			environment: map[string]string{
				"PI_MODEL": "pi-test", "PI_API_KEY": "provider-token", "PI_PROVIDER_API": "openai-completions",
			},
			baseURLVariable: "PI_BASE_URL", providerPath: "/chat/completions",
		},
		{
			name: WorkerTypeMock, smoke: []string{"python3", "--version"},
			preparesSession: true, hasResultEvidence: true, concludes: true, providerMode: providerHealthInProcess,
			environment: map[string]string{
				"MOCK_HEALTHCHECK": `{"delay":[0,0],"outcomes":{"ok":1}}`,
			},
		},
	}

	for _, test := range tests {
		t.Run(string(test.name), func(t *testing.T) {
			receivedRequests := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				receivedRequests++
				if request.Method != http.MethodPost || request.URL.Path != test.providerPath {
					t.Errorf("Provider request = %s %s, want POST %s", request.Method, request.URL.Path, test.providerPath)
				}
				response.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			environment := cloneEnvironment(test.environment)
			if test.baseURLVariable != "" {
				environment[test.baseURLVariable] = server.URL
			}
			definition, err := LookupAdapterDefinition(string(test.name))
			if err != nil {
				t.Fatal(err)
			}
			if definition.Name() != test.name {
				t.Fatalf("name = %q, want %q", definition.Name(), test.name)
			}
			if definition.ModelEnvironmentVariable() != test.modelEnvironment {
				t.Fatalf("model environment = %q, want %q", definition.ModelEnvironmentVariable(), test.modelEnvironment)
			}
			if got := definition.RequiredEnvironmentVariables(); !slices.Equal(got, test.requiredEnvironment) {
				t.Fatalf("required environment = %v, want %v", got, test.requiredEnvironment)
			}
			if got := definition.RequiredSecretVariables(); !slices.Equal(got, test.requiredSecrets) {
				t.Fatalf("required secrets = %v, want %v", got, test.requiredSecrets)
			}
			if got := definition.SmokeCommand(); !slices.Equal(got, test.smoke) {
				t.Fatalf("smoke command = %v, want %v", got, test.smoke)
			}
			if got, err := SmokeCommand(string(test.name)); err != nil || !slices.Equal(got, test.smoke) {
				t.Fatalf("public smoke command = %v, err = %v, want %v", got, err, test.smoke)
			}
			if (definition.driver.prepareSession != nil) != test.preparesSession ||
				(definition.driver.resultEvidence != nil) != test.hasResultEvidence ||
				definition.driver.hasConclude() != test.concludes {
				t.Fatalf("optional capabilities do not match the Adapter contract")
			}
			if definition.providerMode != test.providerMode {
				t.Fatalf("Provider mode = %q, want %q", definition.providerMode, test.providerMode)
			}
			for _, variable := range append(test.requiredEnvironment, test.requiredSecrets...) {
				if !definition.AllowsConfigurationVariable(variable) {
					t.Errorf("required configuration variable %s is not allowed", variable)
				}
			}
			if err := definition.ValidateDeploymentEnvironment("worker", environment); err != nil {
				t.Fatalf("deployment environment: %v", err)
			}

			secretVersionIDs := make(map[string]string, len(test.requiredSecrets))
			for _, variable := range test.requiredSecrets {
				secretVersionIDs[variable] = "PROVIDER_TOKEN"
			}
			source, err := CatalogSourceFromConfig(CatalogConfiguration{
				ImageReference: "registry.example/aether@sha256:" + strings.Repeat("a", 64), PromptGroup: "default",
				Workers: []WorkerConfig{{
					Name: "worker", Type: test.name, TaskTypes: []TaskType{TaskTypeReason}, MaxRunning: 1,
					Env: environment, SecretVersionIDs: secretVersionIDs,
				}},
			})
			if err != nil {
				t.Fatalf("deployment conversion: %v", err)
			}
			converted := source.Workers[0]
			if converted.Model != environment[test.modelEnvironment] || converted.Adapter != string(test.name) {
				t.Fatalf("converted Worker identity = %+v", converted)
			}
			if len(converted.SecretReferences) != len(test.requiredSecrets) {
				t.Fatalf("Secret References = %+v, want %d", converted.SecretReferences, len(test.requiredSecrets))
			}
			for _, variable := range test.requiredSecrets {
				if _, retained := converted.Environment[variable]; retained {
					t.Fatalf("secret %s was retained in Catalog environment", variable)
				}
			}

			catalog, err := NewCatalog(source)
			if err != nil {
				t.Fatalf("Catalog normalization: %v", err)
			}
			normalized, ok := catalog.Worker("worker")
			if !ok || normalized.Adapter != string(test.name) || normalized.Model != environment[test.modelEnvironment] {
				t.Fatalf("normalized Worker = %+v, found = %t", normalized, ok)
			}
			if err := providerCheckerForTest(t, "provider-token", time.Second).CheckProvider(t.Context(), normalized); err != nil {
				t.Fatalf("Provider Health: %v", err)
			}
			if test.providerMode == providerHealthHTTP && receivedRequests != 1 ||
				test.providerMode == providerHealthInProcess && receivedRequests != 0 {
				t.Fatalf("Provider requests = %d for mode %s", receivedRequests, test.providerMode)
			}

			binding := &recordingExecutionBinding{}
			executor, err := catalog.BindExecution(binding, ExecutionBindingRequest{
				Task: rundomain.TaskReason, WorkerName: "worker", MaxIntents: 3,
				BlackboardInput: &rundomain.BlackboardInput{
					Schema: rundomain.BlackboardInputSchema, Generation: 1, Revision: 1,
					Project: rundomain.InputProject{Title: "Project", Origin: "origin", Goal: "goal"},
				},
			}, rundomain.StartedRun{})
			if err != nil {
				t.Fatalf("Execution Binding: %v", err)
			}
			if executor == nil || binding.reason == nil || binding.reason.Adapter.TypeName() != string(test.name) {
				t.Fatalf("Execution Adapter = %v", binding.reason)
			}
		})
	}
}

func TestLookupAdapterDefinitionRejectsUnknownAdapter(t *testing.T) {
	if _, err := LookupAdapterDefinition("unknown"); err == nil {
		t.Fatal("unknown Adapter was accepted")
	}
}

func TestAdapterDefinitionRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		adapter     WorkerType
		environment map[string]string
		want        string
	}{
		{
			name: "missing Claude sandbox", adapter: WorkerTypeClaudeCode,
			environment: map[string]string{
				"ANTHROPIC_BASE_URL": "https://api.example.invalid", "ANTHROPIC_MODEL": "model",
				"ANTHROPIC_AUTH_TOKEN": "secret",
			},
			want: "IS_SANDBOX",
		},
		{
			name: "missing model", adapter: WorkerTypeCodex,
			environment: map[string]string{
				"CODEX_BASE_URL": "https://api.example.invalid", "OPENAI_API_KEY": "secret",
			},
			want: "CODEX_MODEL",
		},
		{
			name: "missing secret", adapter: WorkerTypeClaudeCode,
			environment: map[string]string{
				"IS_SANDBOX": "1", "ANTHROPIC_BASE_URL": "https://api.example.invalid", "ANTHROPIC_MODEL": "model",
			},
			want: "ANTHROPIC_AUTH_TOKEN",
		},
		{
			name: "invalid Pi context window", adapter: WorkerTypePi,
			environment: map[string]string{"PI_MODEL_CONTEXT_WINDOW": "zero"}, want: "PI_MODEL_CONTEXT_WINDOW",
		},
		{
			name: "invalid Mock behavior", adapter: WorkerTypeMock,
			environment: map[string]string{"MOCK_REASON": "not-json"}, want: "Mock configuration is invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition, err := LookupAdapterDefinition(string(test.adapter))
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "missing Claude sandbox" || test.name == "missing model" || test.name == "missing secret" {
				err = definition.ValidateDeploymentEnvironment("worker", test.environment)
			} else {
				err = definition.ValidateAdapterEnvironment("worker", test.environment)
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}

func TestAdapterDefinitionAllowsAgentSpecificEnvironment(t *testing.T) {
	definition, err := LookupAdapterDefinition(string(WorkerTypeClaudeCode))
	if err != nil {
		t.Fatal(err)
	}
	for _, variable := range []string{"CODEX_BASE_URL", "CLAUDE_CODE_AUTO_COMPACT_WINDOW", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
		if !definition.AllowsConfigurationVariable(variable) {
			t.Errorf("Claude Code rejected Agent environment variable %s", variable)
		}
	}
	if definition.AllowsConfigurationVariable("AETHER_RESULT_PROTOCOL") {
		t.Fatal("Claude Code accepted an Aether-managed environment variable")
	}
}
