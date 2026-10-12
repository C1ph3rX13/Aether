package query

import (
	"context"
	"database/sql"

	"github.com/C1ph3rX13/Aether/internal/project"
)

const (
	defaultLogLimit = 50
	maximumLogLimit = 100
)

// ListProjectLog derives complete display groups from committed Blackboard revisions.
func (s *Service) ListProjectLog(ctx context.Context, projectID string, query ProjectLogQuery) (LogPage, error) {
	if query.BeforeRevision < 0 {
		return LogPage{}, project.InvalidArgument("before_revision must not be negative")
	}
	if query.Limit == 0 {
		query.Limit = defaultLogLimit
	}
	if query.Limit < 1 || query.Limit > maximumLogLimit {
		return LogPage{}, project.InvalidArgument("limit must be between 1 and 100")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LogPage{}, err
	}
	defer tx.Rollback()

	historyPage, err := s.history.ReadPage(ctx, tx, projectID, project.HistoryPageQuery{
		BeforeRevision: query.BeforeRevision,
		Limit:          query.Limit,
	})
	if err != nil {
		return LogPage{}, err
	}
	page := LogPage{
		ProjectID:                 historyPage.ProjectID,
		AvailableFromRevision:     historyPage.AvailableFromRevision,
		AvailableFromOccurredAt:   historyPage.AvailableFromOccurredAt,
		CurrentRevision:           historyPage.CurrentRevision,
		CurrentRevisionOccurredAt: historyPage.CurrentRevisionOccurredAt,
		Revisions:                 make([]LogRevision, 0, len(historyPage.Revisions)),
		NextBeforeRevision:        historyPage.NextBeforeRevision,
	}
	for _, change := range historyPage.Revisions {
		group, err := decodeLogRevision(change)
		if err != nil {
			return LogPage{}, err
		}
		page.Revisions = append(page.Revisions, group)
	}
	if err := tx.Commit(); err != nil {
		return LogPage{}, err
	}
	return page, nil
}
