package project

import (
	"context"
	"database/sql"
)

// HintStore encapsulates SQL queries for the hints table.
type HintStore struct{}

// ListByProject returns all hints for a project.
func (r *HintStore) ListByProject(ctx context.Context, conn DBTX, projectID string) ([]Hint, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT id, content, creator, created_at, generation, withdrawn_at, withdrawn_by, withdrawal_reason
		FROM hints WHERE project_id = ? ORDER BY created_at`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hints := []Hint{}
	for rows.Next() {
		h, err := scanHint(rows)
		if err != nil {
			return nil, err
		}
		hints = append(hints, h)
	}
	return hints, rows.Err()
}

// Create inserts a new hint.
func (r *HintStore) Create(ctx context.Context, conn DBTX, id, projectID, content, creator, now string, generation int) error {
	_, err := conn.ExecContext(ctx,
		"INSERT INTO hints (id, project_id, content, creator, created_at, generation) VALUES (?, ?, ?, ?, ?, ?)",
		id, projectID, content, creator, now, generation)
	return err
}

// GetByID returns a single hint.
func (r *HintStore) GetByID(ctx context.Context, conn DBTX, projectID, hintID string) (*Hint, error) {
	h, err := scanHint(conn.QueryRowContext(ctx, `
		SELECT id, content, creator, created_at, generation, withdrawn_at, withdrawn_by, withdrawal_reason
		FROM hints WHERE id = ? AND project_id = ?`, hintID, projectID))
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// Withdraw marks an active hint as withdrawn exactly once.
func (r *HintStore) Withdraw(ctx context.Context, tx DBTX, projectID, hintID, actor, reason, now string) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE hints SET withdrawn_at = ?, withdrawn_by = ?, withdrawal_reason = ?
		WHERE id = ? AND project_id = ? AND withdrawn_at IS NULL`,
		now, actor, reason, hintID, projectID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type hintScanner interface {
	Scan(dest ...any) error
}

func scanHint(row hintScanner) (Hint, error) {
	var hint Hint
	var withdrawnAt, withdrawnBy, reason sql.NullString
	if err := row.Scan(&hint.ID, &hint.Content, &hint.Creator, &hint.CreatedAt, &hint.Generation, &withdrawnAt, &withdrawnBy, &reason); err != nil {
		return Hint{}, err
	}
	if withdrawnAt.Valid {
		hint.WithdrawnAt = &withdrawnAt.String
	}
	if withdrawnBy.Valid {
		hint.WithdrawnBy = &withdrawnBy.String
	}
	if reason.Valid {
		hint.WithdrawalReason = &reason.String
	}
	hint.Status = "active"
	if hint.WithdrawnAt != nil {
		hint.Status = "withdrawn"
	}
	return hint, nil
}
