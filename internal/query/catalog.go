package query

import (
	"context"
	"database/sql"

	"github.com/C1ph3rX13/Aether/internal/project"
)

// ListProjectCatalog derives every entry from one database snapshot.
func (s *Service) ListProjectCatalog(ctx context.Context) ([]ProjectCatalogEntry, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if s.activityReader == nil {
		return nil, project.Internal("Project Activity reader is not configured")
	}
	statuses, err := s.activityReader.ReadProjectExecutionStatuses(ctx, tx)
	if err != nil {
		return nil, project.Internal(err.Error())
	}

	rows, err := tx.QueryContext(ctx, catalogQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]ProjectCatalogEntry, 0)
	for rows.Next() {
		var entry ProjectCatalogEntry
		var configRevision sql.NullString
		if err := rows.Scan(
			&entry.Project.ID,
			&entry.Project.Title,
			&entry.Project.Status,
			&entry.Project.BootstrapEnabled,
			&entry.Project.CreatedAt,
			&entry.Project.Generation,
			&entry.Project.Revision,
			&configRevision,
			&entry.Blackboard.Facts.Total,
			&entry.Blackboard.Facts.Active,
			&entry.Blackboard.Intents.Total,
			&entry.Blackboard.Intents.Open,
			&entry.Blackboard.Hints.Total,
			&entry.Blackboard.Hints.Active,
		); err != nil {
			return nil, err
		}
		entry.Project.ConfigRevision = configRevision.String
		status, ok := statuses[entry.Project.ID]
		if !ok {
			return nil, project.Internal("Project Execution status is missing")
		}
		entry.Execution.Status = status
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return entries, nil
}

const catalogQuery = `
SELECT
    p.id,
    p.title,
    p.status,
    p.bootstrap_enabled,
    p.created_at,
    p.generation,
    p.blackboard_revision,
    p.config_revision,
    (SELECT COUNT(*) FROM facts f WHERE f.project_id = p.id),
    (SELECT COUNT(*) FROM facts f WHERE f.project_id = p.id AND f.retracted_at IS NULL),
    (SELECT COUNT(*) FROM intents i WHERE i.project_id = p.id),
    (SELECT COUNT(*) FROM intents i WHERE i.project_id = p.id AND i.to_fact_id IS NULL AND i.cancelled_at IS NULL),
    (SELECT COUNT(*) FROM hints h WHERE h.project_id = p.id),
    (SELECT COUNT(*) FROM hints h WHERE h.project_id = p.id AND h.withdrawn_at IS NULL)
FROM projects p
ORDER BY p.created_at, p.id`
