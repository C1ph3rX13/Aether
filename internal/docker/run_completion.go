package docker

import (
	"context"

	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

// CompleteRun stops only Execs owned by the Run. Project lifecycle
// reconciliation owns the shared container.
func (r *Runtime) CompleteRun(ctx context.Context, runID rundomain.RunID) error {
	return r.stopProjectRunExecs(ctx, runID)
}
