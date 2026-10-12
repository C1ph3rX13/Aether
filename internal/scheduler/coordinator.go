package scheduler

import (
	"context"
	"fmt"
	"sort"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

var errRunRevokedCancellation = fmt.Errorf("Run was revoked by the Control Plane")

type CoordinatorEvent interface {
	coordinatorEvent()
}

type TickEvent struct {
	Snapshot          SchedulingSnapshot
	NextProjectCursor string
	Reply             chan<- SchedulingPlan
}

func (TickEvent) coordinatorEvent() {}

type RunRevocationsEvent struct {
	RunIDs []rundomain.RunID
}

func (RunRevocationsEvent) coordinatorEvent() {}

type RunStartedEvent struct {
	Run    RunningRunSnapshot
	Cancel context.CancelCauseFunc
}

func (RunStartedEvent) coordinatorEvent() {}

type RunCompletionEvent struct {
	RunID rundomain.RunID
}

func (RunCompletionEvent) coordinatorEvent() {}

type WorkerReadinessEvent struct {
	ConfigRevision string
	WorkerName     string
	Ready          bool
}

func (WorkerReadinessEvent) coordinatorEvent() {}

type LeaseRenewedEvent struct {
	RunID          rundomain.RunID
	LeaseExpiresAt time.Time
}

func (LeaseRenewedEvent) coordinatorEvent() {}

type LeaseLostEvent struct {
	RunID rundomain.RunID
	Err   error
}

func (LeaseLostEvent) coordinatorEvent() {}

type RunDeadlineEvent struct {
	RunID rundomain.RunID
}

func (RunDeadlineEvent) coordinatorEvent() {}

// RunInterruptedEvent represents Dispatcher/process cancellation. It is not
// an operator cancellation and therefore keeps the Work Key retryable.
type RunInterruptedEvent struct {
	RunID rundomain.RunID
}

func (RunInterruptedEvent) coordinatorEvent() {}

type RunExitedEvent struct {
	RunID      rundomain.RunID
	Completion rundomain.Completion
	Err        error
}

func (RunExitedEvent) coordinatorEvent() {}

type RecoveryEvent struct {
	Run    RunningRunSnapshot
	Status rundomain.RecoveryStatus
	Err    error
}

func (RecoveryEvent) coordinatorEvent() {}

type CoordinatorState struct {
	Running         []RunningRunSnapshot
	WorkerReadiness map[WorkerIdentity]bool
	LeaseExpiries   map[rundomain.RunID]time.Time
	ProjectCursor   string
}

type CoordinatorStateEvent struct {
	Reply chan<- CoordinatorState
}

func (CoordinatorStateEvent) coordinatorEvent() {}

// Coordinator serializes all scheduler state transitions in Run. Its event
// handlers are deliberately free of Control Plane and Runtime I/O.
type Coordinator struct {
	planner Planner
	events  chan CoordinatorEvent
}

type managedRun struct {
	snapshot RunningRunSnapshot
	cancel   context.CancelCauseFunc
}

func NewCoordinator(planner Planner, eventBuffer int) *Coordinator {
	if eventBuffer < 0 {
		eventBuffer = 0
	}
	return &Coordinator{planner: planner, events: make(chan CoordinatorEvent, eventBuffer)}
}

func (coordinator *Coordinator) Submit(ctx context.Context, event CoordinatorEvent) error {
	if event == nil {
		return fmt.Errorf("Coordinator event is required")
	}
	select {
	case coordinator.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (coordinator *Coordinator) Events() chan<- CoordinatorEvent {
	return coordinator.events
}

func (coordinator *Coordinator) Run(ctx context.Context) error {
	running := make(map[rundomain.RunID]managedRun)
	readiness := make(map[WorkerIdentity]bool)
	leaseExpiries := make(map[rundomain.RunID]time.Time)
	projectCursor := ""
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-coordinator.events:
			switch typed := event.(type) {
			case TickEvent:
				snapshot := coordinatorSnapshot(typed.Snapshot, running, readiness)
				projectCursor = typed.NextProjectCursor
				if typed.Reply != nil {
					plan := coordinator.planner.Plan(snapshot)
					select {
					case typed.Reply <- plan:
					default:
					}
				}
			case RunStartedEvent:
				if typed.Run.RunID != "" {
					running[typed.Run.RunID] = managedRun{snapshot: typed.Run, cancel: typed.Cancel}
				}
			case RunRevocationsEvent:
				cancelRevokedRuns(typed.RunIDs, running)
			case RunCompletionEvent:
				delete(running, typed.RunID)
				delete(leaseExpiries, typed.RunID)
			case WorkerReadinessEvent:
				if typed.WorkerName != "" {
					readiness[WorkerIdentity{ConfigRevision: typed.ConfigRevision, Name: typed.WorkerName}] = typed.Ready
				}
			case LeaseRenewedEvent:
				if _, exists := running[typed.RunID]; exists {
					leaseExpiries[typed.RunID] = typed.LeaseExpiresAt
				}
			case LeaseLostEvent:
				delete(leaseExpiries, typed.RunID)
			case RunDeadlineEvent:
				delete(leaseExpiries, typed.RunID)
			case RunInterruptedEvent:
				delete(leaseExpiries, typed.RunID)
			case RunExitedEvent:
				delete(leaseExpiries, typed.RunID)
			case RecoveryEvent:
				if typed.Status == rundomain.RecoveryObserved && typed.Run.RunID != "" {
					running[typed.Run.RunID] = managedRun{snapshot: typed.Run}
				} else {
					delete(running, typed.Run.RunID)
					delete(leaseExpiries, typed.Run.RunID)
				}
			case CoordinatorStateEvent:
				if typed.Reply != nil {
					state := copyCoordinatorState(running, readiness, leaseExpiries, projectCursor)
					select {
					case typed.Reply <- state:
					default:
					}
				}
			}
		}
	}
}

func coordinatorSnapshot(
	snapshot SchedulingSnapshot,
	running map[rundomain.RunID]managedRun,
	readiness map[WorkerIdentity]bool,
) SchedulingSnapshot {
	combined := make(map[rundomain.RunID]RunningRunSnapshot, len(snapshot.Running)+len(running))
	for _, active := range snapshot.Running {
		if active.RunID != "" {
			combined[active.RunID] = active
		}
	}
	for _, active := range running {
		combined[active.snapshot.RunID] = active.snapshot
	}
	snapshot.Running = make([]RunningRunSnapshot, 0, len(combined))
	for _, active := range combined {
		snapshot.Running = append(snapshot.Running, active)
	}
	snapshot.Work = append([]WorkCandidate(nil), snapshot.Work...)
	for index := range snapshot.Work {
		snapshot.Work[index].Workers = append([]WorkerCandidate(nil), snapshot.Work[index].Workers...)
		for workerIndex := range snapshot.Work[index].Workers {
			worker := &snapshot.Work[index].Workers[workerIndex]
			identity := WorkerIdentity{ConfigRevision: snapshot.Work[index].ConfigRevision, Name: worker.Name}
			if ready, known := readiness[identity]; known {
				worker.Ready = ready
			}
		}
	}
	return snapshot
}

func cancelRevokedRuns(runIDs []rundomain.RunID, running map[rundomain.RunID]managedRun) {
	for _, runID := range runIDs {
		if active, exists := running[runID]; exists && active.cancel != nil {
			active.cancel(errRunRevokedCancellation)
		}
	}
}

func copyCoordinatorState(
	running map[rundomain.RunID]managedRun,
	readiness map[WorkerIdentity]bool,
	leaseExpiries map[rundomain.RunID]time.Time,
	projectCursor string,
) CoordinatorState {
	state := CoordinatorState{
		Running:         make([]RunningRunSnapshot, 0, len(running)),
		WorkerReadiness: make(map[WorkerIdentity]bool, len(readiness)),
		LeaseExpiries:   make(map[rundomain.RunID]time.Time, len(leaseExpiries)),
		ProjectCursor:   projectCursor,
	}
	for _, active := range running {
		state.Running = append(state.Running, active.snapshot)
	}
	sort.Slice(state.Running, func(i, j int) bool { return state.Running[i].RunID < state.Running[j].RunID })
	for worker, ready := range readiness {
		state.WorkerReadiness[worker] = ready
	}
	for runID, expiresAt := range leaseExpiries {
		state.LeaseExpiries[runID] = expiresAt
	}
	return state
}
