package config

import (
	"fmt"
	"time"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/spec"
)

const runStopGraceSeconds = 15

func taskExecutionPolicy(draft DynamicDraft, configured DynamicWorkerDraft, task rundomain.TaskType) (rundomain.TaskExecutionPolicy, error) {
	var phase, conclude, recovery, maxIntents int
	var retry DynamicRetryPolicyDraft
	switch task {
	case rundomain.TaskReason:
		phase, maxIntents, retry = draft.Tasks.Reason.PrimarySeconds, draft.Tasks.Reason.MaxIntents, draft.Retry.Reason
	case rundomain.TaskExplore:
		phase, conclude, recovery, retry = draft.Tasks.Explore.PrimarySeconds, draft.Tasks.Explore.ConcludeSeconds, draft.Tasks.Explore.RecoverySeconds, draft.Retry.Explore
	case rundomain.TaskBootstrap:
		phase, conclude, recovery, retry = draft.Tasks.Bootstrap.PrimarySeconds, draft.Tasks.Bootstrap.ConcludeSeconds, draft.Tasks.Bootstrap.RecoverySeconds, draft.Retry.Bootstrap
	default:
		return rundomain.TaskExecutionPolicy{}, fmt.Errorf("unsupported execution task %q", task)
	}
	for name, value := range map[string]*int{"primary": &phase, "conclude": &conclude, "recovery": &recovery} {
		if override, ok := configured.TimeoutOverrides[string(task)+"."+name]; ok {
			*value = override
		}
	}
	if phase <= 0 || phase > 86400 || task != rundomain.TaskReason && (conclude <= 0 || recovery <= 0) ||
		conclude < 0 || conclude > 86400 || recovery < 0 || recovery > 86400 {
		return rundomain.TaskExecutionPolicy{}, fmt.Errorf("execution phase limits for %s/%s are invalid", configured.Name, task)
	}
	if retry.InitialIntervalSeconds <= 0 || retry.InitialIntervalSeconds > 86400 || retry.MaxIntervalSeconds <= 0 || retry.MaxIntervalSeconds > 86400 {
		return rundomain.TaskExecutionPolicy{}, fmt.Errorf("execution retry intervals for %s are invalid", task)
	}
	grace := min(runStopGraceSeconds, phase)
	if conclude > 0 {
		grace = min(grace, conclude)
	}
	if recovery > 0 {
		grace = min(grace, recovery)
	}
	runSeconds := phase + grace
	if task == rundomain.TaskReason {
		runSeconds += spec.ResultFinalizationPhaseSeconds(phase) + grace
	}
	if conclude > 0 {
		runSeconds += conclude + grace + spec.ResultFinalizationPhaseSeconds(conclude) + grace
	}
	if recovery > 0 {
		runSeconds += recovery + grace
	}
	policy := rundomain.TaskExecutionPolicy{
		Timeouts: rundomain.TimeoutSnapshot{
			RunSeconds: runSeconds, PhaseSeconds: phase, FallbackPhaseSeconds: conclude, RecoveryPhaseSeconds: recovery, GraceSeconds: grace,
		},
		RetryPolicy: rundomain.RetryPolicy{
			MaxAttempts: retry.MaxAttempts, InitialBackoff: time.Duration(retry.InitialIntervalSeconds) * time.Second,
			MaxBackoff: time.Duration(retry.MaxIntervalSeconds) * time.Second, Multiplier: retry.Multiplier, DeclineCooldown: 5 * time.Second,
		},
		MaxProposedIntents: maxIntents,
	}
	if err := policy.Validate(task); err != nil {
		return rundomain.TaskExecutionPolicy{}, fmt.Errorf("execution policy for %s/%s: %w", configured.Name, task, err)
	}
	return policy, nil
}
