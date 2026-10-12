package controlplane

import (
	"context"
	"fmt"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
)

// InProcessRunControl adapts the Control Plane module to Scheduler's
// consumer-owned contract without coupling Scheduler to an HTTP adapter.
type InProcessRunControl struct{ service ControlPlaneCapability }

func NewInProcessRunControl(service ControlPlaneCapability) (*InProcessRunControl, error) {
	if service == nil {
		return nil, fmt.Errorf("in-process Control Plane service is required")
	}
	return &InProcessRunControl{service: service}, nil
}

func (control *InProcessRunControl) SchedulingSnapshot(ctx context.Context, request scheduler.SchedulingSnapshotRequest) (scheduler.ControlSchedulingSnapshot, error) {
	return control.service.SchedulerSnapshot(ctx, request)
}

func (control *InProcessRunControl) AcquireDispatcherLease(ctx context.Context, request rundomain.DispatcherLeaseRequest) (rundomain.DispatcherLease, error) {
	return control.service.AcquireDispatcherLease(ctx, request)
}

func (control *InProcessRunControl) RenewDispatcherLease(ctx context.Context, lease rundomain.DispatcherLease, ttl time.Duration) (rundomain.DispatcherLease, error) {
	return control.service.RenewDispatcherLease(ctx, lease, ttl)
}

func (control *InProcessRunControl) ReleaseDispatcherLease(ctx context.Context, lease rundomain.DispatcherLease) error {
	return control.service.ReleaseDispatcherLease(ctx, lease)
}

func (control *InProcessRunControl) Close() {}

func (control *InProcessRunControl) ProjectRuntimeStatuses(ctx context.Context, request rundomain.ProjectRuntimeStatusRequest) ([]rundomain.ProjectRuntimeStatusResult, error) {
	return control.service.ProjectRuntimeStatuses(ctx, request)
}

func (control *InProcessRunControl) StartRun(ctx context.Context, request rundomain.StartRequest) (*rundomain.AdmittedRun, error) {
	admitted, err := control.service.StartRun(ctx, request)
	if err == nil && admitted != nil {
		err = admitted.Validate(request)
	}
	return admitted, err
}
func (control *InProcessRunControl) BeginProcessRecovery(ctx context.Context, request rundomain.BeginProcessRecoveryRequest) (rundomain.ProcessRecoveryReservation, error) {
	return control.service.BeginProcessRecovery(ctx, request)
}
func (control *InProcessRunControl) RenewRun(ctx context.Context, request rundomain.RenewRunRequest) (time.Time, error) {
	return control.service.RenewRun(ctx, request)
}
func (control *InProcessRunControl) FinishRun(ctx context.Context, request rundomain.FinishRequest) (*rundomain.FinishReceipt, error) {
	return control.service.FinishRun(ctx, request)
}
func (control *InProcessRunControl) RecoverRuns(ctx context.Context, options rundomain.RecoveryOptions) ([]rundomain.RunID, error) {
	return control.service.RecoverRuns(ctx, options)
}
func (control *InProcessRunControl) RecordRecoveryEvent(ctx context.Context, record rundomain.RecoveryRecord) error {
	return control.service.RecordRecoveryEvent(ctx, record)
}
func (control *InProcessRunControl) RecordExecutionHandle(ctx context.Context, request rundomain.ExecutionHandleRecordRequest) error {
	return control.service.RecordExecutionHandle(ctx, request)
}
func (control *InProcessRunControl) RecordProjectContainer(ctx context.Context, request rundomain.ProjectContainerRecordRequest) error {
	return control.service.RecordProjectContainer(ctx, request)
}
func (control *InProcessRunControl) AuthorizeRunContinuation(ctx context.Context, request rundomain.RunContinuationRequest) error {
	return control.service.AuthorizeRunContinuation(ctx, request)
}
func (control *InProcessRunControl) ListExecutionHandles(ctx context.Context, request rundomain.ExecutionHandleListRequest) ([]rundomain.ExecutionHandle, error) {
	return control.service.ListExecutionHandles(ctx, request)
}
func (control *InProcessRunControl) MarkExecutionHandleStopped(ctx context.Context, request rundomain.ExecutionHandleStopRequest) error {
	return control.service.MarkExecutionHandleStopped(ctx, request)
}

func (control *InProcessRunControl) LeaseProjectCleanupJobs(ctx context.Context, request rundomain.CleanupLeaseRequest) ([]rundomain.ProjectCleanupJob, error) {
	return control.service.LeaseProjectCleanupJobs(ctx, request)
}

func (control *InProcessRunControl) CompleteProjectCleanupJob(ctx context.Context, request rundomain.CleanupCompletionRequest) error {
	return control.service.CompleteProjectCleanupJob(ctx, request)
}

var _ scheduler.SchedulingControl = (*InProcessRunControl)(nil)
var _ scheduler.ProjectInventoryControl = (*InProcessRunControl)(nil)
var _ scheduler.RunRecoveryControl = (*InProcessRunControl)(nil)
var _ scheduler.RunLifecycleControl = (*InProcessRunControl)(nil)
var _ scheduler.ProjectCleanupControl = (*InProcessRunControl)(nil)
