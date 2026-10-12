package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestCatalogBindsAdmittedInputToImmutableExecutionMaterialForEveryTask(t *testing.T) {
	catalog, err := NewCatalog(CatalogSource{
		ImageReference: "registry.example/aether@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Workers: []CatalogWorker{{
			Name: "mock", Adapter: "mock", MaxRunning: 1,
			TaskTypes:    []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore, rundomain.TaskBootstrap},
			Capabilities: []string{"reason", "explore", "bootstrap", rundomain.ProcessRecoveryCapability},
		}},
		Prompts: map[rundomain.TaskType]PromptSet{
			rundomain.TaskReason: {Primary: "reason {max_intents}", Finalization: "finalize reason {max_intents}"},
			rundomain.TaskExplore: {
				Primary: "explore {intent_id}: {intent_description}", Fallback: "conclude {intent_id}: {intent_description}",
				RecoveryPrimary: "recover {intent_id}: {intent_description}", RecoveryConclude: "recover conclude {intent_id}: {intent_description}",
				Finalization: "finalize {intent_id}: {intent_description}",
			},
			rundomain.TaskBootstrap: {
				Primary: "bootstrap {origin} {goal}", Fallback: "conclude {intent_id} {origin} {goal}",
				RecoveryPrimary: "recover {intent_id} {origin} {goal} {hints}", RecoveryConclude: "recover conclude {intent_id} {origin} {goal} {hints}",
				Finalization: "finalize {intent_id} {origin} {goal} {hints}",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := ExecutionTarget{ID: "intent-1", Description: "investigate"}
	blackboard := &rundomain.BlackboardInput{
		Schema: rundomain.BlackboardInputSchema, Generation: 1, Revision: 2,
		Project: rundomain.InputProject{Title: "Project", Origin: "origin", Goal: "goal"},
		Intents: []rundomain.InputIntent{{ID: target.ID, Description: target.Description, Creator: "human", Status: "open"}},
	}
	exploreInput := *blackboard
	exploreInput.TargetIntentID = target.ID
	bootstrap := &rundomain.BootstrapInput{
		Schema: rundomain.BootstrapInputSchema, Generation: 1, Revision: 2,
		TargetIntentID: target.ID, Origin: "origin", Goal: "goal",
	}
	for _, test := range []struct {
		task       rundomain.TaskType
		blackboard *rundomain.BlackboardInput
		bootstrap  *rundomain.BootstrapInput
	}{
		{task: rundomain.TaskReason, blackboard: blackboard},
		{task: rundomain.TaskExplore, blackboard: &exploreInput},
		{task: rundomain.TaskBootstrap, bootstrap: bootstrap},
	} {
		t.Run(string(test.task), func(t *testing.T) {
			binding := &recordingExecutionBinding{}
			executor, err := catalog.BindExecution(binding, ExecutionBindingRequest{
				Task: test.task, WorkerName: "mock",
				BlackboardInput: test.blackboard, BootstrapInput: test.bootstrap, MaxIntents: 3,
			}, rundomain.StartedRun{})
			if err != nil {
				t.Fatal(err)
			}
			if executor == nil {
				t.Fatal("Execution Binding returned no Executor")
			}
			wantTemplateHash, err := catalog.PromptTemplateHash(test.task)
			if err != nil {
				t.Fatal(err)
			}
			switch test.task {
			case rundomain.TaskReason:
				execution := binding.reason
				if execution == nil || binding.explore != nil || binding.bootstrap != nil ||
					execution.PromptTemplateHash != wantTemplateHash || execution.Prompt != "reason 3" ||
					execution.FinalizationPrompt != "finalize reason 3" ||
					execution.RenderedInput == nil || !execution.RenderedInput.Hash.Matches(execution.RenderedInput.Bytes) {
					t.Fatalf("Reason binding: execution=%+v", execution)
				}
			case rundomain.TaskExplore:
				execution := binding.explore
				if execution == nil || binding.reason != nil || binding.bootstrap != nil ||
					execution.PromptTemplateHash != wantTemplateHash || execution.RenderedInput == nil ||
					!execution.RenderedInput.Hash.Matches(execution.RenderedInput.Bytes) ||
					execution.Prompt != "explore intent-1: investigate" || execution.FallbackPrompt != "conclude intent-1: investigate" ||
					execution.RecoveryPrompt != "recover intent-1: investigate" ||
					execution.RecoveryConcludePrompt != "recover conclude intent-1: investigate" ||
					execution.FinalizationPrompt != "finalize intent-1: investigate" {
					t.Fatalf("Explore binding: execution=%+v", execution)
				}
			case rundomain.TaskBootstrap:
				execution := binding.bootstrap
				if execution == nil || binding.reason != nil || binding.explore != nil ||
					execution.PromptTemplateHash != wantTemplateHash || execution.RenderedInput == nil ||
					!execution.RenderedInput.Hash.Matches(execution.RenderedInput.Bytes) ||
					execution.Prompt != "bootstrap origin goal" || execution.FallbackPrompt != "conclude intent-1 origin goal" ||
					execution.RecoveryPrompt != "recover intent-1 origin goal []" ||
					execution.RecoveryConcludePrompt != "recover conclude intent-1 origin goal []" ||
					execution.FinalizationPrompt != "finalize intent-1 origin goal []" {
					t.Fatalf("Bootstrap binding: execution=%+v", execution)
				}
			}
		})
	}
}

func TestCatalogClassifiesExecutionMaterialEvidenceWithoutInputValues(t *testing.T) {
	catalog, err := NewCatalog(CatalogSource{
		ImageReference: "registry.example/aether@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Workers: []CatalogWorker{{
			Name: "mock", Adapter: "mock", MaxRunning: 1,
			TaskTypes: []rundomain.TaskType{rundomain.TaskReason, rundomain.TaskExplore},
		}},
		Prompts: map[rundomain.TaskType]PromptSet{
			rundomain.TaskReason:    {Primary: "reason", Finalization: "finalize reason"},
			rundomain.TaskExplore:   {Primary: "explore", Fallback: "conclude", Finalization: "finalize explore"},
			rundomain.TaskBootstrap: {Primary: "bootstrap", Fallback: "conclude", Finalization: "finalize bootstrap"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	privateInput := &rundomain.BlackboardInput{
		Schema: rundomain.BlackboardInputSchema, Generation: 1, Revision: 1,
		Project:        rundomain.InputProject{Title: "private title", Origin: "private origin", Goal: "private goal"},
		TargetIntentID: "missing-private-intent",
	}
	for _, test := range []struct {
		name    string
		catalog *Catalog
		request ExecutionBindingRequest
		want    ExecutionMaterialEvidence
	}{
		{name: "Worker", catalog: catalog, request: ExecutionBindingRequest{
			Task: rundomain.TaskReason, WorkerName: "missing-private-worker", BlackboardInput: privateInput,
		}, want: ExecutionMaterialWorker},
		{name: "Input discriminant", catalog: catalog, request: ExecutionBindingRequest{
			Task: rundomain.TaskReason, WorkerName: "mock",
		}, want: ExecutionMaterialInput},
		{name: "Target Intent", catalog: catalog, request: ExecutionBindingRequest{
			Task: rundomain.TaskExplore, WorkerName: "mock", BlackboardInput: privateInput,
		}, want: ExecutionMaterialTarget},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.catalog.BindExecution(&recordingExecutionBinding{}, test.request, rundomain.StartedRun{})
			var materialErr *ExecutionMaterialError
			if !errors.As(err, &materialErr) || materialErr.Evidence != test.want {
				t.Fatalf("Execution Material evidence: error=%v want=%s", err, test.want)
			}
			for _, sensitive := range []string{"private title", "private origin", "private goal", "missing-private-intent", "missing-private-worker"} {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatalf("Execution Material diagnostic exposed %q: %v", sensitive, err)
				}
			}
		})
	}
}

func TestPromptTemplateIdentityDoesNotDependOnAdmittedProjectInput(t *testing.T) {
	catalog, err := NewCatalog(CatalogSource{
		ImageReference: "registry.example/aether@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Workers:        []CatalogWorker{{Name: "mock", Adapter: "mock", MaxRunning: 1, TaskTypes: []rundomain.TaskType{rundomain.TaskReason}}},
		Prompts: map[rundomain.TaskType]PromptSet{
			rundomain.TaskReason:    {Primary: "reason {max_intents}", Finalization: "finalize reason {max_intents}"},
			rundomain.TaskExplore:   {Primary: "explore {intent_id}", Fallback: "conclude {intent_id}", Finalization: "finalize {intent_id}"},
			rundomain.TaskBootstrap: {Primary: "bootstrap {origin}", Fallback: "conclude {goal}", Finalization: "finalize {goal}"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := rundomain.BlackboardInput{
		Schema: rundomain.BlackboardInputSchema, Generation: 1, Revision: 1,
		Project: rundomain.InputProject{Title: "first", Origin: "origin", Goal: "goal"},
		Facts: []rundomain.InputFact{
			{ID: "origin", Description: "origin", Status: "active"},
			{ID: "goal", Description: "goal", Status: "active"},
		},
	}
	second := first
	second.Revision = 2
	second.Project.Title = "second"
	bind := func(input *rundomain.BlackboardInput, maxIntents int) string {
		binding := &recordingExecutionBinding{}
		_, err := catalog.BindExecution(binding, ExecutionBindingRequest{
			Task: rundomain.TaskReason, WorkerName: "mock", BlackboardInput: input, MaxIntents: maxIntents,
		}, rundomain.StartedRun{})
		if err != nil {
			t.Fatal(err)
		}
		return binding.reason.PromptTemplateHash
	}
	firstMaterial := bind(&first, 1)
	secondMaterial := bind(&second, 8)
	if firstMaterial != secondMaterial {
		t.Fatalf("Project input changed Prompt Template identity: first=%s second=%s",
			firstMaterial, secondMaterial)
	}
}

type recordingExecutionBinding struct {
	reason    *rundomain.ReasonExecution
	explore   *rundomain.ExploreExecution
	bootstrap *rundomain.BootstrapExecution
}

func (binding *recordingExecutionBinding) BindReason(
	_ rundomain.StartedRun,
	execution rundomain.ReasonExecution,
) (rundomain.Executor, error) {
	binding.reason = &execution
	return recordingExecutor{}, nil
}

func (binding *recordingExecutionBinding) BindExplore(
	_ rundomain.StartedRun,
	execution rundomain.ExploreExecution,
) (rundomain.Executor, error) {
	binding.explore = &execution
	return recordingExecutor{}, nil
}

func (binding *recordingExecutionBinding) BindBootstrap(
	_ rundomain.StartedRun,
	execution rundomain.BootstrapExecution,
) (rundomain.Executor, error) {
	binding.bootstrap = &execution
	return recordingExecutor{}, nil
}

type recordingExecutor struct{}

func (recordingExecutor) Execute(context.Context, rundomain.StartedRun) rundomain.Completion {
	return rundomain.Completion{}
}

func TestReasonPromptExcludesGoalFromSourceFactIDs(t *testing.T) {
	blackboard := rundomain.BlackboardInput{
		Facts: []rundomain.InputFact{
			{ID: "origin", Description: "origin", Status: "active"},
			{ID: "goal", Description: "goal", Status: "active"},
			{ID: "f001", Description: "evidence", Status: "active"},
		},
	}
	reason := renderReasonPrompt("facts={fact_ids}", blackboard, 2)
	if reason != `facts=["origin","f001"]` {
		t.Fatalf("Reason prompt source Fact IDs = %q, want goal excluded", reason)
	}
}

func TestDefaultReasonExecutionCarriesActiveReopenGuidanceThroughPrimaryAndFinalization(t *testing.T) {
	catalog, err := NewCatalogFromConfig(CatalogConfiguration{
		ImageReference: "test-image", PromptGroup: "default",
		Workers: []WorkerConfig{{Name: "mock", Type: WorkerTypeMock, MaxRunning: 1, TaskTypes: []TaskType{TaskTypeReason}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	blackboard := rundomain.BlackboardInput{
		Generation: 2, Revision: 3,
		Project: rundomain.InputProject{Title: "Reopened port scan", Origin: "127.0.0.1", Goal: "Scan port 8000"},
		Facts: []rundomain.InputFact{
			{ID: "origin", Description: "127.0.0.1", Status: "active"},
			{ID: "goal", Description: "Scan port 8000", Status: "active"},
			{ID: "f001", Description: "Port 8000 is open", Status: "active"},
		},
		Hints: []rundomain.InputHint{
			{ID: "h001", Content: "Scan port 9999", Creator: "operator", Status: "active"},
			{ID: "h002", Content: "Withdrawn direction", Creator: "operator", Status: "withdrawn"},
		},
	}
	binding := &recordingExecutionBinding{}
	_, err = catalog.BindExecution(binding, ExecutionBindingRequest{
		Task: rundomain.TaskReason, WorkerName: "mock", BlackboardInput: &blackboard, MaxIntents: 3,
	}, rundomain.StartedRun{})
	if err != nil {
		t.Fatal(err)
	}
	for phase, prompt := range map[string]string{
		"primary": binding.reason.Prompt, "finalization": binding.reason.FinalizationPrompt,
	} {
		if !strings.Contains(prompt, `"content":"Scan port 9999"`) || !strings.Contains(prompt, `"id":"h001"`) {
			t.Errorf("%s prompt omits current reopen guidance when old Facts already satisfy the original Goal", phase)
		}
		if strings.Contains(prompt, "Withdrawn direction") {
			t.Errorf("%s prompt includes withdrawn guidance as current work", phase)
		}
	}
	// History remains available in the immutable input; prompt guidance must
	// distinguish it from the current work rather than deleting old records.
	if !strings.Contains(string(binding.reason.RenderedInput.Bytes), "Port 8000 is open") ||
		!strings.Contains(string(binding.reason.RenderedInput.Bytes), "Withdrawn direction") {
		t.Fatal("Reason lost the historical Blackboard context")
	}
}

func TestPromptRenderersPreserveCairnContextMetadata(t *testing.T) {
	blackboard := rundomain.BlackboardInput{
		Facts: []rundomain.InputFact{
			{ID: "f001", Description: "active", Status: "active"},
			{ID: "f002", Description: "old", Status: "retracted"},
		},
		Intents: []rundomain.InputIntent{
			{ID: "i001", From: []string{"f001"}, Description: "open direction", Creator: "reasoner", Status: "open"},
			{ID: "i002", From: []string{"f001"}, To: "f002", Description: "done", Creator: "reasoner", Status: "concluded"},
		},
	}
	reason := renderReasonPrompt("facts={fact_ids}\nintents={open_intents}\nmax={max_intents}", blackboard, 3)
	for _, expected := range []string{`facts=["f001"]`, `"id":"i001"`, `"from":["f001"]`, `"creator":"reasoner"`, `max=3`} {
		if !strings.Contains(reason, expected) {
			t.Fatalf("Reason prompt %q does not contain %q", reason, expected)
		}
	}
	if strings.Contains(reason, `f002`) || strings.Contains(reason, `i002`) {
		t.Fatalf("Reason indexes included inactive records: %s", reason)
	}

	target := ExecutionTarget{ID: "i001", Description: "inspect the service"}
	explore := renderExplorePrompt("{intent_id}: {intent_description}", target)
	if explore != "i001: inspect the service" {
		t.Fatalf("Explore prompt = %q", explore)
	}

	bootstrap := renderBootstrapPrompt("{hints}", rundomain.BootstrapInput{Hints: []rundomain.InputHint{{
		ID: "h001", Content: "prioritize HTTP", Creator: "operator",
		CreatedAt: "2026-07-24T00:00:00Z", Status: "active",
	}}})
	for _, expected := range []string{`"id":"h001"`, `"content":"prioritize HTTP"`, `"creator":"operator"`, `"created_at":"2026-07-24T00:00:00Z"`, `"status":"active"`} {
		if !strings.Contains(bootstrap, expected) {
			t.Fatalf("Bootstrap prompt %q does not contain %q", bootstrap, expected)
		}
	}
	if missing := renderBootstrapPrompt("{hints}", rundomain.BootstrapInput{}); missing != "[]" {
		t.Fatalf("missing Bootstrap hints = %q, want []", missing)
	}
	withdrawn := renderBootstrapPrompt("{hints}", rundomain.BootstrapInput{Hints: []rundomain.InputHint{
		{ID: "h001", Content: "active guidance", Status: "active"},
		{ID: "h002", Content: "withdrawn guidance", Status: "withdrawn"},
	}})
	if !strings.Contains(withdrawn, "active guidance") || strings.Contains(withdrawn, "withdrawn guidance") {
		t.Fatalf("Bootstrap compact hints=%q", withdrawn)
	}
}
