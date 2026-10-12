package run

import "testing"

func TestSchedulingSnapshotRequestValidatesCandidateWindow(t *testing.T) {
	valid := SchedulingSnapshotRequest{
		DispatcherID: "dispatcher-a", DispatcherFencingToken: 1,
		SupportedTasks: []TaskType{TaskReason}, CandidateLimit: 32, ProjectCursor: "p000032",
		ObservedRunIDs: []RunID{"run-a", "run-b"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid scheduling window: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SchedulingSnapshotRequest)
	}{
		{name: "missing limit", mutate: func(request *SchedulingSnapshotRequest) { request.CandidateLimit = 0 }},
		{name: "negative limit", mutate: func(request *SchedulingSnapshotRequest) { request.CandidateLimit = -1 }},
		{name: "above hard limit", mutate: func(request *SchedulingSnapshotRequest) { request.CandidateLimit = MaximumSchedulingCandidates + 1 }},
		{name: "oversized cursor", mutate: func(request *SchedulingSnapshotRequest) {
			request.ProjectCursor = string(make([]byte, MaximumSchedulingProjectCursorBytes+1))
		}},
		{name: "too many observed runs", mutate: func(request *SchedulingSnapshotRequest) {
			request.ObservedRunIDs = make([]RunID, MaximumObservedRunIDs+1)
			for index := range request.ObservedRunIDs {
				request.ObservedRunIDs[index] = RunID(stringOfByte('r', 250) + string(rune(index)))
			}
		}},
		{name: "duplicate observed run", mutate: func(request *SchedulingSnapshotRequest) {
			request.ObservedRunIDs = []RunID{"run-a", "run-a"}
		}},
		{name: "invalid observed run", mutate: func(request *SchedulingSnapshotRequest) {
			request.ObservedRunIDs = []RunID{""}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			test.mutate(&request)
			if err := request.Validate(); err == nil {
				t.Fatalf("invalid request accepted: %+v", request)
			}
		})
	}
}

func stringOfByte(value byte, count int) string {
	buffer := make([]byte, count)
	for index := range buffer {
		buffer[index] = value
	}
	return string(buffer)
}
