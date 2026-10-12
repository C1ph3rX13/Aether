package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// SchedulingControl is the scheduler's read-only planning port.
type SchedulingControl interface {
	SchedulingSnapshot(context.Context, SchedulingSnapshotRequest) (ControlSchedulingSnapshot, error)
}

type ProjectCleanupControl interface {
	LeaseProjectCleanupJobs(context.Context, rundomain.CleanupLeaseRequest) ([]rundomain.ProjectCleanupJob, error)
	CompleteProjectCleanupJob(context.Context, rundomain.CleanupCompletionRequest) error
}

type ProjectCleanupRuntime interface {
	CleanupProject(context.Context, rundomain.ProjectCleanupJob) error
}

type ProjectInventoryControl interface {
	ProjectRuntimeStatuses(context.Context, rundomain.ProjectRuntimeStatusRequest) ([]rundomain.ProjectRuntimeStatusResult, error)
}

// SchedulingSnapshotRequest is the production scheduler's single atomic,
// non-mutating resource query. Both adapters expose the same service operation;
// neither reconstructs the snapshot through lower-level resource calls.
type SchedulingSnapshotRequest = rundomain.SchedulingSnapshotRequest

// SchedulingTarget is the only Project intent data needed to select one
// Explore or Bootstrap candidate.
type SchedulingTarget struct {
	ID string `json:"id"`
}

type ControlSchedulingWork struct {
	ProjectID      string                        `json:"project_id"`
	ConfigRevision string                        `json:"config_revision,omitempty"`
	Generation     int                           `json:"generation"`
	Revision       int64                         `json:"revision"`
	Task           rundomain.TaskType            `json:"task"`
	Target         *SchedulingTarget             `json:"target,omitempty"`
	WorkKey        rundomain.WorkKey             `json:"work_key"`
	State          rundomain.SchedulingWorkState `json:"state"`
}

type ControlDispatcherPolicy struct {
	ConfigRevision      string `json:"config_revision"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	MaxWorkers          int    `json:"max_workers"`
	MaxRunningProjects  int    `json:"max_running_projects"`
}

func (policy ControlDispatcherPolicy) Validate() error {
	if strings.TrimSpace(policy.ConfigRevision) == "" || len(policy.ConfigRevision) > 256 ||
		policy.PollIntervalSeconds < 1 || policy.PollIntervalSeconds > 3600 ||
		policy.MaxWorkers < 1 || policy.MaxWorkers > rundomain.MaximumObservedRunIDs ||
		policy.MaxRunningProjects < 1 || policy.MaxRunningProjects > rundomain.MaximumObservedRunIDs {
		return fmt.Errorf("Control Plane Dispatcher policy is invalid")
	}
	return nil
}

type ControlSchedulingSnapshot struct {
	ObservedAt        time.Time               `json:"observed_at"`
	DispatcherPolicy  ControlDispatcherPolicy `json:"dispatcher_policy"`
	NextProjectCursor string                  `json:"next_project_cursor,omitempty"`
	RevokedRunIDs     []rundomain.RunID       `json:"revoked_run_ids"`
	Work              []ControlSchedulingWork `json:"work"`
}

func (snapshot ControlSchedulingSnapshot) Validate(request SchedulingSnapshotRequest) error {
	if snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.Location() != time.UTC ||
		snapshot.DispatcherPolicy.Validate() != nil ||
		request.CandidateLimit <= 0 || request.CandidateLimit > rundomain.MaximumSchedulingCandidates ||
		len(snapshot.Work) > request.CandidateLimit || len(snapshot.NextProjectCursor) > rundomain.MaximumSchedulingProjectCursorBytes {
		return fmt.Errorf("Control Plane scheduling snapshot is invalid")
	}
	if len(snapshot.RevokedRunIDs) > rundomain.MaximumObservedRunIDs {
		return fmt.Errorf("Control Plane scheduling revocations exceed the bounded limit")
	}
	revoked := make(map[rundomain.RunID]struct{}, len(snapshot.RevokedRunIDs))
	observed := make(map[rundomain.RunID]struct{}, len(request.ObservedRunIDs))
	for _, runID := range request.ObservedRunIDs {
		observed[runID] = struct{}{}
	}
	for _, runID := range snapshot.RevokedRunIDs {
		if runID == "" {
			return fmt.Errorf("Control Plane scheduling revoked Run identity is invalid")
		}
		if _, duplicate := revoked[runID]; duplicate {
			return fmt.Errorf("Control Plane scheduling revoked Run is duplicated")
		}
		if _, requested := observed[runID]; !requested {
			return fmt.Errorf("Control Plane scheduling revoked an unobserved Run")
		}
		revoked[runID] = struct{}{}
	}
	seen := make(map[rundomain.WorkKey]struct{}, len(snapshot.Work))
	for _, candidate := range snapshot.Work {
		if candidate.ProjectID == "" || strings.TrimSpace(candidate.ConfigRevision) == "" || len(candidate.ConfigRevision) > 256 ||
			candidate.Generation <= 0 || candidate.Revision < 0 ||
			candidate.WorkKey == "" || candidate.State.WorkKey != candidate.WorkKey {
			return fmt.Errorf("Control Plane scheduling candidate identity is invalid")
		}
		switch candidate.Task {
		case rundomain.TaskReason:
			if candidate.Target != nil {
				return fmt.Errorf("Control Plane Reason candidate has a target")
			}
		case rundomain.TaskExplore, rundomain.TaskBootstrap:
			if candidate.Target == nil || candidate.Target.ID == "" {
				return fmt.Errorf("Control Plane scheduling candidate target is invalid")
			}
		default:
			return fmt.Errorf("Control Plane scheduling candidate task is invalid")
		}
		if _, duplicate := seen[candidate.WorkKey]; duplicate {
			return fmt.Errorf("Control Plane scheduling candidate is duplicated")
		}
		seen[candidate.WorkKey] = struct{}{}
	}
	return nil
}
