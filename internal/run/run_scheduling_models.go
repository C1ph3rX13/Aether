package run

import "time"

const (
	MaximumSchedulingCandidates         = 1024
	MaximumSchedulingWorkKeys           = MaximumSchedulingCandidates
	MaximumSchedulingInspectedWork      = MaximumSchedulingCandidates * 2
	MaximumSchedulingProjectCursorBytes = 256
	MaximumObservedRunIDs               = MaximumSchedulingCandidates
)

type SchedulingSnapshotRequest struct {
	DispatcherID           string       `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken `json:"dispatcher_fencing_token"`
	SupportedTasks         []TaskType   `json:"supported_tasks"`
	CandidateLimit         int          `json:"candidate_limit"`
	ProjectCursor          string       `json:"project_cursor,omitempty"`
	ObservedRunIDs         []RunID      `json:"observed_run_ids,omitempty"`
}

func (request SchedulingSnapshotRequest) Validate() error {
	if !bounded(request.DispatcherID, 256) || request.DispatcherFencingToken <= 0 || len(request.SupportedTasks) > 3 ||
		request.CandidateLimit <= 0 || request.CandidateLimit > MaximumSchedulingCandidates ||
		len(request.ProjectCursor) > MaximumSchedulingProjectCursorBytes {
		return newError(ErrorInvalidArgument, "scheduling snapshot request is invalid")
	}
	seen := make(map[TaskType]struct{}, len(request.SupportedTasks))
	for _, task := range request.SupportedTasks {
		if !task.valid() {
			return newError(ErrorInvalidArgument, "scheduling snapshot task is invalid")
		}
		if _, duplicate := seen[task]; duplicate {
			return newError(ErrorInvalidArgument, "scheduling snapshot tasks must be unique")
		}
		seen[task] = struct{}{}
	}
	if len(request.ObservedRunIDs) > MaximumObservedRunIDs {
		return newError(ErrorInvalidArgument, "scheduling snapshot observed Runs exceed the bounded limit")
	}
	observed := make(map[RunID]struct{}, len(request.ObservedRunIDs))
	for _, runID := range request.ObservedRunIDs {
		if !bounded(string(runID), 256) {
			return newError(ErrorInvalidArgument, "scheduling snapshot observed Run is invalid")
		}
		if _, duplicate := observed[runID]; duplicate {
			return newError(ErrorInvalidArgument, "scheduling snapshot observed Runs must be unique")
		}
		observed[runID] = struct{}{}
	}
	return nil
}

type SchedulingWorkQuery struct {
	WorkKey        WorkKey  `json:"work_key"`
	ProjectID      string   `json:"project_id"`
	Task           TaskType `json:"task"`
	Generation     int      `json:"generation"`
	Revision       int64    `json:"revision"`
	HasOpenIntents bool     `json:"has_open_intents"`
}

type SchedulingWorkState struct {
	WorkKey          WorkKey              `json:"work_key"`
	RetryState       RetryState           `json:"retry_state"`
	Claimed          bool                 `json:"-"`
	Attempt          int64                `json:"attempt"`
	NextEligibleAt   *time.Time           `json:"next_eligible_at,omitempty"`
	PreviousWorker   string               `json:"previous_worker,omitempty"`
	WorkerRetryAfter map[string]time.Time `json:"worker_retry_after,omitempty"`
}
