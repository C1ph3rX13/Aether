package main

import (
	"fmt"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
)

// runSchedulerPolicy assembles global scheduling controls and the immutable
// execution policies resolved by Config for each Worker/task pair.
func runSchedulerPolicy(configured *config.DispatchConfig) (scheduler.RunSchedulerPolicy, error) {
	if configured == nil {
		return scheduler.RunSchedulerPolicy{}, fmt.Errorf("Dispatcher configuration is required")
	}
	policy := scheduler.RunSchedulerPolicy{
		PollInterval: time.Duration(configured.Runtime.Interval) * time.Second, MaxWorkers: configured.Runtime.MaxWorkers,
		MaxProjectWorkers: configured.Runtime.MaxProjectWorkers, MaxRunningProjects: configured.Runtime.MaxRunningProjects,
		WorkerHealthcheck: configured.Runtime.WorkerHealthcheck == "startup_and_task",
		ExecutionPolicies: configured.ExecutionPolicies,
	}
	if err := policy.Validate(); err != nil {
		return scheduler.RunSchedulerPolicy{}, err
	}
	return policy, nil
}
