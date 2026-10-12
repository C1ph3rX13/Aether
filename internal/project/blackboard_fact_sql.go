package project

import (
	"context"
	"database/sql"
)

// FactStore encapsulates SQL queries for the facts table.
type FactStore struct{}

// ListByProject returns all facts for a project.
func (r *FactStore) ListByProject(ctx context.Context, conn DBTX, projectID string) ([]Fact, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT id, description, supersedes_fact_id, retracted_at, retracted_by,
		       retraction_reason, replacement_fact_id
		FROM facts WHERE project_id = ? ORDER BY id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	facts := []Fact{}
	for rows.Next() {
		f, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

// Create inserts a new fact.
func (r *FactStore) Create(ctx context.Context, tx DBTX, id, projectID, description string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO facts (id, project_id, description) VALUES (?, ?, ?)", id, projectID, description)
	return err
}

// CreateSuperseding inserts a new fact that corrects an earlier fact.
func (r *FactStore) CreateSuperseding(ctx context.Context, tx DBTX, id, projectID, description, supersedesID string) error {
	_, err := tx.ExecContext(ctx,
		"INSERT INTO facts (id, project_id, description, supersedes_fact_id) VALUES (?, ?, ?, ?)",
		id, projectID, description, supersedesID)
	return err
}

func (r *FactStore) GetByID(ctx context.Context, conn DBTX, projectID, factID string) (Fact, error) {
	return scanFact(conn.QueryRowContext(ctx, `
		SELECT id, description, supersedes_fact_id, retracted_at, retracted_by,
		       retraction_reason, replacement_fact_id
		FROM facts WHERE id = ? AND project_id = ?`, factID, projectID))
}

// Retract marks an active fact as retracted exactly once.
func (r *FactStore) Retract(ctx context.Context, tx DBTX, projectID, factID, actor, reason, now string, replacementID *string) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE facts
		SET retracted_at = ?, retracted_by = ?, retraction_reason = ?, replacement_fact_id = ?
		WHERE id = ? AND project_id = ? AND retracted_at IS NULL`,
		now, actor, reason, replacementID, factID, projectID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Exists checks if a fact exists in a project.
func (r *FactStore) Exists(ctx context.Context, conn DBTX, projectID, factID string) (bool, error) {
	var exists int
	err := conn.QueryRowContext(ctx, "SELECT 1 FROM facts WHERE id = ? AND project_id = ?", factID, projectID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// IsUsableSource reports whether a fact exists and has not been retracted.
func (r *FactStore) IsUsableSource(ctx context.Context, conn DBTX, projectID, factID string) (bool, error) {
	var exists int
	err := conn.QueryRowContext(ctx,
		"SELECT 1 FROM facts WHERE id = ? AND project_id = ? AND retracted_at IS NULL",
		factID, projectID).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

type factScanner interface {
	Scan(dest ...any) error
}

func scanFact(row factScanner) (Fact, error) {
	var fact Fact
	var supersedes, retractedAt, retractedBy, reason, replacement sql.NullString
	if err := row.Scan(&fact.ID, &fact.Description, &supersedes, &retractedAt, &retractedBy, &reason, &replacement); err != nil {
		return Fact{}, err
	}
	fact.Status = FactStatusActive
	if supersedes.Valid {
		fact.Supersedes = &supersedes.String
	}
	if retractedAt.Valid {
		fact.Status = FactStatusRetracted
		fact.RetractedAt = &retractedAt.String
	}
	if retractedBy.Valid {
		fact.RetractedBy = &retractedBy.String
	}
	if reason.Valid {
		fact.RetractionReason = &reason.String
	}
	if replacement.Valid {
		fact.ReplacementFactID = &replacement.String
	}
	return fact, nil
}
