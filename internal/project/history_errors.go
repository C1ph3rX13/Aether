package project

import "fmt"

type historyEvidenceConflictError struct {
	detail string
}

func (e *historyEvidenceConflictError) Error() string {
	return e.detail
}

func newHistoryEvidenceConflict(format string, arguments ...any) error {
	return &historyEvidenceConflictError{detail: fmt.Sprintf(format, arguments...)}
}
