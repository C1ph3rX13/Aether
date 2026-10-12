package project

import "errors"

// ErrProjectNotFound is returned when a Project lookup fails.
var ErrProjectNotFound = errors.New("Project not found")

// ErrorCode is the stable business classification returned by Project commands.
type ErrorCode string

const (
	ErrorInvalidArgument       ErrorCode = "invalid_argument"
	ErrorNotFound              ErrorCode = "not_found"
	ErrorForbidden             ErrorCode = "forbidden"
	ErrorConflict              ErrorCode = "conflict"
	ErrorInternal              ErrorCode = "internal_error"
	ErrorConfigurationRequired ErrorCode = "configuration_required"
)

// DomainError reports a stable Project command failure without transport details.
type DomainError struct {
	Code            ErrorCode
	Detail          string
	CurrentRevision *int64
}

func (e *DomainError) Error() string { return e.Detail }

func InvalidArgument(detail string) *DomainError {
	return &DomainError{Code: ErrorInvalidArgument, Detail: detail}
}

func NotFound(detail string) *DomainError {
	return &DomainError{Code: ErrorNotFound, Detail: detail}
}

func Forbidden(detail string) *DomainError {
	return &DomainError{Code: ErrorForbidden, Detail: detail}
}

func Internal(detail string) *DomainError {
	return &DomainError{Code: ErrorInternal, Detail: detail}
}

func ConfigurationRequired(detail string) *DomainError {
	return &DomainError{Code: ErrorConfigurationRequired, Detail: detail}
}

func Conflict(detail string) *DomainError {
	return &DomainError{Code: ErrorConflict, Detail: detail}
}

func RevisionConflict(detail string, currentRevision int64) *DomainError {
	return &DomainError{Code: ErrorConflict, Detail: detail, CurrentRevision: &currentRevision}
}
