package run_test

import (
	"testing"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func newWorkflowExecutionCatalog(
	t *testing.T,
	workerName string,
	task rundomain.TaskType,
	environment map[string]string,
	primary string,
	fallback string,
) *worker.Catalog {
	t.Helper()
	prompts := map[rundomain.TaskType]worker.PromptSet{
		rundomain.TaskReason:    {Primary: "reason", Finalization: "finalize reason"},
		rundomain.TaskExplore:   {Primary: "explore", Fallback: "conclude explore", Finalization: "finalize explore"},
		rundomain.TaskBootstrap: {Primary: "bootstrap", Fallback: "conclude bootstrap", Finalization: "finalize bootstrap"},
	}
	prompts[task] = worker.PromptSet{Primary: primary, Fallback: fallback, Finalization: "finalize " + primary}
	catalog, err := worker.NewCatalog(worker.CatalogSource{
		ImageReference: "aether-agent:latest",
		Workers: []worker.CatalogWorker{{
			Name: workerName, Adapter: "mock", TaskTypes: []rundomain.TaskType{task},
			MaxRunning: 1, Capabilities: []string{string(task)}, Environment: environment,
		}},
		Prompts: prompts,
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func bindWorkflowExecution(
	t *testing.T,
	runtime rundomain.Runtime,
	started rundomain.StartedRun,
	catalog *worker.Catalog,
	request worker.ExecutionBindingRequest,
) rundomain.Executor {
	t.Helper()
	binding, err := rundomain.NewExecutionBinding(runtime, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := catalog.BindExecution(binding, request, started)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}
