package scheduler

import (
	"context"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// ProjectInventoryReconciler owns runtime resource enumeration and bounded
// status lookup batching. It must return without blocking the scheduler on
// container cleanup.
type ProjectInventoryReconciler interface {
	ReconcileProjectInventory(context.Context, rundomain.ProjectRuntimeStatusLookup) error
}
