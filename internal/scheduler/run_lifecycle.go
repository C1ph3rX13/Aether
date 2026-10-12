package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// RunLifecycleControl is the narrow control-plane surface needed to own one
// execution from admission through its terminal ledger transition.
type RunLifecycleControl interface {
	StartRun(context.Context, rundomain.StartRequest) (*rundomain.AdmittedRun, error)
	RenewRun(context.Context, rundomain.RenewRunRequest) (time.Time, error)
	FinishRun(context.Context, rundomain.FinishRequest) (*rundomain.FinishReceipt, error)
}

// RunLifecycleEvents is the typed event sink owned by the scheduler
// coordinator. The lifecycle never reaches into coordinator state directly.
type RunLifecycleEvents interface {
	Submit(context.Context, CoordinatorEvent) error
}

type RunLifecycleConfig struct {
	FinishTimeout time.Duration
}

// RunLifecycle is the single-run orchestration boundary. It knows about run
// domain transitions and runtime disposal, but not Docker containers or task
// specific execution providers.
type RunLifecycle struct {
	control   RunLifecycleControl
	resources RunResources
	events    RunLifecycleEvents
	config    RunLifecycleConfig
}

// LifecycleRun is the handle returned after a run has been admitted. Run owns
// the supervisor and terminal cleanup; callers only need to invoke Run.
type LifecycleRun struct {
	lifecycle          *RunLifecycle
	runContext         context.Context
	cancel             context.CancelCauseFunc
	coordinatorContext context.Context
	started            rundomain.StartedRun
	run                SupervisedRun
}

func NewRunLifecycle(
	control RunLifecycleControl,
	resources RunResources,
	events RunLifecycleEvents,
	config RunLifecycleConfig,
) (*RunLifecycle, error) {
	if control == nil || resources == nil || events == nil {
		return nil, fmt.Errorf("Run lifecycle dependencies are required")
	}
	if config.FinishTimeout == 0 {
		config.FinishTimeout = 10 * time.Second
	}
	if config.FinishTimeout < time.Second || config.FinishTimeout > time.Minute {
		return nil, fmt.Errorf("Run lifecycle finish timeout is invalid")
	}
	return &RunLifecycle{control: control, resources: resources, events: events, config: config}, nil
}

// Start admits the immutable prepared request and publishes RunStarted before
// returning the active handle. Any post-admission failure is resolved through
// the same Finish/Dispose path used by normal supervision.
func (lifecycle *RunLifecycle) Start(
	ctx context.Context,
	coordinatorContext context.Context,
	prepared PreparedAdmission,
) (*LifecycleRun, error) {
	if prepared.Binding == nil {
		return nil, fmt.Errorf("Run execution factory returned no Binding")
	}
	admitted, err := lifecycle.control.StartRun(ctx, prepared.Request)
	if err != nil {
		if admitted != nil && isExecutionContractMismatch(err) {
			mismatch := executionContractMismatch(err)
			if !lifecycle.resolveExecutionContractMismatch(ctx, coordinatorContext, admitted.StartedRun) {
				return nil, unresolvedContractMismatch(ctx, admitted.Run.ID)
			}
			return nil, mismatch
		}
		return nil, err
	}
	if admitted == nil {
		return nil, fmt.Errorf("Control Plane returned no Admitted Run")
	}
	executor, err := prepared.Binding.BindExecution(ctx, *admitted)
	if err != nil || executor == nil {
		if bindingCancellation := bindingCancellationError(ctx, err); bindingCancellation != nil {
			if !lifecycle.resolveAndDispose(
				ctx, coordinatorContext, admitted.StartedRun,
				rundomain.StatusInterrupted, rundomain.ErrorDispatcherRestarted,
			) {
				return nil, fmt.Errorf("Execution Binding cancellation left admitted Run %s unresolved: %w",
					admitted.Run.ID, bindingCancellation)
			}
			return nil, bindingCancellation
		}
		mismatch := executionContractMismatch(err)
		if !lifecycle.resolveExecutionContractMismatch(ctx, coordinatorContext, admitted.StartedRun) {
			return nil, unresolvedContractMismatch(ctx, admitted.Run.ID)
		}
		return nil, mismatch
	}
	started := admitted.StartedRun
	executorRun, err := NewExecutorRun(executor, started)
	if err != nil {
		if !lifecycle.resolveAndDispose(ctx, coordinatorContext, started, rundomain.StatusFailed, rundomain.ExecutionPrepareFailed) {
			return nil, fmt.Errorf("Executor creation failed and admitted Run %s remained unresolved: %w", started.Run.ID, err)
		}
		return nil, err
	}
	runContext, cancel := context.WithCancelCause(ctx)
	handle := &LifecycleRun{
		lifecycle: lifecycle, runContext: runContext, cancel: cancel,
		coordinatorContext: coordinatorContext, started: started, run: executorRun,
	}
	if err := lifecycle.events.Submit(coordinatorContext, RunStartedEvent{
		Run: RunningRunSnapshot{
			RunID: started.Run.ID, WorkKey: started.Run.WorkKey, ProjectID: started.Run.ProjectID,
			ConfigRevision: started.Run.Spec.ConfigRevision,
			Task:           started.Run.Task, WorkerName: started.Run.Spec.Worker.Name,
		}, Cancel: cancel,
	}); err != nil {
		cancel(err)
		if !lifecycle.resolveAndDispose(ctx, coordinatorContext, started, rundomain.StatusFailed, rundomain.ExecutionProcessFailed) {
			return nil, fmt.Errorf("publish RunStarted failed and admitted Run %s remained unresolved: %w", started.Run.ID, err)
		}
		return nil, err
	}
	return handle, nil
}

func bindingCancellationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return nil
}

func (lifecycle *RunLifecycle) resolveExecutionContractMismatch(
	lifecycleContext context.Context,
	coordinatorContext context.Context,
	started rundomain.StartedRun,
) bool {
	if !lifecycle.finishExecutionContractMismatch(lifecycleContext, started, rundomain.FinishRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		Termination: &rundomain.RunTermination{
			Status: rundomain.StatusFailed, Code: rundomain.ErrorExecutionContractMismatch,
		},
	}) {
		return false
	}
	if !lifecycle.disposeAndComplete(coordinatorContext, started.Run.ID) {
		slog.Error("terminal Execution Contract Mismatch cleanup remains unresolved", "run_id", started.Run.ID)
	}
	return true
}

func (lifecycle *RunLifecycle) finishExecutionContractMismatch(
	ctx context.Context,
	started rundomain.StartedRun,
	request rundomain.FinishRequest,
) bool {
	leaseTTL := started.Claim.LeaseExpiresAt.Sub(started.Claim.ClaimedAt)
	if leaseTTL <= 0 || leaseTTL > rundomain.MaximumLeaseExtension || leaseTTL/3 <= 0 {
		return false
	}
	lifecycle.renewMismatchClaim(ctx, started, leaseTTL)
	renewalContext, stopRenewal := context.WithCancel(ctx)
	renewalDone := make(chan struct{})
	go func() {
		defer close(renewalDone)
		lifecycle.maintainMismatchClaim(renewalContext, started, leaseTTL)
	}()
	defer func() {
		stopRenewal()
		<-renewalDone
	}()

	delay := 100 * time.Millisecond
	for {
		callTimeout := min(lifecycle.config.FinishTimeout, 2*time.Second)
		callContext, cancel := context.WithTimeout(ctx, callTimeout)
		receipt, err := lifecycle.control.FinishRun(callContext, request)
		cancel()
		if err == nil && receipt != nil && receipt.RunID == request.RunID &&
			receipt.Status == rundomain.StatusFailed &&
			receipt.Code == rundomain.ErrorExecutionContractMismatch {
			return true
		}
		if err == nil {
			err = fmt.Errorf("Control Plane returned no matching Execution Contract Mismatch receipt")
		}
		slog.Warn("retry Execution Contract Mismatch finish", "run_id", request.RunID,
			"error_code", executionContractControlErrorCode(err))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		if delay < 2*time.Second {
			delay = min(delay*2, 2*time.Second)
		}
	}
}

func (lifecycle *RunLifecycle) maintainMismatchClaim(
	ctx context.Context,
	started rundomain.StartedRun,
	leaseTTL time.Duration,
) {
	interval := leaseTTL / 3
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		lifecycle.renewMismatchClaim(ctx, started, leaseTTL)
	}
}

func (lifecycle *RunLifecycle) renewMismatchClaim(
	ctx context.Context,
	started rundomain.StartedRun,
	leaseTTL time.Duration,
) {
	interval := leaseTTL / 3
	callContext, cancel := context.WithTimeout(ctx, min(interval, 2*time.Second))
	_, err := lifecycle.control.RenewRun(callContext, rundomain.RenewRunRequest{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, LeaseExtension: leaseTTL,
	})
	cancel()
	if err != nil && ctx.Err() == nil {
		slog.Warn("retry Execution Contract Mismatch Claim renewal", "run_id", started.Run.ID,
			"error_code", executionContractControlErrorCode(err))
	}
}

func executionContractControlErrorCode(err error) string {
	var domainErr *rundomain.DomainError
	if errors.As(err, &domainErr) && domainErr.Code != "" {
		return string(domainErr.Code)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "control_error"
}

func unresolvedContractMismatch(ctx context.Context, runID rundomain.RunID) error {
	err := fmt.Errorf("Execution Contract Mismatch Run %s could not reach a terminal state", runID)
	if cause := context.Cause(ctx); cause != nil {
		return errors.Join(cause, err)
	}
	return err
}

func executionContractMismatch(err error) *rundomain.DomainError {
	detail := "Execution Binding could not reproduce admitted execution evidence"
	var domainErr *rundomain.DomainError
	if errors.As(err, &domainErr) && domainErr.Code == rundomain.ErrorExecutionContractMismatch &&
		safeExecutionContractDetail(domainErr.Detail) {
		detail = domainErr.Detail
	}
	return &rundomain.DomainError{Code: rundomain.ErrorExecutionContractMismatch, Detail: detail}
}

func safeExecutionContractDetail(detail string) bool {
	switch detail {
	case "Admitted Run ownership evidence does not match its request",
		"Admitted Run Task or Target evidence does not match its request",
		"Admitted Run Config Revision does not match its request",
		"Admitted Run Prompt Template identity does not match its request",
		"Admitted Run Worker evidence does not match its request",
		"Admitted Run Image evidence does not match its request",
		"Admitted Run Timeout evidence does not match its request",
		"Admitted Run Result Schema evidence does not match its request",
		"Admitted Run Max Proposed Intents evidence does not match its request",
		"Admitted Run Execution Spec is invalid",
		"Admitted Run Input coordinates do not match its candidate",
		"Admitted Run Task and Input discriminant do not match",
		"Admitted Run Blackboard Input identity does not match",
		"Admitted Run Bootstrap Input identity does not match",
		"Admitted Run Task evidence is invalid",
		"Execution Binding dependencies are unavailable",
		"Worker execution material could not be reproduced from admitted evidence",
		"Execution Binding deterministic material could not be reproduced",
		"Execution Binding Worker could not be reproduced",
		"Execution Binding Worker Adapter could not be reproduced",
		"Execution Binding Prompt Template could not be reproduced",
		"Execution Binding Input discriminant could not be reproduced",
		"Execution Binding rendered Input could not be reproduced",
		"Execution Binding Target Intent could not be reproduced",
		"Execution Binding Task could not be reproduced",
		"Execution Binding Prompt Template identity does not match Admission",
		"HTTP Finish receipt Run identity does not match its request",
		"HTTP Finish receipt terminal evidence does not match its request",
		"Executor dependencies do not match the admitted Run",
		"Started Run evidence is invalid during Execution Binding",
		"Reason execution provider is unavailable",
		"Reason execution material could not be reproduced",
		"Reason Adapter invocation could not be reproduced",
		"Explore execution provider is unavailable",
		"Explore execution material could not be reproduced",
		"Explore Adapter invocation could not be reproduced",
		"Bootstrap execution provider is unavailable",
		"Bootstrap execution material could not be reproduced",
		"Bootstrap Adapter invocation could not be reproduced",
		"admitted Task has no deterministic Execution Binding":
		return true
	}
	for _, task := range []string{"Reason", "Explore", "Bootstrap"} {
		for _, evidence := range []string{
			"Worker Adapter", "rendered Prompt", "Result Schema", "Prompt Template identity",
			"Blackboard Input identity", "Bootstrap Input identity", "rendered Input bytes or hash",
		} {
			if detail == task+" "+evidence+" does not match admitted evidence" {
				return true
			}
		}
	}
	return false
}

// Run supervises the process, commits exactly one terminal result, disposes
// the run resource, and finally emits RunCompletion. It returns only when the
// resource is either disposed or cleanup has been reported as unresolved.
func (active *LifecycleRun) Run() error {
	lifecycle := active.lifecycle
	started := active.started
	events := make(chan CoordinatorEvent)
	leaseTTL := started.Claim.LeaseExpiresAt.Sub(started.Claim.ClaimedAt)
	supervisor, err := NewRunSupervisor(lifecycle.control, active.run, events, RunSupervisorConfig{
		RunID: started.Run.ID, FencingToken: started.Run.FencingToken,
		DispatcherID: started.Run.DispatcherID, LeaseTTL: leaseTTL,
		LeaseExpiresAt: started.Claim.LeaseExpiresAt,
		Deadline:       started.Run.StartedAt.Add(time.Duration(started.Run.Spec.Timeouts.RunSeconds) * time.Second),
		StopGrace:      time.Duration(started.Run.Spec.Timeouts.GraceSeconds) * time.Second,
	})
	if err != nil {
		if !active.lifecycle.resolveAndDispose(active.runContext, active.coordinatorContext, active.started,
			rundomain.StatusFailed, rundomain.ExecutionProcessFailed) {
			return fmt.Errorf("supervisor creation failed and Run %s remained unresolved: %w", started.Run.ID, err)
		}
		return err
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(active.runContext) }()

	cleanupAllowed := false
	terminal := false
	supervisorDone := false
	for !terminal {
		select {
		case event := <-events:
			cleanupAllowed, terminal = active.handleEvent(event)
		case supervisorErr := <-done:
			supervisorDone = true
			if supervisorErr != nil {
				slog.Debug("Run supervisor stopped", "run_id", started.Run.ID, "error", supervisorErr)
				cleanupAllowed = active.resolve(rundomain.StatusFailed, rundomain.ExecutionProcessFailed)
			}
			terminal = true
		}
	}
	active.cancel(nil)
	if !supervisorDone {
		if supervisorErr := <-done; supervisorErr != nil {
			slog.Debug("Run supervisor exited after terminal event", "run_id", started.Run.ID, "error", supervisorErr)
		}
	}
	if !cleanupAllowed {
		slog.Error("Run remains supervised in the ledger after unresolved Finish", "run_id", started.Run.ID)
		return fmt.Errorf("finish Run %s remained unresolved", started.Run.ID)
	}
	if !active.lifecycle.disposeAndComplete(active.coordinatorContext, started.Run.ID) {
		return fmt.Errorf("dispose Run %s remained unresolved", started.Run.ID)
	}
	return nil
}

func (active *LifecycleRun) handleEvent(event CoordinatorEvent) (bool, bool) {
	started := active.started
	switch typed := event.(type) {
	case LeaseRenewedEvent:
		if err := active.lifecycle.events.Submit(active.coordinatorContext, typed); err != nil {
			slog.Warn("publish Run lease renewal", "run_id", started.Run.ID, "error", err)
		}
		return false, false
	case RunExitedEvent:
		allowed := active.finishCompletion(typed.Completion)
		if allowed {
			_ = active.lifecycle.events.Submit(active.coordinatorContext, typed)
		}
		return allowed, true
	case RunDeadlineEvent:
		allowed := active.resolve(rundomain.StatusFailed, rundomain.ExecutionDeadline)
		if allowed {
			_ = active.lifecycle.events.Submit(active.coordinatorContext, typed)
		}
		return allowed, true
	case RunInterruptedEvent:
		allowed := active.resolve(rundomain.StatusInterrupted, rundomain.ErrorDispatcherRestarted)
		if allowed {
			_ = active.lifecycle.events.Submit(active.coordinatorContext, typed)
		}
		return allowed, true
	case LeaseLostEvent:
		_ = active.lifecycle.events.Submit(active.coordinatorContext, typed)
		return true, true
	default:
		return false, false
	}
}

func (active *LifecycleRun) finishCompletion(completion rundomain.Completion) bool {
	request, err := completion.FinishRequest()
	if err != nil {
		slog.Error("build Run completion", "run_id", active.started.Run.ID, "error", err)
		return false
	}
	return active.finish(request)
}

func (active *LifecycleRun) resolve(status rundomain.Status, code rundomain.ErrorCode) bool {
	return active.finish(rundomain.FinishRequest{
		RunID: active.started.Run.ID, FencingToken: active.started.Run.FencingToken,
		Termination: &rundomain.RunTermination{Status: status, Code: code},
	})
}

func (lifecycle *RunLifecycle) resolveAndDispose(
	lifecycleContext context.Context,
	coordinatorContext context.Context,
	run rundomain.StartedRun,
	status rundomain.Status,
	code rundomain.ErrorCode,
) bool {
	finishContext, finishCancel := context.WithTimeout(context.WithoutCancel(lifecycleContext), lifecycle.config.FinishTimeout)
	defer finishCancel()
	if !lifecycle.finishWithContext(finishContext, rundomain.FinishRequest{
		RunID: run.Run.ID, FencingToken: run.Run.FencingToken,
		Termination: &rundomain.RunTermination{Status: status, Code: code},
	}) {
		return false
	}
	return lifecycle.disposeAndComplete(coordinatorContext, run.Run.ID)
}

func (active *LifecycleRun) finish(request rundomain.FinishRequest) bool {
	ctx, cancel := context.WithTimeout(context.Background(), active.lifecycle.config.FinishTimeout)
	defer cancel()
	return active.lifecycle.finishWithContext(ctx, request)
}

func (lifecycle *RunLifecycle) finishWithContext(ctx context.Context, request rundomain.FinishRequest) bool {
	delay := 100 * time.Millisecond
	for {
		callTimeout := min(lifecycle.config.FinishTimeout, 2*time.Second)
		callContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
		_, err := lifecycle.control.FinishRun(callContext, request)
		cancel()
		if err == nil || finishResolvedByControlPlane(err) {
			return true
		}
		slog.Warn("retry Run finish", "run_id", request.RunID, "error", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		if delay < 2*time.Second {
			delay = min(delay*2, 2*time.Second)
		}
	}
}

func finishResolvedByControlPlane(err error) bool {
	var domainErr *rundomain.DomainError
	if !errors.As(err, &domainErr) {
		return false
	}
	return domainErr.Code == rundomain.ErrorFenced || domainErr.Code == rundomain.ErrorNotFound ||
		domainErr.Code == rundomain.ErrorResultConflict
}

func (lifecycle *RunLifecycle) disposeAndComplete(
	coordinatorContext context.Context,
	runID rundomain.RunID,
) bool {
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), lifecycle.config.FinishTimeout)
	defer cleanupCancel()
	delay := 100 * time.Millisecond
	for {
		callContext, cancel := context.WithTimeout(cleanupContext, min(lifecycle.config.FinishTimeout, 2*time.Second))
		err := lifecycle.resources.CompleteRun(callContext, runID)
		cancel()
		if err == nil {
			_ = lifecycle.events.Submit(coordinatorContext, RunCompletionEvent{RunID: runID})
			return true
		}
		slog.Warn("retry Run execution cleanup", "run_id", runID, "error", err)
		timer := time.NewTimer(delay)
		select {
		case <-cleanupContext.Done():
			timer.Stop()
			slog.Error("Run execution cleanup remains unresolved", "run_id", runID, "error", err)
			return false
		case <-timer.C:
		}
		delay = min(delay*2, 2*time.Second)
	}
}
