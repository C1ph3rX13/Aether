package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
	"github.com/C1ph3rX13/Aether/internal/testutil"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func TestRunExecutionFactorySplitsAdmissionPreparationFromExecutionBinding(t *testing.T) {
	catalog, err := worker.NewCatalog(worker.CatalogSource{
		ImageReference: "registry.example/aether@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Workers: []worker.CatalogWorker{
			{
				Name: "mock", Adapter: "mock", MaxRunning: 1,
				TaskTypes:    []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore, rundomain.TaskBootstrap},
				Capabilities: []string{"reason", "explore", "bootstrap"},
			},
			{Name: "reason-only", Adapter: "mock", MaxRunning: 1, TaskTypes: []rundomain.TaskType{rundomain.TaskReason}},
		},
		Prompts: map[rundomain.TaskType]worker.PromptSet{
			rundomain.TaskReason:    {Primary: "reason {max_intents}", Finalization: "finalize reason {max_intents}"},
			rundomain.TaskExplore:   {Primary: "explore {intent_id}: {intent_description}", Fallback: "conclude {intent_id}: {intent_description}", Finalization: "finalize {intent_id}: {intent_description}"},
			rundomain.TaskBootstrap: {Primary: "bootstrap {origin} {goal}", Fallback: "conclude {origin} {goal}", Finalization: "finalize {origin} {goal}"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := testutil.NewMemoryRuntime()
	factory, err := NewRunExecutionFactory(catalog, runtime, nil)
	if err != nil {
		t.Fatal(err)
	}
	targetID := "intent-1"
	blackboard := rundomain.BlackboardInput{
		Schema: rundomain.BlackboardInputSchema, Generation: 1, Revision: 2,
		Project: rundomain.InputProject{Title: "Project", Origin: "origin", Goal: "goal"},
		Intents: []rundomain.InputIntent{{
			ID: targetID, Description: "investigate", Creator: "human", Status: "open",
		}},
	}
	exploreInput := blackboard
	exploreInput.TargetIntentID = targetID
	bootstrapInput := rundomain.BootstrapInput{
		Schema: rundomain.BootstrapInputSchema, Generation: 1, Revision: 2,
		TargetIntentID: targetID, Origin: "origin", Goal: "goal",
	}
	image := rundomain.ImageSnapshot{
		RequestedRef: catalog.ImageReference(), ImageID: "sha256:image", RepoDigest: "registry.example/aether@sha256:digest",
	}

	for _, test := range []struct {
		task       rundomain.TaskType
		targetID   *string
		blackboard *rundomain.BlackboardInput
		bootstrap  *rundomain.BootstrapInput
		timeouts   rundomain.TimeoutSnapshot
	}{
		{task: rundomain.TaskReason, blackboard: &blackboard, timeouts: taskTimeouts(false)},
		{task: rundomain.TaskExplore, targetID: &targetID, blackboard: &exploreInput, timeouts: taskTimeouts(true)},
		{task: rundomain.TaskBootstrap, targetID: &targetID, bootstrap: &bootstrapInput, timeouts: taskTimeouts(true)},
	} {
		t.Run(string(test.task), func(t *testing.T) {
			workKey, err := rundomain.WorkKeyFor("project-1", test.task, rundomain.InputSnapshot{
				Generation: 1, Revision: 2, TargetIntentID: test.targetID,
			})
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := factory.PrepareAdmission(scheduler.AdmissionPreparation{
				ProjectID: "project-1", Task: test.task, TargetIntentID: test.targetID,
				CandidateGeneration: 1, CandidateRevision: 2, WorkKey: workKey,
				WorkerName: "mock", Image: image,
				DispatcherLease: rundomain.DispatcherLease{InstanceID: "dispatcher", FencingToken: 1},
				Timeouts:        test.timeouts, RetryPolicy: rundomain.DefaultRetryPolicy(), MaxIntents: 3,
			})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(prepared.Request)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), `"input"`) || strings.Contains(string(encoded), "blackboard") ||
				prepared.Binding == nil || prepared.Request.Spec.Worker.Name != "mock" {
				t.Fatalf("Admission preparation leaked Project input: %s", encoded)
			}

			admitted := admittedExecutionFixture(t, prepared.Request, test.blackboard, test.bootstrap)
			executor, err := prepared.Binding.BindExecution(t.Context(), admitted)
			if err != nil || executor == nil {
				t.Fatalf("bind admitted execution: executor=%v error=%v", executor, err)
			}
			drifted := admitted
			drifted.Run.Spec.ConfigRevision = "private-config-revision"
			_, err = prepared.Binding.BindExecution(t.Context(), drifted)
			assertExecutionContractMismatch(t, err, "private-config-revision", "Config Revision")

			drifted = admitted
			drifted.Run.Spec.PromptTemplateHash = strings.Repeat("f", 64)
			_, err = prepared.Binding.BindExecution(t.Context(), drifted)
			assertExecutionContractMismatch(t, err, strings.Repeat("f", 64), "Prompt Template identity")

			drifted = admitted
			drifted.Run.Spec.Input.Identity.SHA256 = strings.Repeat("e", 64)
			_, err = prepared.Binding.BindExecution(t.Context(), drifted)
			inputField := "Blackboard Input identity"
			if test.task == rundomain.TaskBootstrap {
				inputField = "Bootstrap Input identity"
			}
			assertExecutionContractMismatch(t, err, strings.Repeat("e", 64), inputField)

			drifted = admitted
			if test.task == rundomain.TaskBootstrap {
				drifted.BootstrapInput = nil
				drifted.BlackboardInput = &blackboard
			} else {
				drifted.BlackboardInput = nil
				drifted.BootstrapInput = &bootstrapInput
			}
			_, err = prepared.Binding.BindExecution(t.Context(), drifted)
			assertExecutionContractMismatch(t, err, blackboard.Project.Origin, "Task and Input discriminant")

			if test.targetID != nil {
				wrongTarget := "different-intent"
				drifted = admitted
				drifted.Run.TargetIntentID = &wrongTarget
				_, err = prepared.Binding.BindExecution(t.Context(), drifted)
				assertExecutionContractMismatch(t, err, wrongTarget, "Task or Target evidence")
			}

			if test.task == rundomain.TaskReason {
				for _, evidenceTest := range []struct {
					name   string
					field  string
					mutate func(*rundomain.AdmittedRun)
				}{
					{name: "Worker", field: "Worker evidence", mutate: func(run *rundomain.AdmittedRun) {
						run.Run.Spec.Worker.Model = "different-model"
					}},
					{name: "Image", field: "Image evidence", mutate: func(run *rundomain.AdmittedRun) {
						run.Run.Spec.Image.RequestedRef = "registry.example/different:latest"
					}},
					{name: "Timeout", field: "Timeout evidence", mutate: func(run *rundomain.AdmittedRun) {
						run.Run.Spec.Timeouts.PhaseSeconds++
					}},
					{name: "Result Schema", field: "Result Schema evidence", mutate: func(run *rundomain.AdmittedRun) {
						run.Run.Spec.ResultSchema = "aether.run-result/different"
					}},
					{name: "Max Proposed Intents", field: "Max Proposed Intents evidence", mutate: func(run *rundomain.AdmittedRun) {
						run.Run.Spec.MaxProposedIntents++
					}},
				} {
					t.Run(evidenceTest.name, func(t *testing.T) {
						drifted := admitted
						evidenceTest.mutate(&drifted)
						_, bindErr := prepared.Binding.BindExecution(t.Context(), drifted)
						assertExecutionContractMismatch(t, bindErr, "different", evidenceTest.field)
					})
				}
			}
		})
	}
	if _, err := factory.PrepareAdmission(scheduler.AdmissionPreparation{
		ProjectID: "project-1", Task: rundomain.TaskExplore, TargetIntentID: &targetID,
		CandidateGeneration: 1, CandidateRevision: 2,
		WorkKey: "intent:project-1:intent-1:explore", WorkerName: "reason-only", Image: image,
		DispatcherLease: rundomain.DispatcherLease{InstanceID: "dispatcher", FencingToken: 1},
		Timeouts:        taskTimeouts(true), RetryPolicy: rundomain.DefaultRetryPolicy(),
	}); err == nil {
		t.Fatal("Admission Preparation accepted a Worker that does not support the candidate Task")
	}
}

func assertExecutionContractMismatch(t *testing.T, err error, sensitive string, expectedFields ...string) {
	t.Helper()
	var domainErr *rundomain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != rundomain.ErrorExecutionContractMismatch {
		t.Fatalf("Execution Binding mismatch classification: %v", err)
	}
	if sensitive != "" && strings.Contains(err.Error(), sensitive) {
		t.Fatalf("Execution Binding mismatch exposed admitted evidence %q: %v", sensitive, err)
	}
	for _, field := range expectedFields {
		if !strings.Contains(domainErr.Detail, field) {
			t.Fatalf("Execution Binding mismatch omitted field %q: %v", field, err)
		}
	}
}

func admittedExecutionFixture(
	t *testing.T,
	request rundomain.StartRequest,
	blackboard *rundomain.BlackboardInput,
	bootstrap *rundomain.BootstrapInput,
) rundomain.AdmittedRun {
	t.Helper()
	var identity rundomain.InputIdentity
	var err error
	if blackboard != nil {
		identity, err = blackboard.InputIdentityV1()
	} else {
		identity, err = bootstrap.InputIdentityV1()
	}
	if err != nil {
		t.Fatal(err)
	}
	input := rundomain.InputSnapshot{
		Generation: request.CandidateGeneration, Revision: request.CandidateRevision,
		Identity: identity, TargetIntentID: request.TargetIntentID,
	}
	scope, err := rundomain.ClaimScopeFor(request.ProjectID, request.Task, input)
	if err != nil {
		t.Fatal(err)
	}
	spec := rundomain.ExecutionSpecSnapshot{
		Schema:         rundomain.ExecutionSpecSchemaV2,
		ConfigRevision: request.Spec.ConfigRevision, PromptTemplateHash: request.Spec.PromptTemplateHash,
		Input: input, Worker: request.Spec.Worker, Image: request.Spec.Image,
		Timeouts: request.Spec.Timeouts, ResultSchema: request.Spec.ResultSchema,
		MaxProposedIntents: request.Spec.MaxProposedIntents,
	}
	now := time.Now().UTC()
	return rundomain.AdmittedRun{
		StartedRun: rundomain.StartedRun{
			Run: rundomain.ExecutionRun{
				ID: "run-1", ProjectID: request.ProjectID, TargetIntentID: request.TargetIntentID,
				WorkKey: request.WorkKey, ClaimScope: scope, Status: rundomain.StatusActive,
				FencingToken: 1, DispatcherID: request.DispatcherID,
				DispatcherFencingToken: request.DispatcherFencingToken, Task: request.Task,
				Spec: spec, StartedAt: now,
			},
			Claim: rundomain.Claim{
				WorkKey: request.WorkKey, Scope: scope, RunID: "run-1", ProjectID: request.ProjectID,
				TargetIntentID: request.TargetIntentID, Task: request.Task, FencingToken: 1,
				DispatcherID: request.DispatcherID, ClaimedAt: now, LeaseExpiresAt: now.Add(time.Minute),
			},
		},
		BlackboardInput: blackboard,
		BootstrapInput:  bootstrap,
	}
}

func taskTimeouts(fallback bool) rundomain.TimeoutSnapshot {
	result := rundomain.TimeoutSnapshot{RunSeconds: 12, PhaseSeconds: 5, GraceSeconds: 1}
	if fallback {
		result.RunSeconds = 10
		result.FallbackPhaseSeconds = 1
	}
	return result
}
