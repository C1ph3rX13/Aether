package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

type RunSchedulerConfig struct {
	ConfigRevision         string
	Policy                 RunSchedulerPolicy
	PolicyForRevision      func(context.Context, string) (RunSchedulerPolicy, error)
	Catalog                *worker.Catalog
	CatalogForRevision     func(context.Context, string) (*worker.Catalog, error)
	Resources              RunResources
	Executions             RunExecutionFactory
	Lease                  rundomain.DispatcherLease
	DeploymentID           string
	PrepareRevisionWorkers func(context.Context, string) (map[string]bool, error)
	PrepareRevisionWorker  func(context.Context, string, string) (bool, error)
	FailClosed             func(error)
	Once                   bool
	EventBuffer            int
	FinishTimeout          time.Duration
}

// RunScheduler is the production scheduler. Coordinator is the sole owner of
// mutable scheduling state; each supervised Run owns only its local lifecycle.
type RunScheduler struct {
	planning         SchedulingControl
	recoveryControl  RunRecoveryControl
	lifecycleControl RunLifecycleControl
	cleanupControl   ProjectCleanupControl
	inventoryControl ProjectInventoryControl
	config           RunSchedulerConfig
	pollInterval     time.Duration
}

type RunSchedulerControls struct {
	Planning  SchedulingControl
	Inventory ProjectInventoryControl
	Recovery  RunRecoveryControl
	Lifecycle RunLifecycleControl
	Cleanup   ProjectCleanupControl
}

func NewRunScheduler(controls RunSchedulerControls, config RunSchedulerConfig) (*RunScheduler, error) {
	if controls.Planning == nil || controls.Recovery == nil || controls.Lifecycle == nil ||
		config.Catalog == nil || config.Resources == nil || config.Executions == nil ||
		strings.TrimSpace(config.ConfigRevision) == "" || len(config.ConfigRevision) > 256 ||
		config.Lease.InstanceID == "" || config.Lease.FencingToken <= 0 || config.DeploymentID == "" {
		return nil, fmt.Errorf("Run scheduler configuration is invalid")
	}
	if _, reconcilesInventory := config.Resources.(ProjectInventoryReconciler); reconcilesInventory && controls.Inventory == nil {
		return nil, fmt.Errorf("Run scheduler Project inventory control is required")
	}
	if err := config.Policy.Validate(); err != nil {
		return nil, err
	}
	if config.Policy.WorkerHealthcheck && config.PrepareRevisionWorker == nil {
		return nil, fmt.Errorf("Run scheduler task healthcheck requires a Worker readiness check")
	}
	if config.EventBuffer == 0 {
		config.EventBuffer = 256
	}
	if config.EventBuffer < 1 || config.EventBuffer > 65536 {
		return nil, fmt.Errorf("Run scheduler event buffer is invalid")
	}
	if config.FinishTimeout == 0 {
		config.FinishTimeout = 10 * time.Second
	}
	if config.FinishTimeout < time.Second || config.FinishTimeout > time.Minute {
		return nil, fmt.Errorf("Run scheduler finish timeout is invalid")
	}
	return &RunScheduler{
		planning: controls.Planning, recoveryControl: controls.Recovery,
		lifecycleControl: controls.Lifecycle, cleanupControl: controls.Cleanup,
		inventoryControl: controls.Inventory, config: config, pollInterval: config.Policy.PollInterval,
	}, nil
}

func (scheduler *RunScheduler) Run(ctx context.Context) error {
	coordinator := NewCoordinator(Planner{}, scheduler.config.EventBuffer)
	coordinatorContext, cancelCoordinator := context.WithCancel(context.Background())
	coordinatorDone := make(chan error, 1)
	go func() { coordinatorDone <- coordinator.Run(coordinatorContext) }()

	stopCoordinator := func() error {
		cancelCoordinator()
		err := <-coordinatorDone
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	lifecycle, err := NewRunLifecycle(
		scheduler.lifecycleControl, scheduler.config.Resources, coordinator,
		RunLifecycleConfig{FinishTimeout: scheduler.config.FinishTimeout},
	)
	if err != nil {
		return errors.Join(err, stopCoordinator())
	}
	recovery, err := NewRunRecovery(
		scheduler.recoveryControl, scheduler.config.Resources,
		scheduler.config.Lease, coordinator.Events(),
	)
	if err != nil {
		return errors.Join(err, stopCoordinator())
	}
	if _, err := recovery.Recover(ctx, "dispatcher startup"); err != nil {
		return errors.Join(err, stopCoordinator())
	}
	var runs sync.WaitGroup
	runTick := func() error { return scheduler.tick(ctx, coordinatorContext, coordinator, lifecycle, &runs) }
	quiesceAfterFatal := func(fatal error) error {
		if scheduler.config.FailClosed == nil {
			if scheduler.config.Once {
				runs.Wait()
			}
			return errors.Join(fatal, stopCoordinator())
		}
		scheduler.config.FailClosed(fatal)
		<-ctx.Done()
		runs.Wait()
		return errors.Join(fatal, stopCoordinator())
	}
	if err := runTick(); isExecutionContractMismatch(err) {
		return quiesceAfterFatal(err)
	} else if err != nil && scheduler.config.Once {
		runs.Wait()
		return errors.Join(err, stopCoordinator())
	} else if err != nil {
		slog.Error("Run scheduler tick failed", "error", err)
	}
	if scheduler.config.Once {
		runs.Wait()
		return stopCoordinator()
	}

	interval := scheduler.pollInterval
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			runs.Wait()
			return errors.Join(stopCoordinator(), context.Cause(ctx))
		case <-timer.C:
			if err := runTick(); isExecutionContractMismatch(err) {
				return quiesceAfterFatal(err)
			} else if err != nil {
				slog.Error("Run scheduler tick failed", "error", err)
			}
			interval = scheduler.pollInterval
			timer.Reset(interval)
		}
	}
}

type plannedRun struct {
	projectID      string
	configRevision string
	catalog        *worker.Catalog
	generation     int
	revision       int64
	task           rundomain.TaskType
	target         *SchedulingTarget
	workKey        rundomain.WorkKey
	policy         RunSchedulerPolicy
}

func (scheduler *RunScheduler) tick(
	ctx context.Context,
	coordinatorContext context.Context,
	coordinator *Coordinator,
	lifecycle *RunLifecycle,
	runs *sync.WaitGroup,
) error {
	if scheduler.cleanupControl != nil {
		if runtime, ok := scheduler.config.Resources.(ProjectCleanupRuntime); ok {
			jobs, err := scheduler.cleanupControl.LeaseProjectCleanupJobs(ctx, rundomain.CleanupLeaseRequest{
				DispatcherID: scheduler.config.Lease.InstanceID, DispatcherFencingToken: scheduler.config.Lease.FencingToken, Limit: 10,
			})
			if err != nil {
				return err
			}
			for _, job := range jobs {
				cleanupErr := runtime.CleanupProject(ctx, job)
				completion := rundomain.CleanupCompletionRequest{JobID: job.ID, DispatcherID: scheduler.config.Lease.InstanceID, DispatcherFencingToken: scheduler.config.Lease.FencingToken}
				if cleanupErr != nil {
					completion.Error = cleanupErr.Error()
				}
				if err := scheduler.cleanupControl.CompleteProjectCleanupJob(ctx, completion); err != nil {
					return err
				}
			}
		}
	}
	stateReply := make(chan CoordinatorState, 1)
	if err := coordinator.Submit(coordinatorContext, CoordinatorStateEvent{Reply: stateReply}); err != nil {
		return err
	}
	var coordinatorState CoordinatorState
	select {
	case coordinatorState = <-stateReply:
	case <-ctx.Done():
		return ctx.Err()
	}
	candidateLimit := scheduler.config.Policy.MaxWorkers - len(coordinatorState.Running)
	if scheduler.config.CatalogForRevision != nil {
		candidateLimit = rundomain.MaximumSchedulingCandidates
	}
	if candidateLimit < 1 {
		candidateLimit = 1
	}
	candidateLimit = min(candidateLimit, rundomain.MaximumSchedulingCandidates)
	observedRunIDs := make([]rundomain.RunID, 0, len(coordinatorState.Running))
	for _, active := range coordinatorState.Running {
		observedRunIDs = append(observedRunIDs, active.RunID)
	}
	snapshotRequest := SchedulingSnapshotRequest{
		DispatcherID: scheduler.config.Lease.InstanceID, DispatcherFencingToken: scheduler.config.Lease.FencingToken,
		SupportedTasks: scheduler.supportedTasks(), CandidateLimit: candidateLimit,
		ProjectCursor: coordinatorState.ProjectCursor, ObservedRunIDs: observedRunIDs,
	}
	snapshot, err := scheduler.planning.SchedulingSnapshot(ctx, snapshotRequest)
	if err != nil {
		return err
	}
	if err := snapshot.Validate(snapshotRequest); err != nil {
		return err
	}
	activePolicy, err := scheduler.policyForRevision(ctx, snapshot.DispatcherPolicy.ConfigRevision)
	if err != nil {
		return err
	}
	pollInterval := time.Duration(snapshot.DispatcherPolicy.PollIntervalSeconds) * time.Second
	if scheduler.config.PolicyForRevision != nil &&
		(activePolicy.PollInterval != pollInterval ||
			activePolicy.MaxWorkers != snapshot.DispatcherPolicy.MaxWorkers ||
			activePolicy.MaxRunningProjects != snapshot.DispatcherPolicy.MaxRunningProjects) {
		return fmt.Errorf("active Dispatcher policy does not match Config Revision %q", snapshot.DispatcherPolicy.ConfigRevision)
	}
	if scheduler.config.CatalogForRevision != nil {
		if _, err := scheduler.catalogForRevision(ctx, snapshot.DispatcherPolicy.ConfigRevision); err != nil {
			return err
		}
	}
	scheduler.pollInterval = pollInterval
	if err := coordinator.Submit(coordinatorContext, RunRevocationsEvent{RunIDs: snapshot.RevokedRunIDs}); err != nil {
		return err
	}
	if reconciler, ok := scheduler.config.Resources.(ProjectInventoryReconciler); ok {
		lookup := func(lookupContext context.Context, projectIDs []string) ([]rundomain.ProjectRuntimeStatusResult, error) {
			return scheduler.inventoryControl.ProjectRuntimeStatuses(lookupContext, rundomain.ProjectRuntimeStatusRequest{
				DispatcherID: scheduler.config.Lease.InstanceID, DispatcherFencingToken: scheduler.config.Lease.FencingToken,
				ProjectIDs: projectIDs,
			})
		}
		if err := reconciler.ReconcileProjectInventory(ctx, lookup); err != nil {
			return err
		}
	}
	workByKey := make(map[rundomain.WorkKey]plannedRun, len(snapshot.Work))
	candidates := make([]WorkCandidate, 0, len(snapshot.Work))
	policyByRevision := make(map[string]RunSchedulerPolicy)
	readinessByRevision := make(map[string]map[string]bool)
	for _, item := range snapshot.Work {
		catalog, err := scheduler.catalogForRevision(ctx, item.ConfigRevision)
		if err != nil {
			return err
		}
		revisionPolicy, ok := policyByRevision[item.ConfigRevision]
		if !ok {
			revisionPolicy, err = scheduler.policyForRevision(ctx, item.ConfigRevision)
			if err != nil {
				return err
			}
			policyByRevision[item.ConfigRevision] = revisionPolicy
		}
		ready := readinessByRevision[item.ConfigRevision]
		if ready == nil && scheduler.config.PrepareRevisionWorkers != nil {
			ready, err = scheduler.config.PrepareRevisionWorkers(ctx, item.ConfigRevision)
			if err != nil {
				return err
			}
			readinessByRevision[item.ConfigRevision] = ready
			for workerName, workerReady := range ready {
				if err := coordinator.Submit(coordinatorContext, WorkerReadinessEvent{
					ConfigRevision: item.ConfigRevision, WorkerName: workerName, Ready: workerReady,
				}); err != nil {
					return err
				}
			}
		}
		workers := scheduler.workerCandidates(catalog, item.Task, ready)
		if len(workers) == 0 {
			continue
		}
		workByKey[item.WorkKey] = plannedRun{
			projectID: item.ProjectID, configRevision: item.ConfigRevision, catalog: catalog,
			generation: item.Generation, revision: item.Revision,
			task: item.Task, target: item.Target, workKey: item.WorkKey, policy: revisionPolicy,
		}
		candidate := WorkCandidate{
			WorkKey: item.WorkKey, ProjectID: item.ProjectID, ConfigRevision: item.ConfigRevision, Task: item.Task,
			MaxProjectRunning: revisionPolicy.MaxProjectWorkers,
			RetryState:        item.State.RetryState, Attempt: item.State.Attempt,
			PreviousWorker: item.State.PreviousWorker, Workers: workers,
		}
		if item.State.NextEligibleAt != nil {
			candidate.NextEligibleAt = *item.State.NextEligibleAt
		}
		for index := range candidate.Workers {
			candidate.Workers[index].RetryAfter = item.State.WorkerRetryAfter[candidate.Workers[index].Name]
		}
		candidates = append(candidates, candidate)
	}

	reply := make(chan SchedulingPlan, 1)
	if err := coordinator.Submit(coordinatorContext, TickEvent{Snapshot: SchedulingSnapshot{
		Now: snapshot.ObservedAt, Capacity: snapshot.DispatcherPolicy.MaxWorkers,
		MaxRunningProjects: snapshot.DispatcherPolicy.MaxRunningProjects,
		Work:               candidates,
	}, NextProjectCursor: snapshot.NextProjectCursor, Reply: reply}); err != nil {
		return err
	}
	var plan SchedulingPlan
	select {
	case plan = <-reply:
	case <-ctx.Done():
		return ctx.Err()
	}
	var tickErrors []error
	for _, action := range plan.Actions {
		item, ok := workByKey[action.WorkKey]
		if !ok {
			continue
		}
		if err := scheduler.startAction(ctx, coordinatorContext, lifecycle, runs, action, item); err != nil {
			var domainErr *rundomain.DomainError
			if errors.As(err, &domainErr) {
				if domainErr.Code == rundomain.ErrorExecutionContractMismatch {
					return err
				}
				slog.Debug("Run proposal rejected by control plane", "work_key", action.WorkKey, "code", domainErr.Code)
				continue
			}
			tickErrors = append(tickErrors, err)
		}
	}
	return errors.Join(tickErrors...)
}

func isExecutionContractMismatch(err error) bool {
	var domainErr *rundomain.DomainError
	return errors.As(err, &domainErr) && domainErr.Code == rundomain.ErrorExecutionContractMismatch
}

func (scheduler *RunScheduler) catalogForRevision(ctx context.Context, revision string) (*worker.Catalog, error) {
	if revision == scheduler.config.ConfigRevision {
		return scheduler.config.Catalog, nil
	}
	if scheduler.config.CatalogForRevision == nil {
		return nil, fmt.Errorf("Worker Catalog for Config Revision %q is unavailable", revision)
	}
	return scheduler.config.CatalogForRevision(ctx, revision)
}

func (scheduler *RunScheduler) policyForRevision(ctx context.Context, revision string) (RunSchedulerPolicy, error) {
	if revision == scheduler.config.ConfigRevision {
		return scheduler.config.Policy, nil
	}
	if scheduler.config.PolicyForRevision == nil {
		return RunSchedulerPolicy{}, fmt.Errorf("Scheduler policy for Config Revision %q is unavailable", revision)
	}
	policy, err := scheduler.config.PolicyForRevision(ctx, revision)
	if err != nil {
		return RunSchedulerPolicy{}, err
	}
	if err := policy.Validate(); err != nil {
		return RunSchedulerPolicy{}, err
	}
	return policy, nil
}

func (scheduler *RunScheduler) supportedTasks() []rundomain.TaskType {
	if scheduler.config.CatalogForRevision != nil {
		return []rundomain.TaskType{rundomain.TaskBootstrap, rundomain.TaskReason, rundomain.TaskExplore}
	}
	seen := make(map[rundomain.TaskType]struct{})
	result := make([]rundomain.TaskType, 0, 3)
	for _, configured := range scheduler.config.Catalog.Workers() {
		for _, task := range configured.TaskTypes {
			if _, exists := seen[task]; exists {
				continue
			}
			seen[task] = struct{}{}
			result = append(result, task)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (scheduler *RunScheduler) workerCandidates(
	catalog *worker.Catalog,
	task rundomain.TaskType,
	readiness map[string]bool,
) []WorkerCandidate {
	result := make([]WorkerCandidate, 0)
	for _, configured := range catalog.Workers() {
		for _, supported := range configured.TaskTypes {
			if supported == task {
				ready := true
				if readiness != nil {
					ready = readiness[configured.Name]
				}
				result = append(result, WorkerCandidate{
					Name: configured.Name, Priority: configured.Priority,
					MaxRunning: configured.MaxRunning, Ready: ready,
				})
				break
			}
		}
	}
	return result
}

func (scheduler *RunScheduler) startAction(
	ctx context.Context,
	coordinatorContext context.Context,
	lifecycle *RunLifecycle,
	runs *sync.WaitGroup,
	action DispatchAction,
	item plannedRun,
) error {
	if item.policy.WorkerHealthcheck {
		if scheduler.config.PrepareRevisionWorker == nil {
			return fmt.Errorf("Config Revision %q requires a Worker readiness check", item.configRevision)
		}
		ready, err := scheduler.config.PrepareRevisionWorker(ctx, item.configRevision, action.WorkerName)
		if err != nil {
			return fmt.Errorf("check Worker %s readiness: %w", action.WorkerName, err)
		}
		if !ready {
			return &rundomain.DomainError{Code: rundomain.ErrorNotReady, Detail: "selected Worker is not ready"}
		}
	}
	var targetID *string
	if item.target != nil {
		value := item.target.ID
		targetID = &value
	}
	resolved, err := item.policy.ExecutionPolicy(action.WorkerName, action.Task)
	if err != nil {
		return err
	}
	prepared, err := scheduler.config.Executions.PrepareAdmission(AdmissionPreparation{
		ProjectID: item.projectID, ConfigRevision: item.configRevision, Catalog: item.catalog,
		Task: action.Task, TargetIntentID: targetID,
		CandidateGeneration: item.generation, CandidateRevision: item.revision, WorkKey: item.workKey,
		WorkerName: action.WorkerName, Image: rundomain.ImageSnapshot{RequestedRef: item.catalog.ImageReference()},
		DispatcherLease: scheduler.config.Lease,
		Timeouts:        resolved.Timeouts,
		RetryPolicy:     resolved.RetryPolicy, MaxIntents: resolved.MaxProposedIntents,
	})
	if err != nil {
		return err
	}
	active, err := lifecycle.Start(ctx, coordinatorContext, prepared)
	if err != nil {
		return err
	}
	runs.Add(1)
	go func() {
		defer runs.Done()
		if err := active.Run(); err != nil {
			slog.Error("Run lifecycle stopped", "error", err)
		}
	}()
	return nil
}
