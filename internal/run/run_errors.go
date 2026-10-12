package run

import "fmt"

// ErrorCode is stable across in-process and future HTTP control-plane adapters.
type ErrorCode string

const (
	ErrorInvalidArgument           ErrorCode = "invalid_argument"
	ErrorNotFound                  ErrorCode = "not_found"
	ErrorProjectInactive           ErrorCode = "project_inactive"
	ErrorProjectPaused             ErrorCode = "project_paused"
	ErrorProjectCompleted          ErrorCode = "project_completed"
	ErrorBlackboardChanged         ErrorCode = "blackboard_changed"
	ErrorFactRetracted             ErrorCode = "fact_retracted"
	ErrorIntentCancelled           ErrorCode = "intent_cancelled"
	ErrorOperatorCompleted         ErrorCode = "operator_completed"
	ErrorProjectDeleted            ErrorCode = "project_deleted"
	ErrorStaleInput                ErrorCode = "stale_input"
	ErrorTargetUnavailable         ErrorCode = "target_unavailable"
	ErrorWorkClaimed               ErrorCode = "work_claimed"
	ErrorInvalidResult             ErrorCode = "invalid_result"
	ErrorFenced                    ErrorCode = "fenced"
	ErrorResultConflict            ErrorCode = "result_conflict"
	ErrorNotReady                  ErrorCode = "not_ready"
	ErrorConfigurationRequired     ErrorCode = "configuration_required"
	ErrorCapacityExceeded          ErrorCode = "capacity_exceeded"
	ErrorRetryExhausted            ErrorCode = "retry_exhausted"
	ErrorRetryDeferred             ErrorCode = "retry_deferred"
	ErrorExecutionContractMismatch ErrorCode = "execution_contract_mismatch"
	ErrorDispatcherRestarted       ErrorCode = "dispatcher_restarted"
	ErrorDispatcherLeaseHeld       ErrorCode = "dispatcher_lease_held"
	ErrorConflict                  ErrorCode = "conflict"
	ErrorForbidden                 ErrorCode = "forbidden"
	ErrorInternal                  ErrorCode = "internal_error"
)

// DomainError reports a business rejection without leaking request contents.
type DomainError struct {
	Code   ErrorCode
	Detail string
}

func (e *DomainError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

func newError(code ErrorCode, detail string) *DomainError {
	return &DomainError{Code: code, Detail: detail}
}
