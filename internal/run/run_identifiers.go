package run

import "github.com/google/uuid"

// NewRunID creates an opaque identifier owned by the Run domain.
func NewRunID() RunID {
	return RunID("run_" + uuid.NewString())
}
