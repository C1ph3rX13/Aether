package controlplane

import (
	"context"
	"fmt"
	"time"

	"github.com/C1ph3rX13/Aether/internal/api"
	"github.com/C1ph3rX13/Aether/internal/database"
	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
)

// ControlPlaneCapability is the scheduler-facing Control Plane module contract.
// It contains persistence-backed coordination only; Docker, Worker Runtime and
// scheduling policy remain owned by the Dispatcher command composition.
type ControlPlaneCapability interface {
	SchedulerSnapshot(context.Context, rundomain.SchedulingSnapshotRequest) (scheduler.ControlSchedulingSnapshot, error)
	ProjectRuntimeStatuses(context.Context, rundomain.ProjectRuntimeStatusRequest) ([]rundomain.ProjectRuntimeStatusResult, error)
	AcquireDispatcherLease(context.Context, rundomain.DispatcherLeaseRequest) (rundomain.DispatcherLease, error)
	RenewDispatcherLease(context.Context, rundomain.DispatcherLease, time.Duration) (rundomain.DispatcherLease, error)
	ReleaseDispatcherLease(context.Context, rundomain.DispatcherLease) error
	StartRun(context.Context, rundomain.StartRequest) (*rundomain.AdmittedRun, error)
	BeginProcessRecovery(context.Context, rundomain.BeginProcessRecoveryRequest) (rundomain.ProcessRecoveryReservation, error)
	RenewRun(context.Context, rundomain.RenewRunRequest) (time.Time, error)
	FinishRun(context.Context, rundomain.FinishRequest) (*rundomain.FinishReceipt, error)
	RecoverRuns(context.Context, rundomain.RecoveryOptions) ([]rundomain.RunID, error)
	RecordRecoveryEvent(context.Context, rundomain.RecoveryRecord) error
	RecordExecutionHandle(context.Context, rundomain.ExecutionHandleRecordRequest) error
	RecordProjectContainer(context.Context, rundomain.ProjectContainerRecordRequest) error
	AuthorizeRunContinuation(context.Context, rundomain.RunContinuationRequest) error
	ListExecutionHandles(context.Context, rundomain.ExecutionHandleListRequest) ([]rundomain.ExecutionHandle, error)
	MarkExecutionHandleStopped(context.Context, rundomain.ExecutionHandleStopRequest) error
	LeaseProjectCleanupJobs(context.Context, rundomain.CleanupLeaseRequest) ([]rundomain.ProjectCleanupJob, error)
	CompleteProjectCleanupJob(context.Context, rundomain.CleanupCompletionRequest) error
}

type DispatcherPolicyProvider func(
	context.Context,
	database.DBTX,
) (scheduler.ControlDispatcherPolicy, error)

// controlPlane coordinates one revision-consistent Project projection with
// the persisted Run ledger. Docker is deliberately absent from this boundary.
type ControlPlane struct {
	db         *database.DB
	projects   *project.Service
	runs       *rundomain.Service
	scheduling *projectSchedulingProjection
}

func newControlPlane(
	db *database.DB,
	projects *project.Service,
	runs *rundomain.Service,
	dispatcherPolicy DispatcherPolicyProvider,
) (*ControlPlane, error) {
	if db == nil || projects == nil || runs == nil || dispatcherPolicy == nil {
		return nil, fmt.Errorf("control plane services are required")
	}
	return &ControlPlane{
		db: db, projects: projects, runs: runs,
		scheduling: newProjectSchedulingProjection(db, projects, runs, dispatcherPolicy),
	}, nil
}

// SchedulerSnapshot returns the consumer-owned projection used by both
// in-process and separated Dispatchers.
func (control *ControlPlane) SchedulerSnapshot(
	ctx context.Context,
	request rundomain.SchedulingSnapshotRequest,
) (scheduler.ControlSchedulingSnapshot, error) {
	return control.scheduling.Snapshot(ctx, request)
}

func (control *ControlPlane) AcquireDispatcherLease(ctx context.Context, request rundomain.DispatcherLeaseRequest) (rundomain.DispatcherLease, error) {
	return control.runs.AcquireDispatcherLease(ctx, request)
}

func (control *ControlPlane) RenewDispatcherLease(ctx context.Context, lease rundomain.DispatcherLease, ttl time.Duration) (rundomain.DispatcherLease, error) {
	return control.runs.RenewDispatcherLease(ctx, lease, ttl)
}

func (control *ControlPlane) ReleaseDispatcherLease(ctx context.Context, lease rundomain.DispatcherLease) error {
	return control.runs.ReleaseDispatcherLease(ctx, lease)
}

func (control *ControlPlane) StartRun(ctx context.Context, request rundomain.StartRequest) (*rundomain.AdmittedRun, error) {
	return control.runs.StartRun(ctx, request)
}

func (control *ControlPlane) BeginProcessRecovery(ctx context.Context, request rundomain.BeginProcessRecoveryRequest) (rundomain.ProcessRecoveryReservation, error) {
	return control.runs.BeginProcessRecovery(ctx, request)
}

func (control *ControlPlane) RenewRun(ctx context.Context, request rundomain.RenewRunRequest) (time.Time, error) {
	return control.runs.RenewRun(ctx, request)
}

func (control *ControlPlane) FinishRun(ctx context.Context, request rundomain.FinishRequest) (*rundomain.FinishReceipt, error) {
	return control.runs.FinishRun(ctx, request)
}

func (control *ControlPlane) RecoverRuns(ctx context.Context, options rundomain.RecoveryOptions) ([]rundomain.RunID, error) {
	return control.runs.RecoverRuns(ctx, options)
}

func (control *ControlPlane) RecordRecoveryEvent(ctx context.Context, record rundomain.RecoveryRecord) error {
	return control.runs.RecordRecoveryEvent(ctx, record)
}

func (control *ControlPlane) RecordExecutionHandle(ctx context.Context, request rundomain.ExecutionHandleRecordRequest) error {
	return control.runs.RecordExecutionHandle(ctx, request)
}

func (control *ControlPlane) RecordProjectContainer(ctx context.Context, request rundomain.ProjectContainerRecordRequest) error {
	return control.runs.RecordProjectContainer(ctx, request)
}

func (control *ControlPlane) AuthorizeRunContinuation(ctx context.Context, request rundomain.RunContinuationRequest) error {
	return control.runs.AuthorizeRunContinuation(ctx, request)
}

func (control *ControlPlane) ListExecutionHandles(ctx context.Context, request rundomain.ExecutionHandleListRequest) ([]rundomain.ExecutionHandle, error) {
	return control.runs.ListExecutionHandles(ctx, request)
}

func (control *ControlPlane) MarkExecutionHandleStopped(ctx context.Context, request rundomain.ExecutionHandleStopRequest) error {
	return control.runs.MarkExecutionHandleStopped(ctx, request)
}

func (control *ControlPlane) LeaseProjectCleanupJobs(ctx context.Context, request rundomain.CleanupLeaseRequest) ([]rundomain.ProjectCleanupJob, error) {
	return control.runs.LeaseProjectCleanupJobs(ctx, request)
}

func (control *ControlPlane) CompleteProjectCleanupJob(ctx context.Context, request rundomain.CleanupCompletionRequest) error {
	return control.runs.CompleteProjectCleanupJob(ctx, request)
}

var _ ControlPlaneCapability = (*ControlPlane)(nil)
var _ api.RunControl = (*ControlPlane)(nil)
