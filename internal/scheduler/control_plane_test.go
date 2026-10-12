package scheduler

import (
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestControlSchedulingSnapshotRejectsUnobservedRevocation(t *testing.T) {
	request := SchedulingSnapshotRequest{
		DispatcherID: "dispatcher", DispatcherFencingToken: 1,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 1,
		ObservedRunIDs: []rundomain.RunID{"observed-run"},
	}
	snapshot := ControlSchedulingSnapshot{
		ObservedAt: time.Now().UTC(), DispatcherPolicy: ControlDispatcherPolicy{
			ConfigRevision: "config", PollIntervalSeconds: 1, MaxWorkers: 1, MaxRunningProjects: 1,
		},
		RevokedRunIDs: []rundomain.RunID{"other-run"},
	}
	if err := snapshot.Validate(request); err == nil {
		t.Fatal("Control Plane response revoked a Run the Dispatcher did not observe")
	}
}

func TestControlSchedulingSnapshotRequiresCandidateConfigRevision(t *testing.T) {
	request := SchedulingSnapshotRequest{
		DispatcherID: "dispatcher", DispatcherFencingToken: 1,
		SupportedTasks: []rundomain.TaskType{rundomain.TaskReason}, CandidateLimit: 1,
	}
	snapshot := ControlSchedulingSnapshot{
		ObservedAt: time.Now().UTC(), DispatcherPolicy: ControlDispatcherPolicy{
			ConfigRevision: "config", PollIntervalSeconds: 1, MaxWorkers: 1, MaxRunningProjects: 1,
		},
		Work: []ControlSchedulingWork{{
			ProjectID: "project", Generation: 1, Revision: 1, Task: rundomain.TaskReason,
			WorkKey: "reason:project:1:1", State: rundomain.SchedulingWorkState{WorkKey: "reason:project:1:1"},
		}},
	}
	if err := snapshot.Validate(request); err == nil {
		t.Fatal("Control Plane response accepted a candidate without its bound Config Revision")
	}
}
