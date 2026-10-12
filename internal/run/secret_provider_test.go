package run

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretResolverResolvesRedactsAndClearsValues(t *testing.T) {
	resolver, err := NewSecretResolver(map[string]SecretProvider{
		"configuration": testMapSecretProvider{"API_KEY": "super-secret-value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.ResolveSecrets(t.Context(), []SecretReference{{
		EnvironmentVariable: "OPENAI_API_KEY", Provider: "configuration", Key: "API_KEY",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Environment()["OPENAI_API_KEY"]; got != "super-secret-value" {
		t.Fatalf("resolved secret=%q", got)
	}
	if got := resolved.Redact("failed with super-secret-value"); got != "failed with [REDACTED]" {
		t.Fatalf("redacted message=%q", got)
	}
	if !resolved.Contains([]byte(`{"description":"super-secret-value"}`)) {
		t.Fatal("resolved Secret was not detected in structured content")
	}
	resolved.Close()
	if len(resolved.Environment()) != 0 {
		t.Fatal("closed Secret resolution retained environment values")
	}
}

func TestExecutorInjectsSecretOnlyIntoSingleRunEnvironment(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	started.Run.Spec.Worker.SecretReferences = []SecretReference{{
		EnvironmentVariable: "OPENAI_API_KEY", Provider: "test", Key: "worker-key",
	}}
	normalized, err := started.Run.Spec.normalized()
	if err != nil {
		t.Fatal(err)
	}
	started.Run.Spec = normalized
	provider := &countingSecretProvider{value: "single-run-secret"}
	resolver, err := NewSecretResolver(map[string]SecretProvider{"test": provider})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newRuntimeTestDouble()
	adapter := &testReasonAdapter{candidate: json.RawMessage(`{"schema":"aether.run-result/v1","outcome":{"kind":"no_change"}}`)}
	executor, err := newTestExecutionBinding(t, runtime, resolver, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionResultReady || provider.calls != 1 {
		t.Fatalf("secret-backed execution: completion=%+v calls=%d", completion, provider.calls)
	}
	launches := runtime.Snapshot().Launches
	if len(launches) != 1 || launches[0].Environment["OPENAI_API_KEY"] != "single-run-secret" {
		t.Fatalf("Run environment: %+v", launches)
	}
	snapshotJSON, err := json.Marshal(started.Run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snapshotJSON), "single-run-secret") ||
		!strings.Contains(string(snapshotJSON), `"key":"worker-key"`) {
		t.Fatalf("persisted snapshot leaked or lost Secret Reference: %s", snapshotJSON)
	}
}

func TestExecutorFailsBeforeLaunchWhenSecretCannotResolve(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	started.Run.Spec.Worker.SecretReferences = []SecretReference{{
		EnvironmentVariable: "OPENAI_API_KEY", Provider: "missing", Key: "worker-key",
	}}
	normalized, err := started.Run.Spec.normalized()
	if err != nil {
		t.Fatal(err)
	}
	started.Run.Spec = normalized
	resolver, err := NewSecretResolver(map[string]SecretProvider{"test": &countingSecretProvider{value: "unused"}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newRuntimeTestDouble()
	executor, err := newTestExecutionBinding(t, runtime, resolver, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash,
		Input: input, Adapter: &testReasonAdapter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionSecretFailed ||
		len(runtime.Snapshot().Launches) != 0 {
		t.Fatalf("failed Secret resolution: completion=%+v runtime=%+v", completion, runtime.Snapshot())
	}
}

func TestExecutorRejectsSecretValueInStructuredResult(t *testing.T) {
	started, input, prompt := reasonExecutorFixture(t)
	started.Run.Spec.Worker.SecretReferences = []SecretReference{{
		EnvironmentVariable: "OPENAI_API_KEY", Provider: "test", Key: "worker-key",
	}}
	normalized, err := started.Run.Spec.normalized()
	if err != nil {
		t.Fatal(err)
	}
	started.Run.Spec = normalized
	resolver, err := NewSecretResolver(map[string]SecretProvider{
		"test": &countingSecretProvider{value: "single-run-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newRuntimeTestDouble()
	adapter := &testReasonAdapter{candidate: json.RawMessage(
		`{"schema":"aether.run-result/v1","outcome":{"kind":"complete_project","description":"found single-run-secret","from":["origin"]}}`,
	)}
	executor, err := newTestExecutionBinding(t, runtime, resolver, nil, nil).BindReason(started, ReasonExecution{
		Prompt: prompt, PromptTemplateHash: started.Run.Spec.PromptTemplateHash, Input: input, Adapter: adapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	completion := executor.Execute(t.Context(), started)
	if completion.Kind != CompletionFailed || completion.Code != ExecutionSecretExposed || len(completion.Candidate) != 0 {
		t.Fatalf("secret-bearing result was not rejected: %+v", completion)
	}
}

type testMapSecretProvider map[string]string

func (provider testMapSecretProvider) ResolveSecret(_ context.Context, key string) ([]byte, error) {
	return []byte(provider[key]), nil
}

type countingSecretProvider struct {
	value string
	calls int
}

func (provider *countingSecretProvider) ResolveSecret(context.Context, string) ([]byte, error) {
	provider.calls++
	return []byte(provider.value), nil
}
