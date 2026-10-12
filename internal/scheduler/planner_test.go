package scheduler

import (
	"reflect"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestPlannerIsDeterministicPureAndHonorsSchedulingGates(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	snapshot := SchedulingSnapshot{
		Now: now, Capacity: 2,
		Running: []RunningRunSnapshot{{RunID: "run-active", WorkKey: "work-active", WorkerName: "busy"}},
		Work: []WorkCandidate{
			{
				WorkKey: "work-ready", ProjectID: "project-b", Task: rundomain.TaskReason,
				RetryState: rundomain.RetryEligible, PreviousWorker: "previous",
				Workers: []WorkerCandidate{
					{Name: "previous", Priority: 10, Ready: true, MaxRunning: 1},
					{Name: "alternate", Priority: 0, Ready: true, MaxRunning: 1},
				},
			},
			{
				WorkKey: "work-active", ProjectID: "project-a", Task: rundomain.TaskReason,
				RetryState: rundomain.RetryEligible,
				Workers:    []WorkerCandidate{{Name: "free", Ready: true}},
			},
			{
				WorkKey: "work-deferred", ProjectID: "project-c", Task: rundomain.TaskReason,
				RetryState: rundomain.RetryBackoff, NextEligibleAt: now.Add(time.Second),
				Workers: []WorkerCandidate{{Name: "free", Ready: true}},
			},
		},
	}
	original := SchedulingSnapshot{
		Now: snapshot.Now, Capacity: snapshot.Capacity,
		Running: append([]RunningRunSnapshot(nil), snapshot.Running...),
		Work:    make([]WorkCandidate, len(snapshot.Work)),
	}
	copy(original.Work, snapshot.Work)
	for index := range original.Work {
		original.Work[index].Workers = append([]WorkerCandidate(nil), snapshot.Work[index].Workers...)
	}

	planner := Planner{}
	first := planner.Plan(snapshot)
	second := planner.Plan(snapshot)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("non-deterministic plans: first=%+v second=%+v", first, second)
	}
	if len(first.Actions) != 1 || first.Actions[0].WorkKey != "work-ready" || first.Actions[0].WorkerName != "previous" {
		t.Fatalf("plan: %+v", first)
	}
	if !reflect.DeepEqual(snapshot, original) {
		t.Fatalf("Planner mutated its input: got=%+v want=%+v", snapshot, original)
	}
}

func TestPlannerFallsBackWhenPreviousWorkerIsUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		workers []WorkerCandidate
		running []RunningRunSnapshot
		want    string
	}{
		{
			name: "not configured or unsupported",
			workers: []WorkerCandidate{
				{Name: "alternate", Priority: 1, Ready: true, MaxRunning: 1},
			},
			want: "alternate",
		},
		{
			name: "not ready",
			workers: []WorkerCandidate{
				{Name: "previous", Priority: 0, Ready: false, MaxRunning: 1},
				{Name: "alternate", Priority: 1, Ready: true, MaxRunning: 1},
			},
			want: "alternate",
		},
		{
			name: "at capacity",
			workers: []WorkerCandidate{
				{Name: "previous", Priority: 0, Ready: true, MaxRunning: 1},
				{Name: "alternate", Priority: 1, Ready: true, MaxRunning: 1},
			},
			running: []RunningRunSnapshot{{RunID: "active", WorkKey: "other", WorkerName: "previous"}},
			want:    "alternate",
		},
		{
			name: "explicit decline cooldown",
			workers: []WorkerCandidate{
				{Name: "previous", Priority: 0, Ready: true, MaxRunning: 1, RetryAfter: now.Add(time.Minute)},
				{Name: "alternate", Priority: 1, Ready: true, MaxRunning: 1},
			},
			want: "alternate",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := (Planner{}).Plan(SchedulingSnapshot{
				Now: now, Capacity: 2, Running: test.running,
				Work: []WorkCandidate{{
					WorkKey: "retry", ProjectID: "project", Task: rundomain.TaskReason,
					RetryState: rundomain.RetryEligible, PreviousWorker: "previous", Workers: test.workers,
				}},
			})
			if len(plan.Actions) != 1 || plan.Actions[0].WorkerName != test.want {
				t.Fatalf("plan: %+v", plan)
			}
		})
	}
}

func TestPlannerKeepsDistinctWorkKeysConcurrentAndOneActiveRunPerWorkKey(t *testing.T) {
	plan := (Planner{}).Plan(SchedulingSnapshot{
		Now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC), Capacity: 3,
		Running: []RunningRunSnapshot{{
			RunID: "run-active", WorkKey: "work-active", ProjectID: "project-a", WorkerName: "worker-a",
		}},
		Work: []WorkCandidate{
			{
				WorkKey: "work-active", ProjectID: "project-a", Task: rundomain.TaskExplore,
				RetryState: rundomain.RetryEligible,
				Workers:    []WorkerCandidate{{Name: "worker-a", Ready: true, MaxRunning: 2}},
			},
			{
				WorkKey: "work-b", ProjectID: "project-b", Task: rundomain.TaskExplore,
				RetryState: rundomain.RetryEligible,
				Workers:    []WorkerCandidate{{Name: "worker-a", Ready: true, MaxRunning: 2}},
			},
			{
				WorkKey: "work-c", ProjectID: "project-c", Task: rundomain.TaskExplore,
				RetryState: rundomain.RetryEligible,
				Workers:    []WorkerCandidate{{Name: "worker-b", Ready: true, MaxRunning: 1}},
			},
		},
	})
	if len(plan.Actions) != 2 || plan.Actions[0].WorkKey != "work-b" || plan.Actions[1].WorkKey != "work-c" {
		t.Fatalf("concurrent Work Key plan: %+v", plan)
	}
}

func TestPlannerFiltersWorkerReadinessCooldownAndCapacity(t *testing.T) {
	now := time.Now().UTC()
	planner := Planner{MaxActions: 1}
	plan := planner.Plan(SchedulingSnapshot{
		Now: now, Capacity: 2,
		Work: []WorkCandidate{
			{WorkKey: "a", ProjectID: "p", Task: rundomain.TaskReason, RetryState: rundomain.RetryEligible,
				Workers: []WorkerCandidate{{Name: "not-ready", Ready: false}}},
			{WorkKey: "b", ProjectID: "p", Task: rundomain.TaskReason, RetryState: rundomain.RetryEligible,
				Workers: []WorkerCandidate{{Name: "cooling", Ready: true, RetryAfter: now.Add(time.Second)}}},
			{WorkKey: "c", ProjectID: "p", Task: rundomain.TaskReason, RetryState: rundomain.RetryEligible,
				Workers: []WorkerCandidate{{Name: "available", Ready: true}}},
			{WorkKey: "d", ProjectID: "p", Task: rundomain.TaskReason, RetryState: rundomain.RetryEligible,
				Workers: []WorkerCandidate{{Name: "also-available", Ready: true}}},
		},
	})
	if len(plan.Actions) != 1 || plan.Actions[0].WorkKey != "c" {
		t.Fatalf("gated plan: %+v", plan)
	}
}

func TestPlannerHonorsProjectAndRunningProjectCapacity(t *testing.T) {
	now := time.Now().UTC()
	plan := (Planner{}).Plan(SchedulingSnapshot{
		Now: now, Capacity: 8, MaxRunningProjects: 2,
		Running: []RunningRunSnapshot{
			{RunID: "run-a", WorkKey: "old-a", ProjectID: "project-a", WorkerName: "worker"},
			{RunID: "run-b", WorkKey: "old-b", ProjectID: "project-b", WorkerName: "worker"},
		},
		Work: []WorkCandidate{
			{WorkKey: "a-1", ProjectID: "project-a", Task: rundomain.TaskReason, RetryState: rundomain.RetryEligible, MaxProjectRunning: 2,
				Workers: []WorkerCandidate{{Name: "worker", Ready: true, MaxRunning: 8}}},
			{WorkKey: "a-2", ProjectID: "project-a", Task: rundomain.TaskExplore, RetryState: rundomain.RetryEligible, MaxProjectRunning: 2,
				Workers: []WorkerCandidate{{Name: "worker", Ready: true, MaxRunning: 8}}},
			{WorkKey: "c-1", ProjectID: "project-c", Task: rundomain.TaskReason, RetryState: rundomain.RetryEligible, MaxProjectRunning: 2,
				Workers: []WorkerCandidate{{Name: "worker", Ready: true, MaxRunning: 8}}},
		},
	})
	if len(plan.Actions) != 1 || plan.Actions[0].WorkKey != "a-1" {
		t.Fatalf("project capacity plan: %+v", plan)
	}
}

func TestPlannerRotatesAndInterleavesProjects(t *testing.T) {
	worker := []WorkerCandidate{{Name: "worker", Ready: true, MaxRunning: 8}}
	plan := (Planner{}).Plan(SchedulingSnapshot{
		Now: time.Now().UTC(), Capacity: 3, ProjectCursor: "project-a",
		Work: []WorkCandidate{
			{WorkKey: "a-1", ProjectID: "project-a", RetryState: rundomain.RetryEligible, Workers: worker},
			{WorkKey: "a-2", ProjectID: "project-a", RetryState: rundomain.RetryEligible, Workers: worker},
			{WorkKey: "b-1", ProjectID: "project-b", RetryState: rundomain.RetryEligible, Workers: worker},
		},
	})
	if len(plan.Actions) != 3 || plan.Actions[0].ProjectID != "project-b" ||
		plan.Actions[1].WorkKey != "a-1" || plan.Actions[2].WorkKey != "a-2" {
		t.Fatalf("fair plan: %+v", plan)
	}
}

func TestPlannerSchedulesReasonBeforeExploreForSameProject(t *testing.T) {
	worker := []WorkerCandidate{{Name: "worker", Ready: true, MaxRunning: 8}}
	plan := (Planner{}).Plan(SchedulingSnapshot{
		Now: time.Now().UTC(), Capacity: 2,
		Work: []WorkCandidate{
			{WorkKey: "a-explore", ProjectID: "project-a", Task: rundomain.TaskExplore,
				RetryState: rundomain.RetryEligible, Workers: worker},
			{WorkKey: "z-reason", ProjectID: "project-a", Task: rundomain.TaskReason,
				RetryState: rundomain.RetryEligible, Workers: worker},
		},
	})
	if len(plan.Actions) != 1 || plan.Actions[0].Task != rundomain.TaskReason {
		t.Fatalf("business-priority plan: %+v", plan)
	}
}

func TestPlannerPreservesControlPlaneOrderWithinTaskPriority(t *testing.T) {
	worker := []WorkerCandidate{{Name: "worker", Ready: true, MaxRunning: 8}}
	plan := (Planner{}).Plan(SchedulingSnapshot{
		Now: time.Now().UTC(), Capacity: 2,
		Work: []WorkCandidate{
			{WorkKey: "z-oldest", ProjectID: "project-a", Task: rundomain.TaskExplore,
				RetryState: rundomain.RetryEligible, Workers: worker},
			{WorkKey: "a-newest", ProjectID: "project-a", Task: rundomain.TaskExplore,
				RetryState: rundomain.RetryEligible, Workers: worker},
		},
	})
	if len(plan.Actions) != 2 || plan.Actions[0].WorkKey != "z-oldest" || plan.Actions[1].WorkKey != "a-newest" {
		t.Fatalf("Control Plane order was not preserved: %+v", plan)
	}
}

func TestPlannerPreservesControlPlaneProjectRotation(t *testing.T) {
	worker := []WorkerCandidate{{Name: "worker", Ready: true, MaxRunning: 8}}
	plan := (Planner{}).Plan(SchedulingSnapshot{
		Now: time.Now().UTC(), Capacity: 2,
		Work: []WorkCandidate{
			{WorkKey: "project-c-work", ProjectID: "project-c", Task: rundomain.TaskReason,
				RetryState: rundomain.RetryEligible, Workers: worker},
			{WorkKey: "project-a-work", ProjectID: "project-a", Task: rundomain.TaskReason,
				RetryState: rundomain.RetryEligible, Workers: worker},
		},
	})
	if len(plan.Actions) != 2 || plan.Actions[0].ProjectID != "project-c" || plan.Actions[1].ProjectID != "project-a" {
		t.Fatalf("Control Plane Project rotation was not preserved: %+v", plan)
	}
}

func TestPlannerDoesNotStartExploreWhileReasonIsRunning(t *testing.T) {
	plan := (Planner{}).Plan(SchedulingSnapshot{
		Now: time.Now().UTC(), Capacity: 2,
		Running: []RunningRunSnapshot{{
			RunID: "run-reason", WorkKey: "work-reason", ProjectID: "project-a", Task: rundomain.TaskReason,
			WorkerName: "worker",
		}},
		Work: []WorkCandidate{{
			WorkKey: "work-explore", ProjectID: "project-a", Task: rundomain.TaskExplore,
			RetryState: rundomain.RetryEligible,
			Workers:    []WorkerCandidate{{Name: "worker", Ready: true, MaxRunning: 8}},
		}},
	})
	if len(plan.Actions) != 0 {
		t.Fatalf("explore overlapped active Reason: %+v", plan)
	}
}

func TestPlannerIsolatesSameNamedWorkerCapacityByConfigRevision(t *testing.T) {
	now := time.Now().UTC()
	worker := []WorkerCandidate{{Name: "shared", Ready: true, MaxRunning: 1}}
	plan := (Planner{}).Plan(SchedulingSnapshot{
		Now: now, Capacity: 2,
		Work: []WorkCandidate{
			{WorkKey: "work-a", ProjectID: "project-a", ConfigRevision: "revision-a", Task: rundomain.TaskReason, RetryState: rundomain.RetryEligible, Workers: worker},
			{WorkKey: "work-b", ProjectID: "project-b", ConfigRevision: "revision-b", Task: rundomain.TaskReason, RetryState: rundomain.RetryEligible, Workers: worker},
		},
	})
	if len(plan.Actions) != 2 {
		t.Fatalf("same-named Workers from different Revisions shared capacity: %+v", plan)
	}

	plan = (Planner{}).Plan(SchedulingSnapshot{
		Now: now, Capacity: 2,
		Running: []RunningRunSnapshot{{
			RunID: "run-a", WorkKey: "active-a", ProjectID: "project-a",
			ConfigRevision: "revision-a", WorkerName: "shared",
		}},
		Work: []WorkCandidate{
			{WorkKey: "next-a", ProjectID: "project-c", ConfigRevision: "revision-a", Task: rundomain.TaskReason, RetryState: rundomain.RetryEligible, Workers: worker},
			{WorkKey: "next-b", ProjectID: "project-b", ConfigRevision: "revision-b", Task: rundomain.TaskReason, RetryState: rundomain.RetryEligible, Workers: worker},
		},
	})
	if len(plan.Actions) != 1 || plan.Actions[0].WorkKey != "next-b" {
		t.Fatalf("Revision-scoped Worker capacity plan: %+v", plan)
	}
}

func TestPlannerBlocksNewAdmissionWhenGlobalLimitDropsBelowRunningCount(t *testing.T) {
	plan := (Planner{}).Plan(SchedulingSnapshot{
		Now: time.Now().UTC(), Capacity: 1, MaxRunningProjects: 1,
		Running: []RunningRunSnapshot{
			{RunID: "run-a", WorkKey: "active-a", ProjectID: "project-a"},
			{RunID: "run-b", WorkKey: "active-b", ProjectID: "project-b"},
		},
		Work: []WorkCandidate{{
			WorkKey: "new-work", ProjectID: "project-c", RetryState: rundomain.RetryEligible,
			Workers: []WorkerCandidate{{Name: "worker", Ready: true, MaxRunning: 3}},
		}},
	})
	if len(plan.Actions) != 0 {
		t.Fatalf("reduced global limit admitted new work: %+v", plan)
	}
}
