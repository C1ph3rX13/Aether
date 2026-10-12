package main

import (
	"testing"
	"time"

	"github.com/C1ph3rX13/Aether/internal/config"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/scheduler"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

func TestRunSchedulerPolicyTranslatesResolvedExecutionPoliciesAtAppBoundary(t *testing.T) {
	retry := rundomain.RetryPolicy{MaxAttempts: 3, InitialBackoff: 10 * time.Millisecond, MaxBackoff: time.Second, Multiplier: 2, DeclineCooldown: 5 * time.Second}
	config := &config.DispatchConfig{
		Runtime: config.RuntimeConfig{
			Interval: 2, MaxWorkers: 4, MaxProjectWorkers: 2, MaxRunningProjects: 3,
			WorkerHealthcheck: "startup_and_task",
		},
		Workers: []worker.WorkerConfig{{Name: "worker-a"}},
		ExecutionPolicies: map[string]map[rundomain.TaskType]rundomain.TaskExecutionPolicy{
			"worker-a": {
				rundomain.TaskReason:    {Timeouts: rundomain.TimeoutSnapshot{RunSeconds: 48, PhaseSeconds: 12, GraceSeconds: 12}, RetryPolicy: retry, MaxProposedIntents: 5},
				rundomain.TaskExplore:   {Timeouts: rundomain.TimeoutSnapshot{RunSeconds: 80, PhaseSeconds: 40, FallbackPhaseSeconds: 8, GraceSeconds: 8}, RetryPolicy: retry},
				rundomain.TaskBootstrap: {Timeouts: rundomain.TimeoutSnapshot{RunSeconds: 70, PhaseSeconds: 20, FallbackPhaseSeconds: 10, GraceSeconds: 10}, RetryPolicy: retry},
			},
		},
	}

	policy, err := runSchedulerPolicy(config)
	if err != nil {
		t.Fatal(err)
	}
	if policy.PollInterval != 2*time.Second || policy.MaxWorkers != 4 ||
		!policy.WorkerHealthcheck {
		t.Fatalf("scheduler policy = %+v", policy)
	}
	if policy.ExecutionPolicies["worker-a"][rundomain.TaskExplore].Timeouts.FallbackPhaseSeconds != 8 ||
		policy.ExecutionPolicies["worker-a"][rundomain.TaskBootstrap].Timeouts.FallbackPhaseSeconds != 10 ||
		policy.ExecutionPolicies["worker-a"][rundomain.TaskReason].Timeouts.PhaseSeconds != 12 {
		t.Fatalf("scheduler execution policy = %+v", policy)
	}
}

func executionPolicyTableForWorkers(
	workerNames []string,
	retry rundomain.RetryPolicy,
	phaseSeconds, fallbackSeconds, recoverySeconds, graceSeconds, reasonRunSeconds, phasedRunSeconds, maxIntents int,
) map[string]map[rundomain.TaskType]rundomain.TaskExecutionPolicy {
	result := make(map[string]map[rundomain.TaskType]rundomain.TaskExecutionPolicy, len(workerNames))
	for _, workerName := range workerNames {
		result[workerName] = map[rundomain.TaskType]rundomain.TaskExecutionPolicy{
			rundomain.TaskReason: {
				Timeouts:    rundomain.TimeoutSnapshot{RunSeconds: reasonRunSeconds, PhaseSeconds: phaseSeconds, GraceSeconds: graceSeconds},
				RetryPolicy: retry, MaxProposedIntents: maxIntents,
			},
			rundomain.TaskExplore: {
				Timeouts:    rundomain.TimeoutSnapshot{RunSeconds: phasedRunSeconds, PhaseSeconds: phaseSeconds, FallbackPhaseSeconds: fallbackSeconds, RecoveryPhaseSeconds: recoverySeconds, GraceSeconds: graceSeconds},
				RetryPolicy: retry,
			},
			rundomain.TaskBootstrap: {
				Timeouts:    rundomain.TimeoutSnapshot{RunSeconds: phasedRunSeconds, PhaseSeconds: phaseSeconds, FallbackPhaseSeconds: fallbackSeconds, RecoveryPhaseSeconds: recoverySeconds, GraceSeconds: graceSeconds},
				RetryPolicy: retry,
			},
		}
	}
	return result
}

func bindExecutionPoliciesToCatalog(policy scheduler.RunSchedulerPolicy, catalog *worker.Catalog) scheduler.RunSchedulerPolicy {
	template := policy.ExecutionPolicies["__test_worker__"]
	if template == nil {
		return policy
	}
	bound := make(map[string]map[rundomain.TaskType]rundomain.TaskExecutionPolicy, len(catalog.Workers()))
	for _, configured := range catalog.Workers() {
		bound[configured.Name] = template
	}
	policy.ExecutionPolicies = bound
	return policy
}
