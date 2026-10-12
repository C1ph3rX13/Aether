package run

import (
	"context"
	"fmt"
)

const MaximumProjectRuntimeStatusBatch = 128

type ProjectRuntimeStatus string

const (
	ProjectRuntimeActive    ProjectRuntimeStatus = "active"
	ProjectRuntimePaused    ProjectRuntimeStatus = "paused"
	ProjectRuntimeCompleted ProjectRuntimeStatus = "completed"
	ProjectRuntimeMissing   ProjectRuntimeStatus = "missing"
)

type ProjectRuntimeStatusRequest struct {
	DispatcherID           string       `json:"dispatcher_id"`
	DispatcherFencingToken FencingToken `json:"dispatcher_fencing_token"`
	ProjectIDs             []string     `json:"project_ids"`
}

func (request ProjectRuntimeStatusRequest) Validate() error {
	if !bounded(request.DispatcherID, 256) || request.DispatcherFencingToken <= 0 ||
		len(request.ProjectIDs) == 0 || len(request.ProjectIDs) > MaximumProjectRuntimeStatusBatch {
		return newError(ErrorInvalidArgument, "Project runtime status request is invalid")
	}
	seen := make(map[string]struct{}, len(request.ProjectIDs))
	for _, projectID := range request.ProjectIDs {
		if !bounded(projectID, 256) {
			return newError(ErrorInvalidArgument, "Project runtime status Project is invalid")
		}
		if _, duplicate := seen[projectID]; duplicate {
			return newError(ErrorInvalidArgument, "Project runtime status Projects must be unique")
		}
		seen[projectID] = struct{}{}
	}
	return nil
}

type ProjectRuntimeStatusResult struct {
	ProjectID string               `json:"project_id"`
	Status    ProjectRuntimeStatus `json:"status"`
}

type ProjectRuntimeStatusLookup func(context.Context, []string) ([]ProjectRuntimeStatusResult, error)

func ValidateProjectRuntimeStatusResults(projectIDs []string, results []ProjectRuntimeStatusResult) error {
	if len(results) != len(projectIDs) {
		return fmt.Errorf("Control Plane Project runtime status response is incomplete")
	}
	for index, result := range results {
		if result.ProjectID != projectIDs[index] {
			return fmt.Errorf("Control Plane Project runtime status response order is invalid")
		}
		switch result.Status {
		case ProjectRuntimeActive, ProjectRuntimePaused, ProjectRuntimeCompleted, ProjectRuntimeMissing:
		default:
			return fmt.Errorf("Control Plane Project runtime status is invalid")
		}
	}
	return nil
}
