package main

import (
	"context"
	"errors"
	"fmt"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

// RunExecutionFactory assembles immutable Worker material with the Runtime
// and Secret Provider dependencies, then returns Scheduler's execution seam.
type RunExecutionFactory struct {
	catalog *worker.Catalog
	binding rundomain.ExecutionBinding
}

func NewRunExecutionFactory(
	catalog *worker.Catalog,
	runtime rundomain.Runtime,
	secrets rundomain.SecretEnvironmentProvider,
	controls ...rundomain.ExecutionControlStore,
) (*RunExecutionFactory, error) {
	if catalog == nil || runtime == nil {
		return nil, fmt.Errorf("Run execution dependencies are required")
	}
	var control rundomain.ExecutionControlStore
	if len(controls) > 0 {
		control = controls[0]
	}
	binding, err := rundomain.NewExecutionBinding(runtime, secrets, control, control, control)
	if err != nil {
		return nil, fmt.Errorf("Run execution binding: %w", err)
	}
	return &RunExecutionFactory{catalog: catalog, binding: binding}, nil
}

func (factory *RunExecutionFactory) PrepareAdmission(
	preparation scheduler.AdmissionPreparation,
) (scheduler.PreparedAdmission, error) {
	catalog := preparation.Catalog
	if catalog == nil {
		catalog = factory.catalog
	}
	if preparation.ConfigRevision != "" && catalog.Revision() != preparation.ConfigRevision {
		return scheduler.PreparedAdmission{}, fmt.Errorf("Worker Catalog does not match the Project Config Revision")
	}
	promptTemplateHash, err := catalog.PromptTemplateHash(preparation.Task)
	if err != nil {
		return scheduler.PreparedAdmission{}, err
	}
	workerSnapshot, err := catalog.WorkerSnapshotForTask(preparation.WorkerName, preparation.Task)
	if err != nil {
		return scheduler.PreparedAdmission{}, err
	}
	if _, err := catalog.AgentAdapter(preparation.WorkerName); err != nil {
		return scheduler.PreparedAdmission{}, err
	}
	request := rundomain.StartRequest{
		ProjectID:              preparation.ProjectID,
		Task:                   preparation.Task,
		CandidateGeneration:    preparation.CandidateGeneration,
		CandidateRevision:      preparation.CandidateRevision,
		TargetIntentID:         preparation.TargetIntentID,
		WorkKey:                preparation.WorkKey,
		DispatcherID:           preparation.DispatcherLease.InstanceID,
		DispatcherFencingToken: preparation.DispatcherLease.FencingToken,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: catalog.Revision(), PromptTemplateHash: promptTemplateHash,
			Worker: workerSnapshot, Image: preparation.Image,
			Timeouts: preparation.Timeouts, ResultSchema: rundomain.ResultSchemaV1,
		},
		RetryPolicy: preparation.RetryPolicy,
	}
	if preparation.Task == rundomain.TaskReason {
		request.Spec.MaxProposedIntents = preparation.MaxIntents
	}
	request, err = rundomain.ValidateStartRequest(request)
	if err != nil {
		return scheduler.PreparedAdmission{}, err
	}
	return scheduler.PreparedAdmission{
		Request: request,
		Binding: &runExecutionBinding{factory: factory, catalog: catalog, request: request},
	}, nil
}

type runExecutionBinding struct {
	factory *RunExecutionFactory
	catalog *worker.Catalog
	request rundomain.StartRequest
}

func (binding *runExecutionBinding) BindExecution(
	_ context.Context,
	admitted rundomain.AdmittedRun,
) (rundomain.Executor, error) {
	if binding == nil || binding.factory == nil {
		return nil, &rundomain.DomainError{
			Code: rundomain.ErrorExecutionContractMismatch, Detail: "Execution Binding dependencies are unavailable",
		}
	}
	if err := admitted.Validate(binding.request); err != nil {
		return nil, err
	}
	executor, err := binding.catalog.BindExecution(binding.factory.binding, worker.ExecutionBindingRequest{
		Task: admitted.Run.Task, WorkerName: binding.request.Spec.Worker.Name,
		BlackboardInput: admitted.BlackboardInput, BootstrapInput: admitted.BootstrapInput,
		MaxIntents: binding.request.Spec.MaxProposedIntents,
	}, admitted.StartedRun)
	if err != nil {
		var domainErr *rundomain.DomainError
		if errors.As(err, &domainErr) && domainErr.Code == rundomain.ErrorExecutionContractMismatch {
			return nil, err
		}
		detail := "Execution Binding deterministic material could not be reproduced"
		var materialErr *worker.ExecutionMaterialError
		if errors.As(err, &materialErr) && materialErr.Evidence != "" {
			detail = "Execution Binding " + string(materialErr.Evidence) + " could not be reproduced"
		}
		return nil, &rundomain.DomainError{
			Code: rundomain.ErrorExecutionContractMismatch, Detail: detail,
		}
	}
	return executor, nil
}

var _ scheduler.RunExecutionFactory = (*RunExecutionFactory)(nil)
