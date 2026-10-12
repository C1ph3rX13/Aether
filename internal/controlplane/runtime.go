package controlplane

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/C1ph3rX13/Aether/internal/project"
	rundomain "github.com/C1ph3rX13/Aether/internal/run"
)

func (control *ControlPlane) ProjectRuntimeStatuses(
	ctx context.Context,
	request rundomain.ProjectRuntimeStatusRequest,
) ([]rundomain.ProjectRuntimeStatusResult, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	tx, err := control.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	observedAt := control.runs.SchedulingObservedAt()
	if err := control.runs.AuthorizeDispatcherAt(
		ctx, tx, request.DispatcherID, request.DispatcherFencingToken, observedAt,
	); err != nil {
		return nil, err
	}
	stored, err := control.projects.ReadRuntimeStatuses(ctx, tx, request.ProjectIDs)
	if err != nil {
		return nil, err
	}
	results := make([]rundomain.ProjectRuntimeStatusResult, 0, len(stored))
	for _, evidence := range stored {
		status := rundomain.ProjectRuntimeMissing
		if !evidence.Missing {
			switch evidence.Status {
			case project.ProjectStatusActive:
				status = rundomain.ProjectRuntimeActive
			case project.ProjectStatusPaused:
				status = rundomain.ProjectRuntimePaused
			case project.ProjectStatusCompleted:
				status = rundomain.ProjectRuntimeCompleted
			default:
				return nil, fmt.Errorf("Project %s has unsupported runtime status %q", evidence.ProjectID, evidence.Status)
			}
		}
		results = append(results, rundomain.ProjectRuntimeStatusResult{ProjectID: evidence.ProjectID, Status: status})
	}
	return results, nil
}
