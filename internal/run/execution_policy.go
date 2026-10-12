package run

import "fmt"

const MaximumReasonProposedIntents = 64

// TaskExecutionPolicy is the resolved execution material for one Worker/task.
// Config owns its derivation; Admission and Scheduler consume the same value.
type TaskExecutionPolicy struct {
	Timeouts           TimeoutSnapshot
	RetryPolicy        RetryPolicy
	MaxProposedIntents int
}

func (policy TaskExecutionPolicy) Validate(task TaskType) error {
	if !task.valid() {
		return newError(ErrorInvalidArgument, "execution policy task is invalid")
	}
	if err := policy.Timeouts.validate(); err != nil {
		return fmt.Errorf("task %s timeouts: %w", task, err)
	}
	if err := validateTaskExecutionPolicy(task, policy.Timeouts, policy.MaxProposedIntents); err != nil {
		return err
	}
	if task == TaskReason && policy.Timeouts.RecoveryPhaseSeconds != 0 {
		return newError(ErrorInvalidArgument, "reason cannot define a recovery timeout")
	}
	normalized, err := policy.RetryPolicy.Normalized()
	if err != nil || normalized != policy.RetryPolicy {
		return newError(ErrorInvalidArgument, "execution retry policy must be complete and valid")
	}
	return nil
}
