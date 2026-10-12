package project

import "context"

// Export returns a full project detail for export.
func (s *Service) Export(ctx context.Context, projectID string) (ProjectDetail, error) {
	return s.Get(ctx, projectID)
}
