package scheduler

import (
	"context"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
	"github.com/C1ph3rX13/Aether/internal/worker"
)

// RunResources is the scheduler-owned lifecycle view of active Docker Execs.
// Launch remains private to the factory that builds a Run Executor.
type RunResources interface {
	CompleteRun(context.Context, rundomain.RunID) error
	StopExecutionHandle(context.Context, rundomain.ExecutionHandle) error
}

// AdmissionPreparation contains a selected lightweight Candidate and only
// Dispatcher-owned execution configuration. It never carries Project input.
type AdmissionPreparation struct {
	ProjectID           string
	ConfigRevision      string
	Catalog             *worker.Catalog
	Task                rundomain.TaskType
	TargetIntentID      *string
	CandidateGeneration int
	CandidateRevision   int64
	WorkKey             rundomain.WorkKey
	WorkerName          string
	Image               rundomain.ImageSnapshot
	DispatcherLease     rundomain.DispatcherLease
	Timeouts            rundomain.TimeoutSnapshot
	RetryPolicy         rundomain.RetryPolicy
	MaxIntents          int
}

type ExecutionBinding interface {
	BindExecution(context.Context, rundomain.AdmittedRun) (rundomain.Executor, error)
}

type PreparedAdmission struct {
	Request rundomain.StartRequest
	Binding ExecutionBinding
}

// RunExecutionFactory validates local configuration before Admission.
// Task-specific input rendering remains hidden behind the returned Binding.
type RunExecutionFactory interface {
	PrepareAdmission(AdmissionPreparation) (PreparedAdmission, error)
}
