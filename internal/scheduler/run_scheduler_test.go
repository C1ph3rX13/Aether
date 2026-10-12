package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/testutil"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func TestRunSchedulerExecutesPersistedSupervisedRunAndPreservesRetryWorkerAffinity(t *testing.T) {
	lease := rundomain.DispatcherLease{
		InstanceID: "dispatcher-test", FencingToken: 7, ExpiresAt: time.Now().Add(time.Minute),
	}
	input := rundomain.BlackboardInput{
		Schema: rundomain.BlackboardInputSchema, Generation: 1, Revision: 2,
		Project: rundomain.InputProject{Title: "Project", Origin: "origin", Goal: "goal"},
		Facts: []rundomain.InputFact{
			{ID: "origin", Description: "origin", Status: "active"},
			{ID: "goal", Description: "goal", Status: "active"},
		},
	}
	control := &runSchedulerTestControl{
		lease: lease, projectID: "project-1", input: input, previousWorker: "mock",
	}
	runtime := testutil.NewMemoryRuntime()
	runtime.ConfigureDefaultExecution(testutil.ExecutionBehavior{Output: rundomain.ProcessOutput{
		Exited: true, ExitCode: 0, Stdout: `{"outcome":{"kind":"no_change"}}`,
	}})
	if err := runtime.FailCompleteRunAttempts("run-1", 1, errors.New("temporary Docker failure")); err != nil {
		t.Fatal(err)
	}
	catalog, err := worker.NewCatalog(worker.CatalogSource{
		ImageReference: "registry.example/aether@sha256:abc",
		Workers: []worker.CatalogWorker{
			{
				Name: "mock", Adapter: "mock", TaskTypes: []rundomain.TaskType{rundomain.TaskReason},
				Priority: 10, MaxRunning: 1, Environment: map[string]string{}, Capabilities: []string{"reason"},
			},
			{
				Name: "alternate", Adapter: "mock", TaskTypes: []rundomain.TaskType{rundomain.TaskReason},
				Priority: 0, MaxRunning: 1, Environment: map[string]string{}, Capabilities: []string{"reason"},
			},
		},
		Prompts: map[rundomain.TaskType]worker.PromptSet{
			rundomain.TaskReason: {
				Primary:      "Reason from {graph_yaml}; facts={fact_ids}; open={open_intents}; max={max_intents}",
				Finalization: "Finalize facts={fact_ids}; open={open_intents}; max={max_intents}",
			},
			rundomain.TaskExplore: {
				Primary: "Explore {intent_id}: {intent_description}", Fallback: "Conclude {intent_id}: {intent_description}",
				Finalization: "Finalize {intent_id}: {intent_description}",
			},
			rundomain.TaskBootstrap: {
				Primary: "Bootstrap {origin} to {goal}; {hints}", Fallback: "Conclude {origin} to {goal}; {hints}",
				Finalization: "Finalize {origin} to {goal}; {hints}",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := RunSchedulerPolicy{
		PollInterval: time.Second, MaxWorkers: 2, MaxProjectWorkers: 1, MaxRunningProjects: 1,
		WorkerHealthcheck: true, ExecutionPolicies: schedulerExecutionPoliciesForTest(retryPolicyForTest(), 5, 1, 0, 1, 12, 10, 2),
	}
	schedulerConfig := RunSchedulerConfig{
		ConfigRevision: "scheduler-test-config",
		Policy:         policy, Catalog: catalog,
		PrepareRevisionWorkers: func(context.Context, string) (map[string]bool, error) {
			control.recordEvent("workers")
			return map[string]bool{"mock": true, "alternate": true}, nil
		},
		PrepareRevisionWorker: func(_ context.Context, _, workerName string) (bool, error) {
			control.recordEvent("task-worker:" + workerName)
			return true, nil
		},
		Resources:  runtime,
		Executions: &runSchedulerTestExecutionFactory{runtime: runtime, catalog: catalog},
		Lease:      lease, DeploymentID: "deployment-test", Once: true,
	}
	sut, err := NewRunScheduler(RunSchedulerControls{
		Planning: control, Recovery: control, Lifecycle: control,
	}, schedulerConfig)
	if err != nil {
		t.Fatal(err)
	}
	overCapacity := schedulerConfig
	overCapacity.Policy.MaxWorkers = rundomain.MaximumObservedRunIDs + 1
	if _, err := NewRunScheduler(RunSchedulerControls{
		Planning: control, Recovery: control, Lifecycle: control,
	}, overCapacity); err == nil {
		t.Fatal("scheduler accepted more locally supervised Runs than one bounded revocation request can observe")
	}
	missingTaskHealthcheck := schedulerConfig
	missingTaskHealthcheck.PrepareRevisionWorker = nil
	if _, err := NewRunScheduler(RunSchedulerControls{
		Planning: control, Recovery: control, Lifecycle: control,
	}, missingTaskHealthcheck); err == nil {
		t.Fatal("task healthcheck mode accepted no Worker readiness check")
	}
	if err := sut.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	control.mu.Lock()
	starts := append([]rundomain.StartRequest(nil), control.starts...)
	finishes := append([]rundomain.FinishRequest(nil), control.finishes...)
	events := append([]string(nil), control.events...)
	control.mu.Unlock()
	if len(events) < 5 || events[0] != "recover" || events[1] != "snapshot" ||
		events[2] != "workers" || events[3] != "task-worker:mock" || events[4] != "start" {
		t.Fatalf("startup ordering: %+v", events)
	}
	if len(starts) != 1 || starts[0].Task != rundomain.TaskReason ||
		starts[0].DispatcherFencingToken != lease.FencingToken || starts[0].Spec.Worker.Name != "mock" {
		t.Fatalf("persisted starts: %+v", starts)
	}
	if len(finishes) != 1 || len(finishes[0].Candidate) == 0 || finishes[0].Termination != nil {
		t.Fatalf("finished Runs: %+v", finishes)
	}
	snapshot := runtime.Snapshot()
	if len(snapshot.Launches) != 1 || len(snapshot.CompletedRunIDs) == 0 || snapshot.CompletedRunIDs[len(snapshot.CompletedRunIDs)-1] != "run-1" {
		t.Fatalf("runtime lifecycle: %+v", snapshot)
	}
	if snapshot.CompleteRunAttempts["run-1"] != 2 {
		t.Fatalf("CompleteRun calls = %d, want 2", snapshot.CompleteRunAttempts["run-1"])
	}
}

func TestRunSchedulerFailsClosedAfterResolvingExecutionContractMismatch(t *testing.T) {
	lease := rundomain.DispatcherLease{
		InstanceID: "dispatcher-mismatch", FencingToken: 9, ExpiresAt: time.Now().Add(time.Minute),
	}
	input := rundomain.BlackboardInput{
		Schema: rundomain.BlackboardInputSchema, Generation: 1, Revision: 1,
		Project: rundomain.InputProject{Title: "Project", Origin: "origin", Goal: "goal"},
		Facts: []rundomain.InputFact{
			{ID: "origin", Description: "origin", Status: "active"},
			{ID: "goal", Description: "goal", Status: "active"},
		},
	}
	control := &runSchedulerTestControl{lease: lease, projectID: "project-mismatch", input: input}
	runtime := testutil.NewMemoryRuntime()
	catalog, err := worker.NewCatalog(worker.CatalogSource{
		ImageReference: "registry.example/aether@sha256:mismatch",
		Workers: []worker.CatalogWorker{{
			Name: "mock", Adapter: "mock", TaskTypes: []rundomain.TaskType{rundomain.TaskReason}, MaxRunning: 1,
		}},
		Prompts: map[rundomain.TaskType]worker.PromptSet{
			rundomain.TaskReason:    {Primary: "reason", Finalization: "finalize reason"},
			rundomain.TaskExplore:   {Primary: "explore", Fallback: "conclude", Finalization: "finalize explore"},
			rundomain.TaskBootstrap: {Primary: "bootstrap", Fallback: "conclude", Finalization: "finalize bootstrap"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := RunSchedulerPolicy{
		PollInterval: time.Second, MaxWorkers: 2, MaxProjectWorkers: 1, MaxRunningProjects: 1,
		ExecutionPolicies: schedulerExecutionPoliciesForTest(retryPolicyForTest(), 1, 1, 0, 1, 4, 6, 2),
	}
	fatalReported := make(chan error, 1)
	sut, err := NewRunScheduler(RunSchedulerControls{
		Planning: control, Recovery: control, Lifecycle: control,
	}, RunSchedulerConfig{
		ConfigRevision: "scheduler-test-config",
		Policy:         policy, Catalog: catalog,
		Resources: runtime, Executions: &runSchedulerTestExecutionFactory{
			runtime: runtime, catalog: catalog, bindingError: errors.New("private binding material"),
		},
		Lease: lease, DeploymentID: "deployment-mismatch",
		FailClosed: func(err error) {
			fatalReported <- err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- sut.Run(runContext) }()
	var reported error
	select {
	case reported = <-fatalReported:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not report fatal Execution Contract Mismatch")
	}
	var domainErr *rundomain.DomainError
	if !errors.As(reported, &domainErr) || domainErr.Code != rundomain.ErrorExecutionContractMismatch {
		t.Fatalf("scheduler reported fatal error: %v", reported)
	}
	select {
	case err := <-done:
		t.Fatalf("scheduler returned before fatal Lease expiry: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancelRun()
	err = <-done
	domainErr = nil
	if !errors.As(err, &domainErr) || domainErr.Code != rundomain.ErrorExecutionContractMismatch {
		t.Fatalf("scheduler fatal return: %v", err)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if len(control.starts) != 1 || len(control.finishes) != 1 || control.finishes[0].Termination == nil ||
		control.finishes[0].Termination.Code != rundomain.ErrorExecutionContractMismatch {
		t.Fatalf("fail-closed lifecycle: starts=%d finishes=%+v", len(control.starts), control.finishes)
	}
}

func TestRunSchedulerKeepsActiveRunsAliveUntilFailClosedLeaseExpires(t *testing.T) {
	input := rundomain.BlackboardInput{
		Schema: rundomain.BlackboardInputSchema, Generation: 1, Revision: 1,
		Project: rundomain.InputProject{Title: "Project", Origin: "origin", Goal: "goal"},
		Facts: []rundomain.InputFact{
			{ID: "origin", Description: "origin", Status: "active"},
			{ID: "goal", Description: "goal", Status: "active"},
		},
	}
	control := &runSchedulerTestControl{
		projectIDs: []string{"project-active", "project-mismatch"}, input: input, uniqueRunIDs: true,
	}
	catalog, err := worker.NewCatalog(worker.CatalogSource{
		ImageReference: "registry.example/aether@sha256:quiescing",
		Workers: []worker.CatalogWorker{{
			Name: "mock", Adapter: "mock", TaskTypes: []rundomain.TaskType{rundomain.TaskReason}, MaxRunning: 2,
		}},
		Prompts: map[rundomain.TaskType]worker.PromptSet{
			rundomain.TaskReason:    {Primary: "reason", Finalization: "finalize reason"},
			rundomain.TaskExplore:   {Primary: "explore", Fallback: "conclude", Finalization: "finalize explore"},
			rundomain.TaskBootstrap: {Primary: "bootstrap", Fallback: "conclude", Finalization: "finalize bootstrap"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := RunSchedulerPolicy{
		PollInterval: time.Second, MaxWorkers: 2, MaxProjectWorkers: 1, MaxRunningProjects: 2,
		ExecutionPolicies: schedulerExecutionPoliciesForTest(retryPolicyForTest(), 1, 1, 0, 1, 4, 6, 2),
	}
	active := &blockingSchedulerExecutor{startCh: make(chan struct{}), stopCh: make(chan struct{})}
	fatalReported := make(chan error, 1)
	leaseControl := &dispatcherLeaseControlFake{expiresAt: time.Now().Add(250 * time.Millisecond)}
	leaseRunner, err := NewDispatcherLeaseRunner(leaseControl, DispatcherLeaseRunnerConfig{
		InstanceID: "dispatcher-quiescing", LeaseTTL: 5 * time.Second,
		RenewInterval: time.Second, CallTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	startedAt := time.Now()
	go func() {
		done <- leaseRunner.RunFailClosed(t.Context(), func(
			dispatchContext context.Context,
			lease rundomain.DispatcherLease,
			failClosed func(error),
		) error {
			runtime := testutil.NewMemoryRuntime()
			sut, schedulerErr := NewRunScheduler(RunSchedulerControls{
				Planning: control, Recovery: control, Lifecycle: control,
			}, RunSchedulerConfig{
				ConfigRevision: "scheduler-test-config",
				Policy:         policy, Catalog: catalog,
				Resources: runtime, Executions: &runSchedulerTestExecutionFactory{
					runtime: runtime, catalog: catalog,
					bindings: map[string]ExecutionBinding{
						"project-active": runSchedulerFixedExecutionBinding{executor: active},
					},
					bindingError:        errors.New("private binding material"),
					bindingErrorProject: "project-mismatch",
				},
				Lease: lease, DeploymentID: "deployment-quiescing",
				FailClosed: func(err error) {
					fatalReported <- err
					failClosed(err)
				},
			})
			if schedulerErr != nil {
				return schedulerErr
			}
			return sut.Run(dispatchContext)
		})
	}()
	select {
	case <-active.startCh:
	case <-time.After(time.Second):
		t.Fatal("first Run did not become Active")
	}
	select {
	case <-fatalReported:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not report the later Execution Contract Mismatch")
	}
	select {
	case <-active.stopCh:
		t.Fatal("fail-closed cancelled an existing Active Run before Dispatcher Lease expiry")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case err := <-done:
		t.Fatalf("scheduler returned before Dispatcher Lease expiry: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-active.stopCh:
		if elapsed := time.Since(startedAt); elapsed < 150*time.Millisecond {
			t.Fatalf("existing Active Run stopped before Dispatcher Lease expiry: %s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("existing Active Run did not converge after Dispatcher Lease expiry")
	}
	err = <-done
	if !isExecutionContractMismatch(err) {
		t.Fatalf("scheduler fatal return: %v", err)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if len(control.starts) != 2 || len(control.finishes) != 2 {
		t.Fatalf("quiescing lifecycle: starts=%d finishes=%+v", len(control.starts), control.finishes)
	}
	terminations := make(map[rundomain.RunID]*rundomain.RunTermination, len(control.finishes))
	for _, finish := range control.finishes {
		terminations[finish.RunID] = finish.Termination
	}
	if termination := terminations["run-project-mismatch"]; termination == nil ||
		termination.Code != rundomain.ErrorExecutionContractMismatch {
		t.Fatalf("mismatched Run terminal evidence: %+v", termination)
	}
	if termination := terminations["run-project-active"]; termination == nil ||
		termination.Status != rundomain.StatusInterrupted {
		t.Fatalf("Active Run convergence evidence: %+v", termination)
	}
	leaseControl.mu.Lock()
	defer leaseControl.mu.Unlock()
	if leaseControl.renewed != 0 || leaseControl.released != 0 {
		t.Fatalf("fail-closed Dispatcher Lease renewed=%d released=%d", leaseControl.renewed, leaseControl.released)
	}
}

func TestRunSchedulerCancelsRevokedRunBeforeInventoryReconciliation(t *testing.T) {
	control := &revocationTimingControl{requests: make(chan SchedulingSnapshotRequest, 1)}
	baseRuntime := testutil.NewMemoryRuntime()
	resources := &blockingInventoryRuntime{
		MemoryRuntime: baseRuntime, entered: make(chan struct{}), release: make(chan struct{}),
	}
	catalog, err := worker.NewCatalog(worker.CatalogSource{
		ImageReference: "registry.example/aether@sha256:revocation",
		Workers: []worker.CatalogWorker{{
			Name: "mock", Adapter: "mock", TaskTypes: []rundomain.TaskType{rundomain.TaskReason}, MaxRunning: 1,
		}},
		Prompts: map[rundomain.TaskType]worker.PromptSet{
			rundomain.TaskReason:    {Primary: "reason", Finalization: "finalize reason"},
			rundomain.TaskExplore:   {Primary: "explore", Fallback: "conclude", Finalization: "finalize explore"},
			rundomain.TaskBootstrap: {Primary: "bootstrap", Fallback: "conclude", Finalization: "finalize bootstrap"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sut := &RunScheduler{
		planning: control, inventoryControl: control,
		config: RunSchedulerConfig{
			ConfigRevision: "revocation-config",
			Policy:         RunSchedulerPolicy{MaxWorkers: 1, MaxProjectWorkers: 1, MaxRunningProjects: 1},
			Catalog:        catalog, Resources: resources,
			Lease: rundomain.DispatcherLease{InstanceID: "revocation-dispatcher", FencingToken: 7},
		},
	}
	coordinator := NewCoordinator(Planner{}, 8)
	coordinatorContext, stopCoordinator := context.WithCancel(t.Context())
	coordinatorDone := make(chan error, 1)
	go func() { coordinatorDone <- coordinator.Run(coordinatorContext) }()
	runContext, cancelRun := context.WithCancelCause(t.Context())
	submitCoordinatorEvent(t, coordinator, RunStartedEvent{
		Run: RunningRunSnapshot{RunID: "revoked-run", ProjectID: "project"}, Cancel: cancelRun,
	})
	tickDone := make(chan error, 1)
	var runs sync.WaitGroup
	go func() { tickDone <- sut.tick(t.Context(), coordinatorContext, coordinator, nil, &runs) }()
	select {
	case request := <-control.requests:
		if len(request.ObservedRunIDs) != 1 || request.ObservedRunIDs[0] != "revoked-run" {
			t.Fatalf("Scheduling Snapshot observed Runs: %v", request.ObservedRunIDs)
		}
	case <-time.After(time.Second):
		t.Fatal("Scheduler did not request its Scheduling Snapshot")
	}
	select {
	case <-resources.entered:
	case <-time.After(time.Second):
		t.Fatal("Scheduler did not enter inventory reconciliation")
	}
	select {
	case <-runContext.Done():
		if !errors.Is(context.Cause(runContext), errRunRevokedCancellation) {
			t.Fatalf("Run cancellation cause: %v", context.Cause(runContext))
		}
	case <-time.After(time.Second):
		t.Fatal("Run revocation was delayed behind inventory reconciliation")
	}
	close(resources.release)
	if err := <-tickDone; err != nil {
		t.Fatal(err)
	}
	stopCoordinator()
	if err := <-coordinatorDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func retryPolicyForTest() rundomain.RetryPolicy {
	return rundomain.RetryPolicy{
		MaxAttempts: 3, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 100 * time.Millisecond,
		Multiplier: 2, DeclineCooldown: 5 * time.Second,
	}
}

func schedulerExecutionPoliciesForTest(
	retry rundomain.RetryPolicy,
	phaseSeconds, fallbackSeconds, recoverySeconds, graceSeconds, reasonRunSeconds, phasedRunSeconds, maxIntents int,
) map[string]map[rundomain.TaskType]rundomain.TaskExecutionPolicy {
	return map[string]map[rundomain.TaskType]rundomain.TaskExecutionPolicy{
		"mock": {
			rundomain.TaskReason: {
				Timeouts:    rundomain.TimeoutSnapshot{RunSeconds: reasonRunSeconds, PhaseSeconds: phaseSeconds, GraceSeconds: graceSeconds},
				RetryPolicy: retry, MaxProposedIntents: maxIntents,
			},
			rundomain.TaskExplore: {
				Timeouts:    rundomain.TimeoutSnapshot{RunSeconds: phasedRunSeconds, PhaseSeconds: phaseSeconds, FallbackPhaseSeconds: fallbackSeconds, RecoveryPhaseSeconds: recoverySeconds, GraceSeconds: graceSeconds},
				RetryPolicy: retry,
			},
			rundomain.TaskBootstrap: {
				Timeouts:    rundomain.TimeoutSnapshot{RunSeconds: phasedRunSeconds, PhaseSeconds: phaseSeconds, FallbackPhaseSeconds: fallbackSeconds, RecoveryPhaseSeconds: recoverySeconds, GraceSeconds: graceSeconds},
				RetryPolicy: retry,
			},
		},
	}
}

type runSchedulerTestControl struct {
	mu             sync.Mutex
	lease          rundomain.DispatcherLease
	projectID      string
	projectIDs     []string
	input          rundomain.BlackboardInput
	uniqueRunIDs   bool
	previousWorker string
	starts         []rundomain.StartRequest
	finishes       []rundomain.FinishRequest
	finishErrors   []error
	events         []string
}

type revocationTimingControl struct {
	requests chan SchedulingSnapshotRequest
}

func (control *revocationTimingControl) SchedulingSnapshot(_ context.Context, request SchedulingSnapshotRequest) (ControlSchedulingSnapshot, error) {
	control.requests <- request
	return ControlSchedulingSnapshot{
		ObservedAt: time.Now().UTC(), DispatcherPolicy: ControlDispatcherPolicy{
			ConfigRevision: "revocation-config", PollIntervalSeconds: 1, MaxWorkers: 1, MaxRunningProjects: 1,
		},
		RevokedRunIDs: []rundomain.RunID{"revoked-run"}, Work: []ControlSchedulingWork{},
	}, nil
}

func (*revocationTimingControl) ProjectRuntimeStatuses(context.Context, rundomain.ProjectRuntimeStatusRequest) ([]rundomain.ProjectRuntimeStatusResult, error) {
	return nil, errors.New("unexpected inventory lookup")
}

type blockingInventoryRuntime struct {
	*testutil.MemoryRuntime
	entered chan struct{}
	release chan struct{}
}

func (runtime *blockingInventoryRuntime) ReconcileProjectInventory(ctx context.Context, _ rundomain.ProjectRuntimeStatusLookup) error {
	close(runtime.entered)
	select {
	case <-runtime.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type runSchedulerTestExecutionFactory struct {
	runtime             rundomain.Runtime
	catalog             *worker.Catalog
	bindings            map[string]ExecutionBinding
	bindingError        error
	bindingErrorProject string
}

func (factory *runSchedulerTestExecutionFactory) PrepareAdmission(
	preparation AdmissionPreparation,
) (PreparedAdmission, error) {
	promptTemplateHash, err := factory.catalog.PromptTemplateHash(preparation.Task)
	if err != nil {
		return PreparedAdmission{}, err
	}
	workerSnapshot, err := factory.catalog.WorkerSnapshot(preparation.WorkerName)
	if err != nil {
		return PreparedAdmission{}, err
	}
	request := rundomain.StartRequest{
		ProjectID: preparation.ProjectID, Task: preparation.Task,
		CandidateGeneration: preparation.CandidateGeneration,
		CandidateRevision:   preparation.CandidateRevision,
		TargetIntentID:      preparation.TargetIntentID, WorkKey: preparation.WorkKey,
		DispatcherID:           preparation.DispatcherLease.InstanceID,
		DispatcherFencingToken: preparation.DispatcherLease.FencingToken,
		Spec: rundomain.AdmissionSpecSnapshot{
			ConfigRevision: factory.catalog.Revision(), PromptTemplateHash: promptTemplateHash,
			Worker: workerSnapshot, Image: preparation.Image,
			Timeouts: preparation.Timeouts, ResultSchema: rundomain.ResultSchemaV1,
			MaxProposedIntents: preparation.MaxIntents,
		},
		RetryPolicy: preparation.RetryPolicy,
	}
	prepared := PreparedAdmission{
		Request: request,
		Binding: runSchedulerTestExecutionBinding{
			factory: factory, workerName: preparation.WorkerName,
		},
	}
	if binding := factory.bindings[preparation.ProjectID]; binding != nil {
		prepared.Binding = binding
	}
	if factory.bindingError != nil &&
		(factory.bindingErrorProject == "" || factory.bindingErrorProject == preparation.ProjectID) {
		prepared.Binding = runSchedulerErrorExecutionBinding{err: factory.bindingError}
	}
	return prepared, nil
}

type runSchedulerFixedExecutionBinding struct{ executor rundomain.Executor }

func (binding runSchedulerFixedExecutionBinding) BindExecution(
	context.Context,
	rundomain.AdmittedRun,
) (rundomain.Executor, error) {
	return binding.executor, nil
}

type blockingSchedulerExecutor struct {
	started sync.Once
	stopped sync.Once
	startCh chan struct{}
	stopCh  chan struct{}
}

func (executor *blockingSchedulerExecutor) Execute(ctx context.Context, started rundomain.StartedRun) rundomain.Completion {
	executor.started.Do(func() { close(executor.startCh) })
	<-ctx.Done()
	executor.stopped.Do(func() { close(executor.stopCh) })
	return rundomain.Completion{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		Kind: rundomain.CompletionInterrupted, Code: rundomain.ErrorDispatcherRestarted,
	}
}

type runSchedulerErrorExecutionBinding struct{ err error }

func (binding runSchedulerErrorExecutionBinding) BindExecution(
	context.Context,
	rundomain.AdmittedRun,
) (rundomain.Executor, error) {
	return nil, binding.err
}

type runSchedulerTestExecutionBinding struct {
	factory    *runSchedulerTestExecutionFactory
	workerName string
}

func (binding runSchedulerTestExecutionBinding) BindExecution(
	_ context.Context,
	admitted rundomain.AdmittedRun,
) (rundomain.Executor, error) {
	if admitted.BlackboardInput == nil {
		return nil, errors.New("admitted Reason Run has no Blackboard input")
	}
	executionBinding, err := rundomain.NewExecutionBinding(binding.factory.runtime, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	return binding.factory.catalog.BindExecution(executionBinding, worker.ExecutionBindingRequest{
		Task: rundomain.TaskReason, WorkerName: binding.workerName,
		BlackboardInput: admitted.BlackboardInput,
		MaxIntents:      admitted.Run.Spec.MaxProposedIntents,
	}, admitted.StartedRun)
}

func (control *runSchedulerTestControl) recordEvent(event string) {
	control.mu.Lock()
	defer control.mu.Unlock()
	control.events = append(control.events, event)
}

func (control *runSchedulerTestControl) SchedulingSnapshot(
	_ context.Context,
	request SchedulingSnapshotRequest,
) (ControlSchedulingSnapshot, error) {
	control.recordEvent("snapshot")
	snapshot := rundomain.InputSnapshot{
		Generation: control.input.Generation,
		Revision:   control.input.Revision,
	}
	projectIDs := control.projectIDs
	if len(projectIDs) == 0 {
		projectIDs = []string{control.projectID}
	}
	work := make([]ControlSchedulingWork, 0, len(projectIDs))
	for _, projectID := range projectIDs {
		key, err := rundomain.WorkKeyFor(projectID, rundomain.TaskReason, snapshot)
		if err != nil {
			return ControlSchedulingSnapshot{}, err
		}
		work = append(work, ControlSchedulingWork{
			ProjectID: projectID, ConfigRevision: "scheduler-test-config",
			Generation: snapshot.Generation, Revision: snapshot.Revision,
			Task: rundomain.TaskReason, WorkKey: key,
			State: rundomain.SchedulingWorkState{
				WorkKey: key, RetryState: rundomain.RetryEligible, PreviousWorker: control.previousWorker,
			},
		})
	}
	return ControlSchedulingSnapshot{
		ObservedAt: time.Now().UTC(), DispatcherPolicy: ControlDispatcherPolicy{
			ConfigRevision: "scheduler-test-config", PollIntervalSeconds: 1,
			MaxWorkers: 2, MaxRunningProjects: 2,
		},
		NextProjectCursor: projectIDs[len(projectIDs)-1], Work: work,
	}, nil
}

func (control *runSchedulerTestControl) StartRun(_ context.Context, request rundomain.StartRequest) (*rundomain.AdmittedRun, error) {
	control.recordEvent("start")
	validated, err := rundomain.ValidateStartRequest(request)
	if err != nil {
		return nil, err
	}
	inputIdentity, err := control.input.InputIdentityV1()
	if err != nil {
		return nil, err
	}
	inputSnapshot := rundomain.InputSnapshot{
		Generation: control.input.Generation, Revision: control.input.Revision, Identity: inputIdentity,
	}
	scope, err := rundomain.ClaimScopeFor(request.ProjectID, request.Task, inputSnapshot)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	runID := rundomain.RunID("run-1")
	if control.uniqueRunIDs {
		runID = rundomain.RunID("run-" + request.ProjectID)
	}
	executionSpec := rundomain.ExecutionSpecSnapshot{
		Schema:         rundomain.ExecutionSpecSchemaV2,
		ConfigRevision: validated.Spec.ConfigRevision, PromptTemplateHash: validated.Spec.PromptTemplateHash,
		Input: inputSnapshot, Worker: validated.Spec.Worker, Image: validated.Spec.Image,
		Timeouts: validated.Spec.Timeouts, ResultSchema: validated.Spec.ResultSchema,
		MaxProposedIntents: validated.Spec.MaxProposedIntents,
	}
	started := rundomain.StartedRun{
		Run: rundomain.ExecutionRun{
			ID: runID, ProjectID: request.ProjectID, WorkKey: validated.WorkKey, ClaimScope: scope,
			Status: rundomain.StatusActive, FencingToken: 1,
			DispatcherID: request.DispatcherID, DispatcherFencingToken: request.DispatcherFencingToken,
			Task: request.Task, Spec: executionSpec, StartedAt: now,
		},
		Claim: rundomain.Claim{
			WorkKey: validated.WorkKey, Scope: scope, RunID: runID, ProjectID: request.ProjectID,
			Task: request.Task, FencingToken: 1, DispatcherID: request.DispatcherID,
			ClaimedAt: now, LeaseExpiresAt: now.Add(5 * time.Second),
		},
	}
	control.mu.Lock()
	control.starts = append(control.starts, request)
	control.mu.Unlock()
	return &rundomain.AdmittedRun{StartedRun: started, BlackboardInput: &control.input}, nil
}

func (control *runSchedulerTestControl) RenewRun(context.Context, rundomain.RenewRunRequest) (time.Time, error) {
	return time.Now().Add(5 * time.Second), nil
}

func (control *runSchedulerTestControl) FinishRun(_ context.Context, request rundomain.FinishRequest) (*rundomain.FinishReceipt, error) {
	control.mu.Lock()
	control.finishes = append(control.finishes, request)
	if len(control.finishErrors) > 0 {
		err := control.finishErrors[0]
		control.finishErrors = control.finishErrors[1:]
		control.mu.Unlock()
		return nil, err
	}
	control.mu.Unlock()
	receipt := &rundomain.FinishReceipt{RunID: request.RunID, Status: rundomain.StatusCommitted}
	if request.Termination != nil {
		receipt.Status = request.Termination.Status
		receipt.Code = request.Termination.Code
	}
	return receipt, nil
}

func (control *runSchedulerTestControl) RecoverRuns(context.Context, rundomain.RecoveryOptions) ([]rundomain.RunID, error) {
	control.recordEvent("recover")
	return nil, nil
}

func (*runSchedulerTestControl) RecordRecoveryEvent(context.Context, rundomain.RecoveryRecord) error {
	return nil
}

func (*runSchedulerTestControl) ListExecutionHandles(context.Context, rundomain.ExecutionHandleListRequest) ([]rundomain.ExecutionHandle, error) {
	return nil, nil
}

func (*runSchedulerTestControl) MarkExecutionHandleStopped(context.Context, rundomain.ExecutionHandleStopRequest) error {
	return nil
}

func (control *runSchedulerTestControl) MarshalJSON() ([]byte, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	return json.Marshal(struct{ Starts, Finishes int }{len(control.starts), len(control.finishes)})
}

var _ SchedulingControl = (*runSchedulerTestControl)(nil)
var _ RunRecoveryControl = (*runSchedulerTestControl)(nil)
var _ RunLifecycleControl = (*runSchedulerTestControl)(nil)
