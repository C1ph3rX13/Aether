package scheduler

import (
	"context"
	"errors"
	"fmt"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

type RunRecoveryControl interface {
	RecoverRuns(context.Context, rundomain.RecoveryOptions) ([]rundomain.RunID, error)
	RecordRecoveryEvent(context.Context, rundomain.RecoveryRecord) error
	ListExecutionHandles(context.Context, rundomain.ExecutionHandleListRequest) ([]rundomain.ExecutionHandle, error)
	MarkExecutionHandleStopped(context.Context, rundomain.ExecutionHandleStopRequest) error
}

type RunRecovery struct {
	control         RunRecoveryControl
	runtime         RunResources
	dispatcherLease rundomain.DispatcherLease
	events          chan<- CoordinatorEvent
}

func NewRunRecovery(
	control RunRecoveryControl,
	runtime RunResources,
	dispatcherLease rundomain.DispatcherLease,
	events chan<- CoordinatorEvent,
) (*RunRecovery, error) {
	if control == nil || runtime == nil || events == nil ||
		dispatcherLease.InstanceID == "" || dispatcherLease.FencingToken <= 0 {
		return nil, fmt.Errorf("Run recovery configuration is invalid")
	}
	return &RunRecovery{control: control, runtime: runtime, dispatcherLease: dispatcherLease, events: events}, nil
}

// Recover fences persisted Runs before stopping their recorded Docker Execs.
// Shared Project containers are never discovered, adopted, or removed here.
func (recovery *RunRecovery) Recover(ctx context.Context, reason string) ([]rundomain.RunID, error) {
	recovered, err := recovery.control.RecoverRuns(ctx, rundomain.RecoveryOptions{
		DispatcherID:           recovery.dispatcherLease.InstanceID,
		DispatcherFencingToken: recovery.dispatcherLease.FencingToken,
		Reason:                 reason,
	})
	if err != nil {
		return nil, err
	}
	if len(recovered) == 0 {
		return recovered, nil
	}
	handles, err := recovery.control.ListExecutionHandles(ctx, rundomain.ExecutionHandleListRequest{
		RunIDs: recovered, DispatcherID: recovery.dispatcherLease.InstanceID,
		DispatcherFencingToken: recovery.dispatcherLease.FencingToken,
	})
	if err != nil {
		return recovered, err
	}

	var recoveryErrors []error
	statusByRun := make(map[rundomain.RunID]rundomain.RecoveryStatus, len(recovered))
	for _, handle := range handles {
		if err := recovery.runtime.StopExecutionHandle(ctx, handle); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("stop recovered Exec %s: %w", handle.ExecID, err))
			statusByRun[handle.RunID] = rundomain.RecoveryDisposeFailed
			continue
		}
		if statusByRun[handle.RunID] == "" {
			statusByRun[handle.RunID] = rundomain.RecoveryDisposed
		}
		if err := recovery.control.MarkExecutionHandleStopped(ctx, rundomain.ExecutionHandleStopRequest{
			Handle: handle, DispatcherID: recovery.dispatcherLease.InstanceID,
			DispatcherFencingToken: recovery.dispatcherLease.FencingToken,
		}); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("mark recovered Exec %s stopped: %w", handle.ExecID, err))
			statusByRun[handle.RunID] = rundomain.RecoveryDisposeFailed
		}
	}
	for _, runID := range recovered {
		status := statusByRun[runID]
		if status == "" {
			status = rundomain.RecoveryDisposed
		}
		record := rundomain.RecoveryRecord{
			RunID: runID, DispatcherID: recovery.dispatcherLease.InstanceID,
			DispatcherFencingToken: recovery.dispatcherLease.FencingToken,
			Status:                 status, Reason: reason,
		}
		if err := recovery.control.RecordRecoveryEvent(ctx, record); err != nil {
			recoveryErrors = append(recoveryErrors, err)
		}
		if err := recovery.emit(ctx, RecoveryEvent{Run: RunningRunSnapshot{RunID: runID}, Status: status}); err != nil {
			recoveryErrors = append(recoveryErrors, err)
		}
	}
	return recovered, errors.Join(recoveryErrors...)
}

func (recovery *RunRecovery) emit(ctx context.Context, event CoordinatorEvent) error {
	select {
	case recovery.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
