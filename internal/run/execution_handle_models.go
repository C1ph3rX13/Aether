package run

import (
	"context"
	"time"
)

type ExecutionHandleRegistration interface {
	ContainerReady(context.Context, ProjectContainerHandle) error
	Started(context.Context, ExecutionHandle) error
	Stopped(context.Context, ExecutionHandle) error
}

type ProjectContainerHandle struct {
	RunID       RunID     `json:"run_id"`
	ProjectID   string    `json:"project_id"`
	ContainerID string    `json:"container_id"`
	RecordedAt  time.Time `json:"recorded_at"`
}

type ExecutionHandle struct {
	RunID       RunID          `json:"run_id"`
	ProjectID   string         `json:"project_id"`
	Phase       ExecutionPhase `json:"phase"`
	ContainerID string         `json:"container_id"`
	ExecID      string         `json:"exec_id"`
	StartedAt   time.Time      `json:"started_at"`
}

type ProcessHandle interface {
	Handle() ExecutionHandle
}

type ExecutionHandleRecorder interface {
	RecordExecutionHandle(context.Context, ExecutionHandleRecordRequest) error
}

type ProjectContainerRecorder interface {
	RecordProjectContainer(context.Context, ProjectContainerRecordRequest) error
}

type ExecutionHandleCompletionRecorder interface {
	MarkExecutionHandleStopped(context.Context, ExecutionHandleStopRequest) error
}

type ExecutionHandleStore interface {
	ProjectContainerRecorder
	ExecutionHandleRecorder
	ExecutionHandleCompletionRecorder
}

type RunContinuationAuthorizer interface {
	AuthorizeRunContinuation(context.Context, RunContinuationRequest) error
}

type ProcessRecoveryAuthorizer interface {
	BeginProcessRecovery(context.Context, BeginProcessRecoveryRequest) (ProcessRecoveryReservation, error)
}

type ExecutionControlStore interface {
	ExecutionHandleStore
	RunContinuationAuthorizer
	ProcessRecoveryAuthorizer
}

type RunContinuationRequest struct {
	RunID                  RunID        `json:"run_id"`
	ProjectID              string       `json:"project_id"`
	FencingToken           FencingToken `json:"fencing_token"`
	DispatcherID           string       `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken `json:"dispatcher_fencing_token"`
}

func (request RunContinuationRequest) Validate() error {
	if !bounded(string(request.RunID), 256) || !bounded(request.ProjectID, 256) || request.FencingToken <= 0 ||
		!bounded(request.DispatcherID, 256) || request.DispatcherFencingToken <= 0 {
		return newError(ErrorInvalidArgument, "Run continuation request is invalid")
	}
	return nil
}

type ExecutionHandleRecordRequest struct {
	Handle                 ExecutionHandle `json:"handle"`
	RunFencingToken        FencingToken    `json:"run_fencing_token"`
	DispatcherID           string          `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken    `json:"dispatcher_fencing_token"`
}

type ProjectContainerRecordRequest struct {
	Handle                 ProjectContainerHandle `json:"handle"`
	RunFencingToken        FencingToken           `json:"run_fencing_token"`
	DispatcherID           string                 `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken           `json:"dispatcher_fencing_token"`
}

func (request ProjectContainerRecordRequest) Validate() error {
	if !bounded(string(request.Handle.RunID), 256) || !bounded(request.Handle.ProjectID, 256) ||
		!bounded(request.Handle.ContainerID, 512) || request.RunFencingToken <= 0 ||
		!bounded(request.DispatcherID, 256) || request.DispatcherFencingToken <= 0 {
		return newError(ErrorInvalidArgument, "Project container record is invalid")
	}
	return nil
}

type ExecutionHandleListRequest struct {
	RunIDs                 []RunID      `json:"run_ids"`
	DispatcherID           string       `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken `json:"dispatcher_fencing_token"`
}

func (request ExecutionHandleListRequest) Validate() error {
	if len(request.RunIDs) == 0 || len(request.RunIDs) > 1024 ||
		request.DispatcherID == "" || request.DispatcherFencingToken <= 0 {
		return newError(ErrorInvalidArgument, "execution handle list request is invalid")
	}
	for _, runID := range request.RunIDs {
		if !bounded(string(runID), 256) {
			return newError(ErrorInvalidArgument, "execution handle list request is invalid")
		}
	}
	return nil
}

type ExecutionHandleStopRequest struct {
	Handle                 ExecutionHandle `json:"handle"`
	DispatcherID           string          `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken    `json:"dispatcher_fencing_token"`
}

func (request ExecutionHandleStopRequest) Validate() error {
	if !bounded(string(request.Handle.RunID), 256) || !bounded(request.Handle.ProjectID, 256) ||
		!bounded(request.Handle.ContainerID, 512) || !bounded(request.Handle.ExecID, 512) ||
		!request.Handle.Phase.valid() || !bounded(request.DispatcherID, 256) || request.DispatcherFencingToken <= 0 {
		return newError(ErrorInvalidArgument, "execution handle stop request is invalid")
	}
	return nil
}

func (request ExecutionHandleRecordRequest) Validate() error {
	if request.Handle.RunID == "" || request.Handle.ProjectID == "" ||
		request.Handle.ContainerID == "" || request.Handle.ExecID == "" ||
		!request.Handle.Phase.validFor(request.HandleTask()) || request.RunFencingToken <= 0 ||
		request.DispatcherID == "" || request.DispatcherFencingToken <= 0 {
		return newError(ErrorInvalidArgument, "execution handle record is invalid")
	}
	return nil
}

// HandleTask derives the task from the phase without exposing a second task
// field in the persisted Docker handle contract.
func (request ExecutionHandleRecordRequest) HandleTask() TaskType {
	switch request.Handle.Phase {
	case PhaseReason, PhaseReasonFinalization:
		return TaskReason
	case PhaseExplore, PhaseExploreConclude, PhaseExploreRecovery, PhaseExploreConcludeRecovery:
		return TaskExplore
	case PhaseBootstrap, PhaseBootstrapConclude, PhaseBootstrapRecovery, PhaseBootstrapConcludeRecovery:
		return TaskBootstrap
	default:
		return ""
	}
}
