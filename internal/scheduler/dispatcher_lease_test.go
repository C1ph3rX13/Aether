package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type dispatcherLeaseControlFake struct {
	mu        sync.Mutex
	acquired  int
	renewed   int
	released  int
	renewErr  error
	expiresAt time.Time
}

type blockingDispatcherLeaseControl struct {
	dispatcherLeaseControlFake
	renewStarted chan struct{}
}

func (control *blockingDispatcherLeaseControl) RenewDispatcherLease(
	ctx context.Context,
	_ rundomain.DispatcherLease,
	_ time.Duration,
) (rundomain.DispatcherLease, error) {
	select {
	case control.renewStarted <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return rundomain.DispatcherLease{}, ctx.Err()
}

func (control *dispatcherLeaseControlFake) AcquireDispatcherLease(
	_ context.Context,
	request rundomain.DispatcherLeaseRequest,
) (rundomain.DispatcherLease, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	control.acquired++
	expiresAt := control.expiresAt
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(request.TTL)
	}
	return rundomain.DispatcherLease{InstanceID: request.InstanceID, FencingToken: 1, ExpiresAt: expiresAt}, nil
}

func (control *dispatcherLeaseControlFake) RenewDispatcherLease(
	_ context.Context,
	lease rundomain.DispatcherLease,
	ttl time.Duration,
) (rundomain.DispatcherLease, error) {
	control.mu.Lock()
	defer control.mu.Unlock()
	control.renewed++
	if control.renewErr != nil {
		return rundomain.DispatcherLease{}, control.renewErr
	}
	lease.ExpiresAt = time.Now().Add(ttl)
	return lease, nil
}

func (control *dispatcherLeaseControlFake) ReleaseDispatcherLease(
	context.Context,
	rundomain.DispatcherLease,
) error {
	control.mu.Lock()
	defer control.mu.Unlock()
	control.released++
	return nil
}

func TestDispatcherLeaseRunnerHoldsLeaseForWorkLifecycle(t *testing.T) {
	control := &dispatcherLeaseControlFake{}
	runner, err := NewDispatcherLeaseRunner(control, DispatcherLeaseRunnerConfig{
		InstanceID: "dispatcher-a", LeaseTTL: 5 * time.Second,
		RenewInterval: 10 * time.Millisecond, CallTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(t.Context(), func(_ context.Context, lease rundomain.DispatcherLease) error {
		if lease.InstanceID != "dispatcher-a" || lease.FencingToken != 1 {
			t.Fatalf("lease passed to work: %+v", lease)
		}
		deadline := time.After(time.Second)
		for {
			control.mu.Lock()
			renewed := control.renewed
			control.mu.Unlock()
			if renewed > 0 {
				return nil
			}
			select {
			case <-deadline:
				t.Fatal("Dispatcher Lease was not renewed")
			case <-time.After(time.Millisecond):
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.acquired != 1 || control.renewed == 0 || control.released != 1 {
		t.Fatalf("lease lifecycle: acquired=%d renewed=%d released=%d", control.acquired, control.renewed, control.released)
	}
}

func TestDispatcherLeaseRunnerStopsRenewalAndLetsMismatchLeaseExpire(t *testing.T) {
	control := &dispatcherLeaseControlFake{expiresAt: time.Now().Add(80 * time.Millisecond)}
	runner, err := NewDispatcherLeaseRunner(control, DispatcherLeaseRunnerConfig{
		InstanceID: "dispatcher-mismatch", LeaseTTL: 5 * time.Second,
		RenewInterval: time.Second, CallTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	fatal := &rundomain.DomainError{
		Code:   rundomain.ErrorExecutionContractMismatch,
		Detail: "Execution Binding evidence does not match",
	}
	workCancelled := make(chan time.Time, 1)
	startedAt := time.Now()
	err = runner.RunFailClosed(t.Context(), func(
		ctx context.Context,
		_ rundomain.DispatcherLease,
		failClosed func(error),
	) error {
		go func() {
			<-ctx.Done()
			workCancelled <- time.Now()
		}()
		failClosed(fatal)
		<-ctx.Done()
		return fatal
	})
	if !errors.Is(err, fatal) {
		t.Fatalf("Dispatcher fatal error: %v", err)
	}
	cancelledAt := <-workCancelled
	if cancelledAt.Sub(startedAt) < 50*time.Millisecond {
		t.Fatalf("fatal work was cancelled before Dispatcher Lease expiry: %s", cancelledAt.Sub(startedAt))
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if control.acquired != 1 || control.renewed != 0 || control.released != 0 {
		t.Fatalf("fatal Lease lifecycle: acquired=%d renewed=%d released=%d",
			control.acquired, control.renewed, control.released)
	}
}

func TestDispatcherLeaseRunnerCancelsWorkWhenRenewalFails(t *testing.T) {
	fenced := &rundomain.DomainError{Code: rundomain.ErrorFenced, Detail: "taken over"}
	control := &dispatcherLeaseControlFake{renewErr: fenced}
	runner, err := NewDispatcherLeaseRunner(control, DispatcherLeaseRunnerConfig{
		InstanceID: "dispatcher-a", LeaseTTL: 5 * time.Second,
		RenewInterval: 10 * time.Millisecond, CallTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(t.Context(), func(ctx context.Context, _ rundomain.DispatcherLease) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, fenced) {
		t.Fatalf("renewal loss error: %v", err)
	}
}

func TestDispatcherLeaseRunnerDoesNotReleaseWhileWorkBlocksAfterRenewalLoss(t *testing.T) {
	fenced := &rundomain.DomainError{Code: rundomain.ErrorFenced, Detail: "taken over"}
	control := &dispatcherLeaseControlFake{renewErr: fenced}
	runner, err := NewDispatcherLeaseRunner(control, DispatcherLeaseRunnerConfig{
		InstanceID: "dispatcher-a", LeaseTTL: 5 * time.Second,
		RenewInterval: 5 * time.Millisecond, CallTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	unblock := make(chan struct{})
	defer close(unblock)
	err = runner.Run(t.Context(), func(context.Context, rundomain.DispatcherLease) error {
		<-unblock
		return nil
	})
	if !errors.Is(err, fenced) {
		t.Fatalf("renewal loss error: %v", err)
	}
	control.mu.Lock()
	released := control.released
	control.mu.Unlock()
	if released != 0 {
		t.Fatalf("blocked work released Dispatcher Lease %d time(s)", released)
	}
}

func TestDispatcherLeaseRunnerDoesNotReleaseWhileWorkBlocksAfterContextCancellation(t *testing.T) {
	control := &dispatcherLeaseControlFake{}
	runner, err := NewDispatcherLeaseRunner(control, DispatcherLeaseRunnerConfig{
		InstanceID: "dispatcher-a", LeaseTTL: 5 * time.Second,
		RenewInterval: time.Second, CallTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	runContext, cancelRun := context.WithCancel(t.Context())
	workStarted := make(chan struct{})
	unblock := make(chan struct{})
	defer close(unblock)
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(runContext, func(context.Context, rundomain.DispatcherLease) error {
			close(workStarted)
			<-unblock
			return nil
		})
	}()
	<-workStarted
	cancelRun()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error: %v", err)
	}
	control.mu.Lock()
	released := control.released
	control.mu.Unlock()
	if released != 0 {
		t.Fatalf("blocked work released Dispatcher Lease %d time(s)", released)
	}
}

func TestDispatcherLeaseRunnerDoesNotReportCancelledInFlightRenewalOnNormalExit(t *testing.T) {
	control := &blockingDispatcherLeaseControl{renewStarted: make(chan struct{}, 1)}
	runner, err := NewDispatcherLeaseRunner(control, DispatcherLeaseRunnerConfig{
		InstanceID: "dispatcher-a", LeaseTTL: 5 * time.Second,
		RenewInterval: 5 * time.Millisecond, CallTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = runner.Run(t.Context(), func(context.Context, rundomain.DispatcherLease) error {
		select {
		case <-control.renewStarted:
			return nil
		case <-time.After(time.Second):
			t.Fatal("renewal did not start")
			return nil
		}
	})
	if err != nil {
		t.Fatalf("normal exit reported renewal cancellation: %v", err)
	}
}
