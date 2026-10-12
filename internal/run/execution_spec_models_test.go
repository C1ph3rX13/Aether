package run

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExecutionSpecV2JSONIsExplicitAndRoundTrips(t *testing.T) {
	spec := executionSpecFixture()
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, required := range []string{
		`"schema":"aether.execution-spec/v2"`,
		`"prompt":{"schema":"aether.prompt-template-hash/v1","sha256":"`,
		`"identity":{"schema":"aether.input-hash/v1","sha256":"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("v2 execution spec %s does not contain %s", text, required)
		}
	}
	if strings.Contains(text, "prompt_template_hash") || strings.Contains(text, `"prompt_hash"`) {
		t.Fatalf("v2 execution spec retained an ambiguous Prompt hash: %s", text)
	}
	var decoded ExecutionSpecSnapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Schema != ExecutionSpecSchemaV2 || decoded.PromptTemplateHash != spec.PromptTemplateHash ||
		decoded.Input.Identity != spec.Input.Identity {
		t.Fatalf("v2 execution spec round trip: got=%+v want=%+v", decoded, spec)
	}
}

func TestExecutionSpecRequiresResultFinalizationTimeoutBudget(t *testing.T) {
	spec := executionSpecFixture()
	spec.Timeouts.RunSeconds--
	if _, err := spec.normalized(); err == nil {
		t.Fatal("Execution Spec accepted a Run timeout without Result Finalization budget")
	}

	reason := executionSpecFixture()
	reason.Timeouts.FallbackPhaseSeconds = 0
	reason.Timeouts.RunSeconds = reason.Timeouts.PhaseSeconds + reason.Timeouts.GraceSeconds
	if _, err := reason.normalized(); err == nil {
		t.Fatal("Reason Execution Spec accepted a Run timeout without Result Finalization budget")
	}
}

func executionSpecFixture() ExecutionSpecSnapshot {
	target := "i001"
	return ExecutionSpecSnapshot{
		Schema:         ExecutionSpecSchemaV2,
		ConfigRevision: "config-v2", PromptTemplateHash: strings.Repeat("a", 64),
		Input: InputSnapshot{
			Generation: 2, Revision: 7, TargetIntentID: &target,
			Identity: InputIdentity{Schema: InputIdentitySchemaV1, SHA256: strings.Repeat("b", 64)},
		},
		Worker:       WorkerSnapshot{Name: "worker", Adapter: "mock", Capabilities: []string{"explore"}},
		Image:        ImageSnapshot{RequestedRef: "worker@sha256:digest", ImageID: "sha256:image", RepoDigest: "worker@sha256:digest"},
		Timeouts:     TimeoutSnapshot{RunSeconds: 18, PhaseSeconds: 5, FallbackPhaseSeconds: 5, GraceSeconds: 1},
		ResultSchema: ResultSchemaV1,
	}
}

func TestExecutionSpecJSONClassifiesLegacyWithoutPromotingItToV2(t *testing.T) {
	legacy := []byte(`{"config_revision":"legacy","prompt_hash":"` + strings.Repeat("a", 64) +
		`","input":{"generation":1,"revision":0,"hash":"` + strings.Repeat("b", 64) +
		`"},"worker":{"name":"worker","adapter":"mock"},"image":{"requested_ref":"worker:legacy"},` +
		`"timeouts":{"run_seconds":2,"phase_seconds":1,"grace_seconds":1},"result_schema":"aether.run-result/v1"}`)
	var decoded ExecutionSpecSnapshot
	if err := json.Unmarshal(legacy, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Schema != ExecutionSpecSchemaLegacyV1 || decoded.PromptTemplateHash != strings.Repeat("a", 64) ||
		decoded.Input.Identity.Schema != RenderedInputIdentitySchemaLegacyV1 {
		t.Fatalf("legacy execution spec classification: %+v", decoded)
	}
	if _, err := decoded.normalized(); err == nil {
		t.Fatal("legacy execution spec was promoted to an executable v2 spec")
	}
}

func TestExecutionSpecJSONRejectsDuplicateFields(t *testing.T) {
	duplicated := []byte(`{"schema":"aether.execution-spec/v2","schema":"aether.execution-spec/v2"}`)
	var decoded ExecutionSpecSnapshot
	if err := json.Unmarshal(duplicated, &decoded); err == nil {
		t.Fatal("execution spec accepted duplicate JSON fields")
	}
}

func TestExecutionSpecJSONRejectsCaseVariantFields(t *testing.T) {
	encoded, err := json.Marshal(executionSpecFixture())
	if err != nil {
		t.Fatal(err)
	}
	encoded = []byte(strings.Replace(string(encoded), `"name":"worker"`, `"name":"worker","Name":"worker"`, 1))
	var decoded ExecutionSpecSnapshot
	if err := json.Unmarshal(encoded, &decoded); err == nil {
		t.Fatal("Execution Spec accepted a case-variant Worker field")
	}
}

func TestDispatcherRejectsLegacyExecutionSpec(t *testing.T) {
	started, _, _ := reasonExecutorFixture(t)
	started.Run.Spec.Schema = ExecutionSpecSchemaLegacyV1
	started.Run.Spec.PromptEvidenceSchema = RenderedPromptIdentitySchemaLegacyV1
	if err := validateStartedExecution(started); err == nil {
		t.Fatal("new Dispatcher accepted a legacy Execution Spec")
	}
}

func TestDispatcherRejectsExecutionSpecWithoutExplicitSchema(t *testing.T) {
	started, _, _ := reasonExecutorFixture(t)
	started.Run.Spec.Schema = ""
	if err := validateStartedExecution(started); err == nil {
		t.Fatal("new Dispatcher inferred v2 for an unversioned Execution Spec")
	}
}

func TestExecutionSpecRejectsUppercasePromptDigestBeforePersistence(t *testing.T) {
	started, _, _ := reasonExecutorFixture(t)
	started.Run.Spec.PromptTemplateHash = strings.ToUpper(started.Run.Spec.PromptTemplateHash)
	if err := validateStartedExecution(started); err == nil {
		t.Fatal("Execution Spec accepted a non-canonical Prompt digest")
	}
}
