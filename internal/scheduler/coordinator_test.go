package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func TestCoordinatorSerializesTypedStateEventsAndPlansFromOwnedState(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	coordinator := NewCoordinator(Planner{}, 16)
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()

	active := RunningRunSnapshot{RunID: "run-1", WorkKey: "work-1", WorkerName: "worker-a"}
	submitCoordinatorEvent(t, coordinator, RunStartedEvent{Run: active})
	submitCoordinatorEvent(t, coordinator, WorkerReadinessEvent{WorkerName: "worker-b", Ready: false})
	expiresAt := time.Now().UTC().Add(time.Minute)
	submitCoordinatorEvent(t, coordinator, LeaseRenewedEvent{RunID: active.RunID, LeaseExpiresAt: expiresAt})

	stateReply := make(chan CoordinatorState, 1)
	submitCoordinatorEvent(t, coordinator, CoordinatorStateEvent{Reply: stateReply})
	state := <-stateReply
	if len(state.Running) != 1 || state.Running[0].RunID != active.RunID ||
		state.WorkerReadiness[WorkerIdentity{Name: "worker-b"}] || !state.LeaseExpiries[active.RunID].Equal(expiresAt) {
		t.Fatalf("Coordinator state: %+v", state)
	}

	planReply := make(chan SchedulingPlan, 1)
	submitCoordinatorEvent(t, coordinator, TickEvent{
		Snapshot: SchedulingSnapshot{
			Now: time.Now().UTC(), Capacity: 2,
			Work: []WorkCandidate{{
				WorkKey: "work-2", ProjectID: "project", Task: rundomain.TaskReason,
				RetryState: rundomain.RetryEligible,
				Workers:    []WorkerCandidate{{Name: "worker-b", Ready: true}},
			}},
		},
		Reply: planReply,
	})
	if plan := <-planReply; len(plan.Actions) != 0 {
		t.Fatalf("Readiness event did not gate planning: %+v", plan)
	}

	submitCoordinatorEvent(t, coordinator, WorkerReadinessEvent{WorkerName: "worker-b", Ready: true})
	submitCoordinatorEvent(t, coordinator, LeaseLostEvent{RunID: active.RunID, Err: errors.New("fenced")})
	snapshotAfterTerminal := SchedulingSnapshot{
		Now: time.Now().UTC(), Capacity: 1,
		Work: []WorkCandidate{{
			WorkKey: "work-2", ProjectID: "project", Task: rundomain.TaskReason,
			RetryState: rundomain.RetryEligible,
			Workers:    []WorkerCandidate{{Name: "worker-b", Ready: false}},
		}},
	}
	planReply = make(chan SchedulingPlan, 1)
	submitCoordinatorEvent(t, coordinator, TickEvent{
		Snapshot: snapshotAfterTerminal, Reply: planReply,
	})
	if plan := <-planReply; len(plan.Actions) != 0 {
		t.Fatalf("terminal event released capacity before cleanup: %+v", plan)
	}
	submitCoordinatorEvent(t, coordinator, RunCompletionEvent{RunID: active.RunID})
	planReply = make(chan SchedulingPlan, 1)
	submitCoordinatorEvent(t, coordinator, TickEvent{Snapshot: snapshotAfterTerminal, Reply: planReply})
	if plan := <-planReply; len(plan.Actions) != 1 || plan.Actions[0].WorkerName != "worker-b" {
		t.Fatalf("serial Coordinator plan: %+v", plan)
	}

	recovered := RunningRunSnapshot{RunID: "run-old", WorkKey: "work-old", WorkerName: "worker-old"}
	submitCoordinatorEvent(t, coordinator, RecoveryEvent{Run: recovered, Status: rundomain.RecoveryObserved})
	submitCoordinatorEvent(t, coordinator, RecoveryEvent{Run: recovered, Status: rundomain.RecoveryDisposed})
	stateReply = make(chan CoordinatorState, 1)
	submitCoordinatorEvent(t, coordinator, CoordinatorStateEvent{Reply: stateReply})
	if state := <-stateReply; len(state.Running) != 0 {
		t.Fatalf("recovery did not retire observed Run: %+v", state)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Coordinator shutdown: %v", err)
	}
}

func TestCoordinatorScopesWorkerReadinessByConfigRevision(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	coordinator := NewCoordinator(Planner{}, 8)
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()

	submitCoordinatorEvent(t, coordinator, WorkerReadinessEvent{
		ConfigRevision: "revision-a", WorkerName: "shared", Ready: false,
	})
	reply := make(chan SchedulingPlan, 1)
	submitCoordinatorEvent(t, coordinator, TickEvent{
		Snapshot: SchedulingSnapshot{
			Now: time.Now().UTC(), Capacity: 2,
			Work: []WorkCandidate{
				{WorkKey: "work-a", ProjectID: "project-a", ConfigRevision: "revision-a", RetryState: rundomain.RetryEligible, Workers: []WorkerCandidate{{Name: "shared", Ready: true}}},
				{WorkKey: "work-b", ProjectID: "project-b", ConfigRevision: "revision-b", RetryState: rundomain.RetryEligible, Workers: []WorkerCandidate{{Name: "shared", Ready: true}}},
			},
		},
		Reply: reply,
	})
	plan := <-reply
	if len(plan.Actions) != 1 || plan.Actions[0].WorkKey != "work-b" {
		t.Fatalf("Revision-scoped readiness plan: %+v", plan)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCoordinatorDoesNotBlockOnAbandonedReply(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	coordinator := NewCoordinator(Planner{}, 4)
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()

	submitCoordinatorEvent(t, coordinator, TickEvent{
		Snapshot: SchedulingSnapshot{Now: time.Now().UTC(), Capacity: 1},
		Reply:    make(chan SchedulingPlan),
	})
	stateReply := make(chan CoordinatorState, 1)
	submitCoordinatorEvent(t, coordinator, CoordinatorStateEvent{Reply: stateReply})
	select {
	case <-stateReply:
	case <-time.After(time.Second):
		t.Fatal("abandoned Tick reply blocked Coordinator")
	}
	cancel()
	<-done
}

func TestCoordinatorAdvancesControlPlaneCursorWithoutDispatchAction(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	coordinator := NewCoordinator(Planner{}, 4)
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()

	reply := make(chan SchedulingPlan, 1)
	submitCoordinatorEvent(t, coordinator, TickEvent{
		Snapshot: SchedulingSnapshot{
			Now: time.Now().UTC(), Capacity: 1,
			Work: []WorkCandidate{{
				WorkKey: "blocked", ProjectID: "project-a", Task: rundomain.TaskReason,
				RetryState: rundomain.RetryEligible,
				Workers:    []WorkerCandidate{{Name: "offline", Ready: false}},
			}},
		},
		NextProjectCursor: "project-a",
		Reply:             reply,
	})
	if plan := <-reply; len(plan.Actions) != 0 {
		t.Fatalf("unavailable Worker produced actions: %+v", plan)
	}
	stateReply := make(chan CoordinatorState, 1)
	submitCoordinatorEvent(t, coordinator, CoordinatorStateEvent{Reply: stateReply})
	if state := <-stateReply; state.ProjectCursor != "project-a" {
		t.Fatalf("Coordinator cursor = %q, want project-a", state.ProjectCursor)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCoordinatorPlannerIncludesCallerRunningSnapshot(t *testing.T) {
	coordinator := NewCoordinator(Planner{}, 4)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()
	reply := make(chan SchedulingPlan, 1)
	if err := coordinator.Submit(t.Context(), TickEvent{
		Snapshot: SchedulingSnapshot{
			Now: time.Now().UTC(), Capacity: 1,
			Running: []RunningRunSnapshot{{RunID: "legacy-run", WorkKey: "legacy-work", WorkerName: "worker-a"}},
			Work: []WorkCandidate{{
				WorkKey: "new-work", ProjectID: "project", Task: rundomain.TaskReason,
				RetryState: rundomain.RetryEligible,
				Workers:    []WorkerCandidate{{Name: "worker-a", Ready: true, MaxRunning: 2}},
			}},
		},
		Reply: reply,
	}); err != nil {
		t.Fatal(err)
	}
	if plan := <-reply; len(plan.Actions) != 0 {
		t.Fatalf("Planner ignored caller running snapshot: %+v", plan)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCoordinatorCancelsOnlyRevokedRunsWithControlPlaneResolvedCause(t *testing.T) {
	coordinator := NewCoordinator(Planner{}, 4)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(ctx) }()

	runContext, cancelRun := context.WithCancelCause(t.Context())
	submitCoordinatorEvent(t, coordinator, RunStartedEvent{
		Run: RunningRunSnapshot{RunID: "run-1", ProjectID: "project-1"}, Cancel: cancelRun,
	})
	otherContext, cancelOther := context.WithCancelCause(t.Context())
	submitCoordinatorEvent(t, coordinator, RunStartedEvent{
		Run: RunningRunSnapshot{RunID: "run-2", ProjectID: "project-1"}, Cancel: cancelOther,
	})
	submitCoordinatorEvent(t, coordinator, RunRevocationsEvent{RunIDs: []rundomain.RunID{"run-1"}})
	select {
	case <-runContext.Done():
		if !errors.Is(context.Cause(runContext), errRunRevokedCancellation) {
			t.Fatalf("cancellation cause = %v", context.Cause(runContext))
		}
	case <-time.After(time.Second):
		t.Fatal("revoked Run was not cancelled")
	}
	select {
	case <-otherContext.Done():
		t.Fatal("unrevoked Run was cancelled")
	default:
	}
	cancel()
	<-done
}

func submitCoordinatorEvent(t *testing.T, coordinator *Coordinator, event CoordinatorEvent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := coordinator.Submit(ctx, event); err != nil {
		t.Fatal(err)
	}
}
