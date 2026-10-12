package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type DispatcherLeaseControl interface {
	AcquireDispatcherLease(context.Context, rundomain.DispatcherLeaseRequest) (rundomain.DispatcherLease, error)
	RenewDispatcherLease(context.Context, rundomain.DispatcherLease, time.Duration) (rundomain.DispatcherLease, error)
	ReleaseDispatcherLease(context.Context, rundomain.DispatcherLease) error
}

type DispatcherLeaseRunnerConfig struct {
	InstanceID    string
	LeaseTTL      time.Duration
	RenewInterval time.Duration
	CallTimeout   time.Duration
}

type DispatcherLeaseRunner struct {
	control DispatcherLeaseControl
	config  DispatcherLeaseRunnerConfig
}

type dispatcherLeaseRenewalResult struct {
	lease rundomain.DispatcherLease
	err   error
}

func NewDispatcherLeaseRunner(
	control DispatcherLeaseControl,
	config DispatcherLeaseRunnerConfig,
) (*DispatcherLeaseRunner, error) {
	if control == nil {
		return nil, fmt.Errorf("Dispatcher Lease control is required")
	}
	request := rundomain.DispatcherLeaseRequest{InstanceID: config.InstanceID, TTL: config.LeaseTTL}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if config.RenewInterval == 0 {
		config.RenewInterval = config.LeaseTTL / 3
	}
	if config.CallTimeout == 0 {
		config.CallTimeout = config.RenewInterval / 2
	}
	if config.RenewInterval <= 0 || config.RenewInterval >= config.LeaseTTL ||
		config.CallTimeout <= 0 || config.CallTimeout >= config.LeaseTTL {
		return nil, fmt.Errorf("Dispatcher Lease renewal policy is invalid")
	}
	return &DispatcherLeaseRunner{control: control, config: config}, nil
}

// Run holds the global Dispatcher Lease for the complete scheduling lifecycle.
// Any renewal failure cancels work before another instance can take ownership.
func (runner *DispatcherLeaseRunner) Run(
	ctx context.Context,
	work func(context.Context, rundomain.DispatcherLease) error,
) (err error) {
	if work == nil {
		return fmt.Errorf("Dispatcher Lease work is required")
	}
	return runner.RunFailClosed(ctx, func(
		workContext context.Context,
		lease rundomain.DispatcherLease,
		_ func(error),
	) error {
		return work(workContext, lease)
	})
}

// RunFailClosed lets scheduling work stop Lease renewal before it quiesces.
// The Lease is left to expire so active Runs converge through normal fencing.
func (runner *DispatcherLeaseRunner) RunFailClosed(
	ctx context.Context,
	work func(context.Context, rundomain.DispatcherLease, func(error)) error,
) (err error) {
	if work == nil {
		return fmt.Errorf("Dispatcher Lease work is required")
	}
	acquireContext, acquireCancel := context.WithTimeout(ctx, runner.config.CallTimeout)
	lease, err := runner.control.AcquireDispatcherLease(acquireContext, rundomain.DispatcherLeaseRequest{
		InstanceID: runner.config.InstanceID,
		TTL:        runner.config.LeaseTTL,
	})
	acquireCancel()
	if err != nil {
		return fmt.Errorf("acquire global Dispatcher Lease: %w", err)
	}

	workContext, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	workExited := false
	releaseLease := true
	defer func() {
		// TTL remains the fencing boundary while uncooperative work may still run.
		if !workExited || !releaseLease {
			return
		}
		releaseContext, releaseCancel := context.WithTimeout(context.Background(), runner.config.CallTimeout)
		defer releaseCancel()
		releaseErr := runner.control.ReleaseDispatcherLease(releaseContext, lease)
		var domainErr *rundomain.DomainError
		if errors.As(releaseErr, &domainErr) && domainErr.Code == rundomain.ErrorFenced {
			releaseErr = nil
		}
		err = errors.Join(err, releaseErr)
	}()

	fatalSignal := make(chan error, 1)
	failClosed := func(fatal error) {
		if fatal == nil {
			return
		}
		select {
		case fatalSignal <- fatal:
		default:
		}
	}
	workDone := make(chan error, 1)
	go func(acquired rundomain.DispatcherLease) {
		workDone <- work(workContext, acquired, failClosed)
	}(lease)
	renewalContext, stopRenewal := context.WithCancel(workContext)
	defer stopRenewal()
	renewalDone := make(chan dispatcherLeaseRenewalResult, 1)
	go func(acquired rundomain.DispatcherLease) {
		ticker := time.NewTicker(runner.config.RenewInterval)
		defer ticker.Stop()
		current := acquired
		for {
			select {
			case <-renewalContext.Done():
				renewalDone <- dispatcherLeaseRenewalResult{lease: current}
				return
			case <-ticker.C:
				callContext, callCancel := context.WithTimeout(renewalContext, runner.config.CallTimeout)
				renewed, renewErr := runner.control.RenewDispatcherLease(callContext, current, runner.config.LeaseTTL)
				callCancel()
				if renewErr != nil {
					if renewalContext.Err() != nil {
						renewalDone <- dispatcherLeaseRenewalResult{lease: current}
						return
					}
					renewalDone <- dispatcherLeaseRenewalResult{
						lease: current, err: fmt.Errorf("renew global Dispatcher Lease: %w", renewErr),
					}
					return
				}
				current = renewed
			}
		}
	}(lease)
	finishFailClosed := func(fatal, workErr error, workAlreadyExited bool) error {
		releaseLease = false
		stopRenewal()
		renewal := <-renewalDone
		lease = renewal.lease
		expiryErr := waitForDispatcherLeaseExpiry(ctx, lease.ExpiresAt, runner.config.LeaseTTL)
		cancelWork()
		if !workAlreadyExited {
			workErr = <-workDone
		}
		workExited = true
		return errors.Join(fatal, renewal.err, expiryErr, workErr)
	}

	select {
	case fatal := <-fatalSignal:
		return finishFailClosed(fatal, nil, false)
	case err = <-workDone:
		workExited = true
		if isExecutionContractMismatch(err) {
			return finishFailClosed(err, nil, true)
		}
		cancelWork()
		renewal := <-renewalDone
		lease = renewal.lease
		return errors.Join(err, renewal.err)
	case renewal := <-renewalDone:
		lease = renewal.lease
		cancelWork()
		select {
		case workErr := <-workDone:
			workExited = true
			if renewal.err == nil {
				if workErr != nil && !errors.Is(workErr, context.Canceled) {
					return errors.Join(ctx.Err(), workErr)
				}
				return ctx.Err()
			}
			return errors.Join(renewal.err, workErr)
		case <-time.After(runner.config.CallTimeout):
			if renewal.err == nil {
				return ctx.Err()
			}
			return renewal.err
		}
	case <-ctx.Done():
		cancelWork()
		select {
		case workErr := <-workDone:
			workExited = true
			if workErr != nil && !errors.Is(workErr, context.Canceled) {
				return errors.Join(ctx.Err(), workErr)
			}
		case <-time.After(runner.config.CallTimeout):
		}
		return ctx.Err()
	}
}

func waitForDispatcherLeaseExpiry(ctx context.Context, expiresAt time.Time, fallback time.Duration) error {
	wait := time.Until(expiresAt)
	if expiresAt.IsZero() {
		wait = fallback
	}
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
