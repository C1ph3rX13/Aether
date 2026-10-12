package run

import (
	"fmt"
	"time"
)

type retryOutcome struct {
	state          RetryState
	failureCount   int64
	workEligibleAt *time.Time
	runEligibleAt  *time.Time
}

// nextRetryOutcome distinguishes replacement of an execution owner or input
// from a technical failure of the work. Attempt numbers remain monotonic.
func nextRetryOutcome(run ExecutionRun, status Status, code ErrorCode, policy RetryPolicy, failures int64, now time.Time) (retryOutcome, error) {
	outcome := retryOutcome{failureCount: failures}
	switch status {
	case StatusDeclined:
		outcome.state = RetryEligible
		retryAt := now.Add(policy.DeclineCooldown)
		outcome.runEligibleAt = &retryAt
	case StatusFailed:
		if code == ErrorExecutionContractMismatch {
			outcome.state = RetryEligible
			return outcome, nil
		}
		outcome.failureCount++
	case StatusInterrupted:
		if code != ErrorDispatcherRestarted {
			outcome.failureCount++
		}
	case StatusCancelled:
		if code == ErrorProjectPaused || (code == ErrorBlackboardChanged && run.Task == TaskBootstrap) {
			outcome.state = RetryEligible
		} else {
			outcome.state = RetryClosed
		}
	case StatusCommitted, StatusStale:
		outcome.state = RetryClosed
	default:
		return retryOutcome{}, fmt.Errorf("unsupported Run terminal status %q", status)
	}
	if outcome.state != "" {
		return outcome, nil
	}
	if outcome.failureCount >= int64(policy.MaxAttempts) {
		outcome.state = RetryExhausted
		return outcome, nil
	}
	outcome.state = RetryBackoff
	// Interruptions delay replacement without increasing either the failure
	// budget or its backoff exponent.
	retryAt := now.Add(policy.BackoffFor(run.WorkKey, max(1, outcome.failureCount)))
	outcome.workEligibleAt, outcome.runEligibleAt = &retryAt, &retryAt
	return outcome, nil
}
