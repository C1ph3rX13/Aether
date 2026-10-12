package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type RunLeaseControl interface {
	RenewRun(context.Context, rundomain.RenewRunRequest) (time.Time, error)
}

type SupervisedRun interface {
	Wait(context.Context) (rundomain.Completion, error)
	Stop(context.Context, time.Duration) error
}

type RunSupervisorConfig struct {
	RunID          rundomain.RunID
	FencingToken   rundomain.FencingToken
	DispatcherID   string
	LeaseTTL       time.Duration
	LeaseExpiresAt time.Time
	RenewInterval  time.Duration
	Deadline       time.Time
	StopGrace      time.Duration
	EventTimeout   time.Duration
}

type RunSupervisor struct {
	control RunLeaseControl
	run     SupervisedRun
	events  chan<- CoordinatorEvent
	config  RunSupervisorConfig
}

func NewRunSupervisor(
	control RunLeaseControl,
	run SupervisedRun,
	events chan<- CoordinatorEvent,
	config RunSupervisorConfig,
) (*RunSupervisor, error) {
	if control == nil || run == nil || events == nil || config.RunID == "" ||
		config.FencingToken <= 0 || config.DispatcherID == "" || config.LeaseTTL <= 0 ||
		config.LeaseTTL > rundomain.MaximumLeaseExtension || config.StopGrace <= 0 ||
		config.StopGrace > rundomain.MaximumStopGrace {
		return nil, fmt.Errorf("RunSupervisor configuration is invalid")
	}
	if config.RenewInterval == 0 {
		config.RenewInterval = config.LeaseTTL / 3
	}
	if config.RenewInterval <= 0 || config.RenewInterval >= config.LeaseTTL {
		return nil, fmt.Errorf("RunSupervisor renewal interval must be shorter than its Lease TTL")
	}
	if config.EventTimeout == 0 {
		config.EventTimeout = time.Second
	}
	if config.EventTimeout < 0 || config.EventTimeout > 10*time.Second {
		return nil, fmt.Errorf("RunSupervisor event timeout is invalid")
	}
	return &RunSupervisor{control: control, run: run, events: events, config: config}, nil
}

func (supervisor *RunSupervisor) Run(ctx context.Context) error {
	runContext := ctx
	cancel := func() {}
	if !supervisor.config.Deadline.IsZero() {
		runContext, cancel = context.WithDeadline(ctx, supervisor.config.Deadline)
	} else {
		runContext, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	type waitResult struct {
		completion rundomain.Completion
		err        error
	}
	waited := make(chan waitResult, 1)
	go func() {
		completion, err := supervisor.run.Wait(runContext)
		waited <- waitResult{completion: completion, err: err}
	}()

	now := time.Now()
	leaseExpiresAt := supervisor.config.LeaseExpiresAt
	if leaseExpiresAt.IsZero() {
		leaseExpiresAt = now.Add(supervisor.config.LeaseTTL)
	}
	leaseTimer := time.NewTimer(time.Until(leaseExpiresAt))
	defer leaseTimer.Stop()
	renewalTicker := time.NewTicker(supervisor.config.RenewInterval)
	defer renewalTicker.Stop()
	var lastRenewalError error
	type renewalResult struct {
		expiresAt time.Time
		err       error
	}
	renewed := make(chan renewalResult, 1)
	renewalInFlight := false

	for {
		select {
		case result := <-waited:
			if runErr := runContext.Err(); runErr != nil {
				cancel()
				stopErr := supervisor.stop()
				if errors.Is(runErr, context.DeadlineExceeded) {
					return errors.Join(supervisor.emit(RunDeadlineEvent{RunID: supervisor.config.RunID}), stopErr)
				}
				return errors.Join(supervisor.emit(supervisor.cancellationEvent(runContext)), stopErr)
			}
			cancel()
			emitErr := supervisor.emit(RunExitedEvent{RunID: supervisor.config.RunID, Completion: result.completion, Err: result.err})
			if emitErr != nil {
				return errors.Join(emitErr, supervisor.stop())
			}
			return nil
		case <-runContext.Done():
			cancel()
			stopErr := supervisor.stop()
			if errors.Is(runContext.Err(), context.DeadlineExceeded) {
				return errors.Join(supervisor.emit(RunDeadlineEvent{RunID: supervisor.config.RunID}), stopErr)
			}
			return errors.Join(supervisor.emit(supervisor.cancellationEvent(runContext)), stopErr)
		case <-leaseTimer.C:
			cancel()
			if lastRenewalError == nil {
				lastRenewalError = fmt.Errorf("Run lease expired before renewal")
			}
			stopErr := supervisor.stop()
			return errors.Join(
				supervisor.emit(LeaseLostEvent{RunID: supervisor.config.RunID, Err: lastRenewalError}),
				lastRenewalError, stopErr,
			)
		case <-renewalTicker.C:
			if renewalInFlight {
				continue
			}
			remaining := time.Until(leaseExpiresAt)
			if remaining <= 0 {
				continue
			}
			callTimeout := supervisor.config.RenewInterval
			if callTimeout >= remaining {
				callTimeout = remaining / 2
			}
			if callTimeout <= 0 {
				continue
			}
			renewalInFlight = true
			go func() {
				callContext, callCancel := context.WithTimeout(runContext, callTimeout)
				defer callCancel()
				expiresAt, err := supervisor.control.RenewRun(callContext, rundomain.RenewRunRequest{
					RunID: supervisor.config.RunID, FencingToken: supervisor.config.FencingToken,
					DispatcherID: supervisor.config.DispatcherID, LeaseExtension: supervisor.config.LeaseTTL,
				})
				renewed <- renewalResult{expiresAt: expiresAt, err: err}
			}()
		case renewal := <-renewed:
			renewalInFlight = false
			if renewal.err != nil {
				lastRenewalError = renewal.err
				var domainErr *rundomain.DomainError
				if errors.As(renewal.err, &domainErr) && domainErr.Code == rundomain.ErrorFenced {
					cancel()
					stopErr := supervisor.stop()
					return errors.Join(supervisor.emit(LeaseLostEvent{RunID: supervisor.config.RunID, Err: renewal.err}), renewal.err, stopErr)
				}
				continue
			}
			lastRenewalError = nil
			leaseExpiresAt = renewal.expiresAt
			if !leaseTimer.Stop() {
				select {
				case <-leaseTimer.C:
				default:
				}
			}
			leaseTimer.Reset(time.Until(leaseExpiresAt))
			if err := supervisor.emit(LeaseRenewedEvent{RunID: supervisor.config.RunID, LeaseExpiresAt: leaseExpiresAt}); err != nil {
				cancel()
				return errors.Join(err, supervisor.stop())
			}
		}
	}
}

func (supervisor *RunSupervisor) cancellationEvent(ctx context.Context) CoordinatorEvent {
	if errors.Is(context.Cause(ctx), errRunRevokedCancellation) {
		return LeaseLostEvent{RunID: supervisor.config.RunID, Err: context.Cause(ctx)}
	}
	return RunInterruptedEvent{RunID: supervisor.config.RunID}
}

func (supervisor *RunSupervisor) emit(event CoordinatorEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), supervisor.config.EventTimeout)
	defer cancel()
	select {
	case supervisor.events <- event:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("emit RunSupervisor event: %w", ctx.Err())
	}
}

func (supervisor *RunSupervisor) stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), supervisor.config.StopGrace+time.Second)
	defer cancel()
	return supervisor.run.Stop(ctx, supervisor.config.StopGrace)
}
