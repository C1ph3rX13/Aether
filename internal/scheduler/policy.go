package scheduler

import (
	"fmt"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// RunSchedulerPolicy contains scheduling decisions without transport,
// Docker, or legacy Dispatcher configuration types.
type RunSchedulerPolicy struct {
	PollInterval       time.Duration
	MaxWorkers         int
	MaxProjectWorkers  int
	MaxRunningProjects int
	WorkerHealthcheck  bool
	ExecutionPolicies  map[string]map[rundomain.TaskType]rundomain.TaskExecutionPolicy
}

func (policy RunSchedulerPolicy) Validate() error {
	if !wholeSecond(policy.PollInterval) || policy.PollInterval > 24*time.Hour {
		return fmt.Errorf("scheduler poll interval is invalid")
	}
	if policy.MaxWorkers <= 0 || policy.MaxWorkers > rundomain.MaximumObservedRunIDs ||
		policy.MaxProjectWorkers <= 0 || policy.MaxRunningProjects <= 0 {
		return fmt.Errorf("scheduler capacity is invalid")
	}
	if len(policy.ExecutionPolicies) == 0 {
		return fmt.Errorf("scheduler execution policies are required")
	}
	for worker, tasks := range policy.ExecutionPolicies {
		if worker == "" || len(tasks) == 0 {
			return fmt.Errorf("scheduler execution policy Worker is invalid")
		}
		for task, configured := range tasks {
			if err := configured.Validate(task); err != nil {
				return fmt.Errorf("scheduler execution policy for %s/%s: %w", worker, task, err)
			}
		}
	}
	return nil
}

func (policy RunSchedulerPolicy) ExecutionPolicy(workerName string, task rundomain.TaskType) (rundomain.TaskExecutionPolicy, error) {
	if policy.ExecutionPolicies != nil {
		resolved := policy.ExecutionPolicies[workerName][task]
		if err := resolved.Validate(task); err != nil {
			return rundomain.TaskExecutionPolicy{}, fmt.Errorf("execution policy for %s/%s: %w", workerName, task, err)
		}
		return resolved, nil
	}
	return rundomain.TaskExecutionPolicy{}, fmt.Errorf("scheduler execution policies are required")
}

func wholeSecond(value time.Duration) bool {
	return value >= time.Second && value%time.Second == 0
}
