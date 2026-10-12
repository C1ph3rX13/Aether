package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type supervisorTestProcess struct {
	completed  chan struct{}
	completion rundomain.Completion
	err        error
	stopped    chan struct{}
	stopOnce   sync.Once
}

func newSupervisorTestProcess() *supervisorTestProcess {
	return &supervisorTestProcess{completed: make(chan struct{}), stopped: make(chan struct{})}
}

func (process *supervisorTestProcess) Wait(ctx context.Context) (rundomain.Completion, error) {
	select {
	case <-process.completed:
		return process.completion, process.err
	case <-ctx.Done():
		return rundomain.Completion{}, ctx.Err()
	}
}

func (process *supervisorTestProcess) Stop(context.Context, time.Duration) error {
	process.stopOnce.Do(func() { close(process.stopped) })
	return nil
}

type supervisorTestLeaseControl struct {
	renewed chan rundomain.RenewRunRequest
	err     error
	ttl     time.Duration
}

type blockingLeaseControl struct {
	started chan struct{}
}

func (control *blockingLeaseControl) RenewRun(ctx context.Context, _ rundomain.RenewRunRequest) (time.Time, error) {
	select {
	case control.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return time.Time{}, ctx.Err()
}

func (control *supervisorTestLeaseControl) RenewRun(
	_ context.Context,
	request rundomain.RenewRunRequest,
) (time.Time, error) {
	control.renewed <- request
	if control.err != nil {
		return time.Time{}, control.err
	}
	return time.Now().UTC().Add(control.ttl), nil
}

func TestRunSupervisorRenewsQuietRunIndependentlyOfOutput(t *testing.T) {
	process := newSupervisorTestProcess()
	control := &supervisorTestLeaseControl{renewed: make(chan rundomain.RenewRunRequest, 4), ttl: 150 * time.Millisecond}
	events := make(chan CoordinatorEvent, 8)
	supervisor, err := NewRunSupervisor(control, process, events, RunSupervisorConfig{
		RunID: "run-quiet", FencingToken: 7, DispatcherID: "dispatcher",
		LeaseTTL: 150 * time.Millisecond, LeaseExpiresAt: time.Now().Add(150 * time.Millisecond),
		RenewInterval: 10 * time.Millisecond, StopGrace: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(t.Context()) }()
	select {
	case request := <-control.renewed:
		if request.RunID != "run-quiet" || request.FencingToken != 7 {
			t.Fatalf("renew request: %+v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("quiet Run was not renewed")
	}
	if event := <-events; event.(LeaseRenewedEvent).RunID != "run-quiet" {
		t.Fatalf("renew event: %+v", event)
	}
	process.completion = rundomain.Completion{RunID: "run-quiet", FencingToken: 7, Kind: rundomain.CompletionResultReady}
	close(process.completed)
	for {
		event := <-events
		if exited, ok := event.(RunExitedEvent); ok {
			if exited.RunID != "run-quiet" || exited.Completion.Kind != rundomain.CompletionResultReady {
				t.Fatalf("exit event: %+v", event)
			}
			break
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunSupervisorStopsImmediatelyWhenFenced(t *testing.T) {
	process := newSupervisorTestProcess()
	fenced := &rundomain.DomainError{Code: rundomain.ErrorFenced, Detail: "claim lost"}
	control := &supervisorTestLeaseControl{renewed: make(chan rundomain.RenewRunRequest, 1), err: fenced}
	events := make(chan CoordinatorEvent, 4)
	supervisor, err := NewRunSupervisor(control, process, events, RunSupervisorConfig{
		RunID: "run-fenced", FencingToken: 3, DispatcherID: "dispatcher",
		LeaseTTL: 200 * time.Millisecond, LeaseExpiresAt: time.Now().Add(200 * time.Millisecond),
		RenewInterval: 10 * time.Millisecond, StopGrace: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(t.Context()) }()
	select {
	case event := <-events:
		lost, ok := event.(LeaseLostEvent)
		if !ok || lost.RunID != "run-fenced" || !errors.Is(lost.Err, fenced) {
			t.Fatalf("lease loss event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing LeaseLost event")
	}
	select {
	case <-process.stopped:
	case <-time.After(time.Second):
		t.Fatal("fenced process was not stopped")
	}
	if err := <-done; !errors.Is(err, fenced) {
		t.Fatalf("Supervisor error: %v", err)
	}
}

func TestRunSupervisorStopsAtRunDeadline(t *testing.T) {
	process := newSupervisorTestProcess()
	control := &supervisorTestLeaseControl{renewed: make(chan rundomain.RenewRunRequest, 1), ttl: time.Second}
	events := make(chan CoordinatorEvent, 4)
	supervisor, err := NewRunSupervisor(control, process, events, RunSupervisorConfig{
		RunID: "run-deadline", FencingToken: 1, DispatcherID: "dispatcher",
		LeaseTTL: time.Second, LeaseExpiresAt: time.Now().Add(time.Second),
		RenewInterval: 500 * time.Millisecond, Deadline: time.Now().Add(20 * time.Millisecond),
		StopGrace: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(t.Context()) }()
	select {
	case event := <-events:
		if deadline, ok := event.(RunDeadlineEvent); !ok || deadline.RunID != "run-deadline" {
			t.Fatalf("deadline event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing deadline event")
	}
	select {
	case <-process.stopped:
	case <-time.After(time.Second):
		t.Fatal("deadline process was not stopped")
	}
	if err := <-done; err != nil {
		t.Fatalf("deadline Supervisor: %v", err)
	}
}

func TestRunSupervisorTreatsControlPlaneRevocationAsResolvedLeaseLoss(t *testing.T) {
	process := newSupervisorTestProcess()
	control := &supervisorTestLeaseControl{renewed: make(chan rundomain.RenewRunRequest, 1), ttl: time.Second}
	events := make(chan CoordinatorEvent, 4)
	supervisor, err := NewRunSupervisor(control, process, events, RunSupervisorConfig{
		RunID: "run-revoked", FencingToken: 1, DispatcherID: "dispatcher",
		LeaseTTL: time.Second, LeaseExpiresAt: time.Now().Add(time.Second),
		RenewInterval: 500 * time.Millisecond, StopGrace: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	cancel(errRunRevokedCancellation)
	select {
	case event := <-events:
		lost, ok := event.(LeaseLostEvent)
		if !ok || lost.RunID != "run-revoked" || !errors.Is(lost.Err, errRunRevokedCancellation) {
			t.Fatalf("Control Plane revocation event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing Control Plane revocation event")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunSupervisorDeadlineIsResponsiveWhileRenewalIsBlocked(t *testing.T) {
	process := newSupervisorTestProcess()
	control := &blockingLeaseControl{started: make(chan struct{}, 1)}
	events := make(chan CoordinatorEvent, 4)
	supervisor, err := NewRunSupervisor(control, process, events, RunSupervisorConfig{
		RunID: "run-blocked-renewal", FencingToken: 1, DispatcherID: "dispatcher",
		LeaseTTL: 200 * time.Millisecond, LeaseExpiresAt: time.Now().Add(200 * time.Millisecond),
		RenewInterval: 5 * time.Millisecond, Deadline: time.Now().Add(30 * time.Millisecond),
		StopGrace: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(t.Context()) }()
	select {
	case <-control.started:
	case <-time.After(time.Second):
		t.Fatal("renewal did not start")
	}
	select {
	case event := <-events:
		if _, ok := event.(RunDeadlineEvent); !ok {
			t.Fatalf("event while renewal blocked: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked renewal prevented Run deadline")
	}
	select {
	case <-process.stopped:
	case <-time.After(time.Second):
		t.Fatal("blocked renewal prevented process stop")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunSupervisorStopsRunWhenCoordinatorEventDeliveryFails(t *testing.T) {
	process := newSupervisorTestProcess()
	control := &supervisorTestLeaseControl{renewed: make(chan rundomain.RenewRunRequest, 1), ttl: time.Second}
	events := make(chan CoordinatorEvent)
	supervisor, err := NewRunSupervisor(control, process, events, RunSupervisorConfig{
		RunID: "run-event-timeout", FencingToken: 1, DispatcherID: "dispatcher",
		LeaseTTL: time.Second, LeaseExpiresAt: time.Now().Add(time.Second),
		RenewInterval: 500 * time.Millisecond, StopGrace: time.Millisecond,
		EventTimeout: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	close(process.completed)
	err = supervisor.Run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "emit RunSupervisor event") {
		t.Fatalf("event delivery error: %v", err)
	}
	select {
	case <-process.stopped:
	case <-time.After(time.Second):
		t.Fatal("event delivery failure did not stop Run")
	}
}
