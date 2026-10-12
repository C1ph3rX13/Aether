package scheduler

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type lifecycleRecorder struct {
	mu     sync.Mutex
	events []string
}

func (recorder *lifecycleRecorder) add(event string) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
}

func (recorder *lifecycleRecorder) snapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.events...)
}

type lifecycleTestControl struct {
	recorder       *lifecycleRecorder
	started        rundomain.AdmittedRun
	startError     error
	finishFailures int
	finishErrors   []error
	finishReceipts []*rundomain.FinishReceipt
	finishBlocked  <-chan struct{}
	renewError     error
	enforceClaim   bool
	claimExpiresAt time.Time
	renewals       int
	mu             sync.Mutex
	finishes       []rundomain.FinishRequest
}

func (control *lifecycleTestControl) StartRun(context.Context, rundomain.StartRequest) (*rundomain.AdmittedRun, error) {
	control.recorder.add("start")
	started := control.started
	return &started, control.startError
}

func (control *lifecycleTestControl) RenewRun(_ context.Context, request rundomain.RenewRunRequest) (time.Time, error) {
	control.recorder.add("renew")
	control.mu.Lock()
	defer control.mu.Unlock()
	control.renewals++
	if control.renewError != nil {
		return time.Time{}, control.renewError
	}
	now := time.Now().UTC()
	if control.enforceClaim && !now.Before(control.claimExpiresAt) {
		return time.Time{}, &rundomain.DomainError{Code: rundomain.ErrorFenced, Detail: "test Claim expired"}
	}
	control.claimExpiresAt = now.Add(request.LeaseExtension)
	return control.claimExpiresAt, nil
}

func (control *lifecycleTestControl) FinishRun(
	ctx context.Context,
	request rundomain.FinishRequest,
) (*rundomain.FinishReceipt, error) {
	control.recorder.add("finish")
	control.mu.Lock()
	control.finishes = append(control.finishes, request)
	blocked := control.finishBlocked
	control.mu.Unlock()
	if blocked != nil {
		select {
		case <-blocked:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.enforceClaim && !time.Now().UTC().Before(control.claimExpiresAt) {
		return nil, &rundomain.DomainError{Code: rundomain.ErrorFenced, Detail: "test Claim expired"}
	}
	if len(control.finishErrors) > 0 {
		err := control.finishErrors[0]
		control.finishErrors = control.finishErrors[1:]
		return nil, err
	}
	if len(control.finishReceipts) > 0 {
		receipt := control.finishReceipts[0]
		control.finishReceipts = control.finishReceipts[1:]
		return receipt, nil
	}
	if control.finishFailures > 0 {
		control.finishFailures--
		return nil, errors.New("temporary finish failure")
	}
	receipt := &rundomain.FinishReceipt{RunID: request.RunID, Status: rundomain.StatusCommitted}
	if request.Termination != nil {
		receipt.Status = request.Termination.Status
		receipt.Code = request.Termination.Code
	}
	return receipt, nil
}

func (control *lifecycleTestControl) finishRequests() []rundomain.FinishRequest {
	control.mu.Lock()
	defer control.mu.Unlock()
	return append([]rundomain.FinishRequest(nil), control.finishes...)
}

type lifecycleTestResources struct {
	recorder        *lifecycleRecorder
	disposeFailures int
	mu              sync.Mutex
	disposals       int
}

func (resources *lifecycleTestResources) CompleteRun(context.Context, rundomain.RunID) error {
	resources.recorder.add("dispose")
	resources.mu.Lock()
	defer resources.mu.Unlock()
	resources.disposals++
	if resources.disposeFailures > 0 {
		resources.disposeFailures--
		return errors.New("temporary dispose failure")
	}
	return nil
}

func (*lifecycleTestResources) StopExecutionHandle(context.Context, rundomain.ExecutionHandle) error {
	return nil
}

type lifecycleTestEvents struct {
	recorder *lifecycleRecorder
	mu       sync.Mutex
	cancel   context.CancelCauseFunc
	types    []string
}

func (events *lifecycleTestEvents) Submit(_ context.Context, event CoordinatorEvent) error {
	name := "unknown"
	switch typed := event.(type) {
	case RunStartedEvent:
		name = "started"
		events.mu.Lock()
		events.cancel = typed.Cancel
		events.mu.Unlock()
	case RunExitedEvent:
		name = "exited"
	case RunDeadlineEvent:
		name = "deadline"
	case LeaseLostEvent:
		name = "lease_lost"
	case RunCompletionEvent:
		name = "completion"
	case LeaseRenewedEvent:
		name = "lease_renewed"
	}
	events.recorder.add("event:" + name)
	events.mu.Lock()
	events.types = append(events.types, name)
	events.mu.Unlock()
	return nil
}

func (events *lifecycleTestEvents) cancelRun(cause error) {
	events.mu.Lock()
	cancel := events.cancel
	events.mu.Unlock()
	if cancel != nil {
		cancel(cause)
	}
}

type lifecycleTestExecutor struct {
	recorder            *lifecycleRecorder
	waitForCancellation bool
	completion          rundomain.Completion
}

type lifecycleTestBinding struct {
	recorder *lifecycleRecorder
	executor rundomain.Executor
	err      error
}

func (binding lifecycleTestBinding) BindExecution(context.Context, rundomain.AdmittedRun) (rundomain.Executor, error) {
	binding.recorder.add("bind")
	return binding.executor, binding.err
}

func (executor *lifecycleTestExecutor) Execute(ctx context.Context, started rundomain.StartedRun) rundomain.Completion {
	executor.recorder.add("execute")
	if executor.waitForCancellation {
		<-ctx.Done()
	}
	completion := executor.completion
	completion.RunID = started.Run.ID
	completion.FencingToken = started.Run.FencingToken
	return completion
}

func TestRunLifecycleOwnsStartFinishAndDisposeRetryOrder(t *testing.T) {
	recorder := &lifecycleRecorder{}
	control := &lifecycleTestControl{recorder: recorder, started: lifecycleStartedRun(time.Now().UTC(), time.Second), finishFailures: 1}
	resources := &lifecycleTestResources{recorder: recorder, disposeFailures: 1}
	events := &lifecycleTestEvents{recorder: recorder}
	lifecycle, err := NewRunLifecycle(control, resources, events, RunLifecycleConfig{FinishTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	executor := &lifecycleTestExecutor{recorder: recorder, completion: rundomain.Completion{
		Kind: rundomain.CompletionFailed, Code: rundomain.ExecutionProcessFailed,
	}}
	active, err := lifecycle.Start(t.Context(), t.Context(), PreparedAdmission{
		Request: rundomain.StartRequest{},
		Binding: lifecycleTestBinding{recorder: recorder, executor: executor},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := active.Run(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"start", "bind", "event:started", "execute", "finish", "finish",
		"event:exited", "dispose", "dispose", "event:completion",
	}
	if got := recorder.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle order:\n got: %v\nwant: %v", got, want)
	}
}

func TestRunLifecycleMakesPostAdmissionBindingFailureTerminal(t *testing.T) {
	recorder := &lifecycleRecorder{}
	control := &lifecycleTestControl{
		recorder: recorder, started: lifecycleStartedRun(time.Now().UTC(), time.Second),
	}
	resources := &lifecycleTestResources{recorder: recorder}
	events := &lifecycleTestEvents{recorder: recorder}
	lifecycle, err := NewRunLifecycle(control, resources, events, RunLifecycleConfig{FinishTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	active, err := lifecycle.Start(t.Context(), t.Context(), PreparedAdmission{
		Binding: lifecycleTestBinding{
			recorder: recorder,
			err: &rundomain.DomainError{
				Code:   rundomain.ErrorExecutionContractMismatch,
				Detail: "prompt=private input=private /runtime/private SECRET_VALUE",
			},
		},
	})
	if err == nil || active != nil {
		t.Fatalf("binding failure: active=%v error=%v", active, err)
	}
	var domainErr *rundomain.DomainError
	if !errors.As(err, &domainErr) || domainErr.Code != rundomain.ErrorExecutionContractMismatch {
		t.Fatalf("binding failure classification: %v", err)
	}
	for _, sensitive := range []string{"prompt=private", "input=private", "/runtime/private", "SECRET_VALUE"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("binding failure diagnostic exposed %q: %v", sensitive, err)
		}
	}
	wantOrder := []string{"start", "bind", "renew", "finish", "dispose", "event:completion"}
	if got := recorder.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("binding failure order: got=%v want=%v", got, wantOrder)
	}
	finishes := control.finishRequests()
	wantTermination := &rundomain.RunTermination{
		Status: rundomain.StatusFailed, Code: rundomain.ErrorExecutionContractMismatch,
	}
	if len(finishes) != 1 || !reflect.DeepEqual(finishes[0].Termination, wantTermination) {
		t.Fatalf("binding failure termination: got=%+v want=%+v", finishes, wantTermination)
	}
	if containsLifecycleEvent(events, "started") {
		t.Fatalf("binding failure published RunStarted: %v", recorder.snapshot())
	}
}

func TestRunLifecycleKeepsMismatchQuiescedUntilTerminalizationSucceeds(t *testing.T) {
	recorder := &lifecycleRecorder{}
	unblockFinish := make(chan struct{})
	started := lifecycleStartedRun(time.Now().UTC(), time.Second)
	control := &lifecycleTestControl{
		recorder: recorder, started: started, finishBlocked: unblockFinish,
		enforceClaim: true, claimExpiresAt: started.Claim.LeaseExpiresAt,
	}
	resources := &lifecycleTestResources{recorder: recorder}
	events := &lifecycleTestEvents{recorder: recorder}
	lifecycle, err := NewRunLifecycle(control, resources, events, RunLifecycleConfig{FinishTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, startErr := lifecycle.Start(t.Context(), t.Context(), PreparedAdmission{
			Binding: lifecycleTestBinding{recorder: recorder, err: errors.New("binding mismatch")},
		})
		done <- startErr
	}()
	select {
	case err := <-done:
		t.Fatalf("mismatch left quiescing before terminalization recovered: %v", err)
	case <-time.After(1100 * time.Millisecond):
	}
	close(unblockFinish)
	select {
	case err := <-done:
		var domainErr *rundomain.DomainError
		if !errors.As(err, &domainErr) || domainErr.Code != rundomain.ErrorExecutionContractMismatch {
			t.Fatalf("terminalized mismatch classification: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("mismatch did not leave quiescing after terminalization recovered")
	}
	control.mu.Lock()
	renewals := control.renewals
	control.mu.Unlock()
	if renewals < 2 {
		t.Fatalf("Execution Contract Mismatch Claim renewed %d times during terminalization", renewals)
	}
}

func TestRunLifecycleRequiresExactMismatchReceiptBeforeFailingClosed(t *testing.T) {
	recorder := &lifecycleRecorder{}
	control := &lifecycleTestControl{
		recorder: recorder, started: lifecycleStartedRun(time.Now().UTC(), time.Second),
		finishErrors: []error{
			&rundomain.DomainError{Code: rundomain.ErrorFenced, Detail: "stale Claim"},
			&rundomain.DomainError{Code: rundomain.ErrorNotFound, Detail: "missing Run"},
			&rundomain.DomainError{Code: rundomain.ErrorResultConflict, Detail: "different terminal"},
		},
		finishReceipts: []*rundomain.FinishReceipt{{
			RunID: "different-run", Status: rundomain.StatusFailed,
			Code: rundomain.ErrorExecutionContractMismatch,
		}},
	}
	lifecycle, err := NewRunLifecycle(
		control,
		&lifecycleTestResources{recorder: recorder},
		&lifecycleTestEvents{recorder: recorder},
		RunLifecycleConfig{FinishTimeout: time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	active, err := lifecycle.Start(t.Context(), t.Context(), PreparedAdmission{
		Binding: lifecycleTestBinding{recorder: recorder, err: errors.New("binding mismatch")},
	})
	if active != nil || !isExecutionContractMismatch(err) {
		t.Fatalf("mismatch terminal confirmation: active=%v error=%v", active, err)
	}
	finishes := control.finishRequests()
	if len(finishes) != 5 || finishes[len(finishes)-1].Termination == nil ||
		finishes[len(finishes)-1].Termination.Code != rundomain.ErrorExecutionContractMismatch {
		t.Fatalf("mismatch terminal attempts: %+v", finishes)
	}
}

func TestRunLifecycleResolvesAdmittedTransportContractMismatchBeforeFailing(t *testing.T) {
	recorder := &lifecycleRecorder{}
	control := &lifecycleTestControl{
		recorder: recorder, started: lifecycleStartedRun(time.Now().UTC(), time.Second),
		startError: &rundomain.DomainError{
			Code:   rundomain.ErrorExecutionContractMismatch,
			Detail: "Admitted Run Prompt Template identity does not match its request",
		},
	}
	resources := &lifecycleTestResources{recorder: recorder}
	events := &lifecycleTestEvents{recorder: recorder}
	lifecycle, err := NewRunLifecycle(control, resources, events, RunLifecycleConfig{FinishTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	active, err := lifecycle.Start(t.Context(), t.Context(), PreparedAdmission{
		Binding: lifecycleTestBinding{recorder: recorder},
	})
	var domainErr *rundomain.DomainError
	if active != nil || !errors.As(err, &domainErr) || domainErr.Code != rundomain.ErrorExecutionContractMismatch {
		t.Fatalf("transport mismatch: active=%v error=%v", active, err)
	}
	if domainErr.Detail != "Admitted Run Prompt Template identity does not match its request" {
		t.Fatalf("transport mismatch lost safe field diagnostic: %q", domainErr.Detail)
	}
	wantOrder := []string{"start", "renew", "finish", "dispose", "event:completion"}
	if got := recorder.snapshot(); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("transport mismatch order: got=%v want=%v", got, wantOrder)
	}
	finishes := control.finishRequests()
	if len(finishes) != 1 || finishes[0].Termination == nil ||
		finishes[0].Termination.Code != rundomain.ErrorExecutionContractMismatch {
		t.Fatalf("transport mismatch termination: %+v", finishes)
	}
}

func TestRunLifecycleDoesNotClassifyBindingCancellationAsContractMismatch(t *testing.T) {
	recorder := &lifecycleRecorder{}
	control := &lifecycleTestControl{
		recorder: recorder, started: lifecycleStartedRun(time.Now().UTC(), time.Second),
	}
	resources := &lifecycleTestResources{recorder: recorder}
	events := &lifecycleTestEvents{recorder: recorder}
	lifecycle, err := NewRunLifecycle(control, resources, events, RunLifecycleConfig{FinishTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithCancel(t.Context())
	cancelRun()
	active, err := lifecycle.Start(runContext, t.Context(), PreparedAdmission{
		Binding: lifecycleTestBinding{
			recorder: recorder,
			err:      fmt.Errorf("prompt=private /runtime/private SECRET_VALUE: %w", context.Canceled),
		},
	})
	if active != nil || !errors.Is(err, context.Canceled) || isExecutionContractMismatch(err) {
		t.Fatalf("Binding cancellation classification: active=%v error=%v", active, err)
	}
	for _, sensitive := range []string{"prompt=private", "/runtime/private", "SECRET_VALUE"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("Binding cancellation diagnostic exposed %q: %v", sensitive, err)
		}
	}
	finishes := control.finishRequests()
	want := &rundomain.RunTermination{
		Status: rundomain.StatusInterrupted, Code: rundomain.ErrorDispatcherRestarted,
	}
	if len(finishes) != 1 || !reflect.DeepEqual(finishes[0].Termination, want) {
		t.Fatalf("Binding cancellation termination: got=%+v want=%+v", finishes, want)
	}
}

func TestRunLifecycleMapsSupervisorTerminalEvents(t *testing.T) {
	tests := []struct {
		name            string
		started         rundomain.AdmittedRun
		renewError      error
		cancelCause     error
		wantEvent       string
		wantTermination *rundomain.RunTermination
	}{
		{
			name: "deadline", started: lifecycleStartedRun(time.Now().UTC(), 0), wantEvent: "deadline",
			wantTermination: &rundomain.RunTermination{Status: rundomain.StatusFailed, Code: rundomain.ExecutionDeadline},
		},
		{
			name: "lease lost", started: lifecycleStartedRunWithLease(time.Now().UTC(), 10*time.Second, 30*time.Millisecond),
			renewError: &rundomain.DomainError{Code: rundomain.ErrorFenced, Detail: "lease lost"},
			wantEvent:  "lease_lost",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &lifecycleRecorder{}
			control := &lifecycleTestControl{recorder: recorder, started: test.started, renewError: test.renewError}
			resources := &lifecycleTestResources{recorder: recorder}
			events := &lifecycleTestEvents{recorder: recorder}
			lifecycle, err := NewRunLifecycle(control, resources, events, RunLifecycleConfig{FinishTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			executor := &lifecycleTestExecutor{
				recorder: recorder, waitForCancellation: true,
				completion: rundomain.Completion{Kind: rundomain.CompletionCancelled},
			}
			active, err := lifecycle.Start(t.Context(), t.Context(), PreparedAdmission{
				Binding: lifecycleTestBinding{recorder: recorder, executor: executor},
			})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- active.Run() }()
			if test.cancelCause != nil {
				events.cancelRun(test.cancelCause)
			}
			select {
			case err := <-done:
				if err != nil && test.wantEvent != "lease_lost" {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Run lifecycle did not reach a terminal state")
			}
			if !containsLifecycleEvent(events, test.wantEvent) {
				t.Fatalf("missing %s event: %v", test.wantEvent, recorder.snapshot())
			}
			finishes := control.finishRequests()
			if test.wantTermination == nil {
				if len(finishes) != 0 {
					t.Fatalf("lease loss must not finish a fenced Run: %+v", finishes)
				}
			} else if len(finishes) != 1 || !reflect.DeepEqual(finishes[0].Termination, test.wantTermination) {
				t.Fatalf("termination: %+v, want %+v", finishes, test.wantTermination)
			}
		})
	}
}

func containsLifecycleEvent(events *lifecycleTestEvents, want string) bool {
	events.mu.Lock()
	defer events.mu.Unlock()
	for _, event := range events.types {
		if event == want {
			return true
		}
	}
	return false
}

func lifecycleStartedRun(startedAt time.Time, runTimeout time.Duration) rundomain.AdmittedRun {
	return lifecycleStartedRunWithLease(startedAt, runTimeout, time.Second)
}

func lifecycleStartedRunWithLease(startedAt time.Time, runTimeout, leaseTTL time.Duration) rundomain.AdmittedRun {
	return rundomain.AdmittedRun{StartedRun: rundomain.StartedRun{
		Run: rundomain.ExecutionRun{
			ID: "run-lifecycle", WorkKey: "work-lifecycle", ProjectID: "project-lifecycle",
			Task: rundomain.TaskReason, FencingToken: 7, DispatcherID: "dispatcher-lifecycle",
			StartedAt: startedAt,
			Spec: rundomain.ExecutionSpecSnapshot{
				Worker: rundomain.WorkerSnapshot{Name: "worker-lifecycle"},
				Timeouts: rundomain.TimeoutSnapshot{
					RunSeconds: int(runTimeout / time.Second), GraceSeconds: 1,
				},
			},
		},
		Claim: rundomain.Claim{
			RunID: "run-lifecycle", ClaimedAt: startedAt, LeaseExpiresAt: startedAt.Add(leaseTTL),
		},
	}}
}
