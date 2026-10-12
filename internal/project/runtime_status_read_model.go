package project

import (
	"context"
	"fmt"
)

const MaximumRuntimeStatusProjects = 128

const maximumProjectIdentityBytes = 256

// RuntimeStatusReadModel distinguishes a missing Project from a persisted
// lifecycle status without leaking the Project store's map representation.
type RuntimeStatusReadModel struct {
	ProjectID string
	Status    ProjectStatus
	Missing   bool
}

func (s *Service) ReadRuntimeStatuses(
	ctx context.Context,
	tx DBTX,
	projectIDs []string,
) ([]RuntimeStatusReadModel, error) {
	if len(projectIDs) == 0 || len(projectIDs) > MaximumRuntimeStatusProjects {
		return nil, fmt.Errorf("Project runtime status request is invalid")
	}
	seen := make(map[string]struct{}, len(projectIDs))
	for _, projectID := range projectIDs {
		if projectID == "" || len(projectID) > maximumProjectIdentityBytes {
			return nil, fmt.Errorf("Project runtime status identity is invalid")
		}
		if _, duplicate := seen[projectID]; duplicate {
			return nil, fmt.Errorf("Project runtime status identities must be unique")
		}
		seen[projectID] = struct{}{}
	}
	stored, err := listProjectStatuses(ctx, tx, projectIDs)
	if err != nil {
		return nil, fmt.Errorf("list Project runtime statuses: %w", err)
	}
	result := make([]RuntimeStatusReadModel, 0, len(projectIDs))
	for _, projectID := range projectIDs {
		status, exists := stored[projectID]
		if !exists {
			result = append(result, RuntimeStatusReadModel{ProjectID: projectID, Missing: true})
			continue
		}
		switch status {
		case ProjectStatusActive, ProjectStatusPaused, ProjectStatusCompleted:
		default:
			return nil, fmt.Errorf("Project %s has unsupported runtime status %q", projectID, status)
		}
		result = append(result, RuntimeStatusReadModel{ProjectID: projectID, Status: status})
	}
	return result, nil
}
