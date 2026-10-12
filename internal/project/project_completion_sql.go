package project

import (
	"context"
)

// CompletionStore owns immutable Project completion records.
type CompletionStore struct{}

func (r *CompletionStore) Create(
	ctx context.Context,
	conn DBTX,
	projectID string,
	generation int,
	intentID, description, actor, completedAt string,
) error {
	_, err := conn.ExecContext(ctx,
		`INSERT INTO project_completions
		(project_id, generation, intent_id, description, actor, completed_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, generation, intentID, description, actor, completedAt)
	return err
}

func (r *CompletionStore) GetGeneration(ctx context.Context, conn DBTX, projectID string, generation int) (CompletionRecord, error) {
	var record CompletionRecord
	err := conn.QueryRowContext(ctx,
		`SELECT generation, intent_id, description, actor, completed_at
		FROM project_completions WHERE project_id = ? AND generation = ?`,
		projectID, generation,
	).Scan(&record.Generation, &record.IntentID, &record.Description, &record.Actor, &record.CompletedAt)
	return record, err
}

func (r *CompletionStore) ListByProject(ctx context.Context, conn DBTX, projectID string) ([]CompletionRecord, error) {
	rows, err := conn.QueryContext(ctx,
		`SELECT generation, intent_id, description, actor, completed_at
		FROM project_completions WHERE project_id = ? ORDER BY generation`,
		projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := []CompletionRecord{}
	for rows.Next() {
		var record CompletionRecord
		if err := rows.Scan(&record.Generation, &record.IntentID, &record.Description, &record.Actor, &record.CompletedAt); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
