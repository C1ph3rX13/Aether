package scheduler

import (
	"sort"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// SchedulingSnapshot is an immutable view consumed by Planner. It contains no
// adapters or database handles, so planning is deterministic and side-effect free.
type SchedulingSnapshot struct {
	Now                time.Time
	Capacity           int
	MaxRunningProjects int
	ProjectCursor      string
	Running            []RunningRunSnapshot
	Work               []WorkCandidate
}

type RunningRunSnapshot struct {
	RunID          rundomain.RunID
	WorkKey        rundomain.WorkKey
	ProjectID      string
	ConfigRevision string
	Task           rundomain.TaskType
	WorkerName     string
}

type WorkCandidate struct {
	WorkKey           rundomain.WorkKey
	ProjectID         string
	ConfigRevision    string
	Task              rundomain.TaskType
	Attempt           int64
	RetryState        rundomain.RetryState
	NextEligibleAt    time.Time
	PreviousWorker    string
	MaxProjectRunning int
	Workers           []WorkerCandidate
}

type WorkerCandidate struct {
	Name       string
	Priority   int
	MaxRunning int
	Ready      bool
	RetryAfter time.Time
}

type DispatchAction struct {
	WorkKey    rundomain.WorkKey
	ProjectID  string
	Task       rundomain.TaskType
	WorkerName string
}

type SchedulingPlan struct {
	Actions []DispatchAction
}

type WorkerIdentity struct {
	ConfigRevision string
	Name           string
}

type Planner struct {
	MaxActions int
}

func (planner Planner) Plan(snapshot SchedulingSnapshot) SchedulingPlan {
	if snapshot.Now.IsZero() || snapshot.Capacity <= 0 {
		return SchedulingPlan{}
	}
	limit := snapshot.Capacity - len(snapshot.Running)
	if planner.MaxActions > 0 && planner.MaxActions < limit {
		limit = planner.MaxActions
	}
	if limit <= 0 {
		return SchedulingPlan{}
	}

	runningWork := make(map[rundomain.WorkKey]struct{}, len(snapshot.Running))
	runningWorkers := make(map[WorkerIdentity]int)
	runningProjects := make(map[string]int)
	projectDecisionActive := make(map[string]bool)
	for _, running := range snapshot.Running {
		if running.WorkKey != "" {
			runningWork[running.WorkKey] = struct{}{}
		}
		if running.WorkerName != "" {
			runningWorkers[WorkerIdentity{ConfigRevision: running.ConfigRevision, Name: running.WorkerName}]++
		}
		if running.ProjectID != "" {
			runningProjects[running.ProjectID]++
			if businessTaskPriority(running.Task) < businessTaskPriority(rundomain.TaskExplore) {
				projectDecisionActive[running.ProjectID] = true
			}
		}
	}
	work := fairWorkOrder(snapshot.Work, snapshot.ProjectCursor)

	plan := SchedulingPlan{Actions: make([]DispatchAction, 0, limit)}
	for _, candidate := range work {
		if len(plan.Actions) >= limit {
			break
		}
		if candidate.WorkKey == "" || candidate.ProjectID == "" {
			continue
		}
		if _, exists := runningWork[candidate.WorkKey]; exists || !eligibleAt(candidate, snapshot.Now) {
			continue
		}
		if candidate.Task == rundomain.TaskExplore && projectDecisionActive[candidate.ProjectID] {
			continue
		}
		maxProjectRunning := candidate.MaxProjectRunning
		if maxProjectRunning > 0 && runningProjects[candidate.ProjectID] >= maxProjectRunning {
			continue
		}
		if snapshot.MaxRunningProjects > 0 && runningProjects[candidate.ProjectID] == 0 &&
			len(runningProjects) >= snapshot.MaxRunningProjects {
			continue
		}
		workers := append([]WorkerCandidate(nil), candidate.Workers...)
		sort.SliceStable(workers, func(i, j int) bool {
			left, right := workers[i], workers[j]
			leftPrevious := left.Name == candidate.PreviousWorker
			rightPrevious := right.Name == candidate.PreviousWorker
			if leftPrevious != rightPrevious {
				return leftPrevious
			}
			if left.Priority != right.Priority {
				return left.Priority < right.Priority
			}
			leftRunning := runningWorkers[WorkerIdentity{ConfigRevision: candidate.ConfigRevision, Name: left.Name}]
			rightRunning := runningWorkers[WorkerIdentity{ConfigRevision: candidate.ConfigRevision, Name: right.Name}]
			if leftRunning != rightRunning {
				return leftRunning < rightRunning
			}
			return left.Name < right.Name
		})
		for _, worker := range workers {
			if !worker.Ready || worker.Name == "" || worker.RetryAfter.After(snapshot.Now) {
				continue
			}
			identity := WorkerIdentity{ConfigRevision: candidate.ConfigRevision, Name: worker.Name}
			if worker.MaxRunning > 0 && runningWorkers[identity] >= worker.MaxRunning {
				continue
			}
			plan.Actions = append(plan.Actions, DispatchAction{
				WorkKey: candidate.WorkKey, ProjectID: candidate.ProjectID,
				Task: candidate.Task, WorkerName: worker.Name,
			})
			runningWork[candidate.WorkKey] = struct{}{}
			runningWorkers[identity]++
			runningProjects[candidate.ProjectID]++
			if businessTaskPriority(candidate.Task) < businessTaskPriority(rundomain.TaskExplore) {
				projectDecisionActive[candidate.ProjectID] = true
			}
			break
		}
	}
	return plan
}

func fairWorkOrder(work []WorkCandidate, cursor string) []WorkCandidate {
	byProject := make(map[string][]WorkCandidate)
	projects := make([]string, 0)
	for _, candidate := range work {
		if _, exists := byProject[candidate.ProjectID]; !exists {
			projects = append(projects, candidate.ProjectID)
		}
		byProject[candidate.ProjectID] = append(byProject[candidate.ProjectID], candidate)
	}
	for projectID := range byProject {
		sort.SliceStable(byProject[projectID], func(i, j int) bool {
			leftPriority := businessTaskPriority(byProject[projectID][i].Task)
			rightPriority := businessTaskPriority(byProject[projectID][j].Task)
			return leftPriority < rightPriority
		})
	}
	if cursor != "" && len(projects) > 1 {
		sort.Strings(projects)
		start := sort.SearchStrings(projects, cursor)
		for start < len(projects) && projects[start] <= cursor {
			start++
		}
		if start == len(projects) {
			start = 0
		}
		projects = append(append([]string(nil), projects[start:]...), projects[:start]...)
	}
	ordered := make([]WorkCandidate, 0, len(work))
	for round := 0; len(ordered) < len(work); round++ {
		for _, projectID := range projects {
			if round < len(byProject[projectID]) {
				ordered = append(ordered, byProject[projectID][round])
			}
		}
	}
	return ordered
}

func businessTaskPriority(task rundomain.TaskType) int {
	switch task {
	case rundomain.TaskReason, rundomain.TaskBootstrap:
		return 0
	case rundomain.TaskExplore:
		return 1
	default:
		return 2
	}
}

func eligibleAt(candidate WorkCandidate, now time.Time) bool {
	if candidate.RetryState != rundomain.RetryEligible && candidate.RetryState != rundomain.RetryBackoff {
		return false
	}
	return candidate.NextEligibleAt.IsZero() || !candidate.NextEligibleAt.After(now)
}
